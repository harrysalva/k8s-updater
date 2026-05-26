// Package eksaddons validates EKS managed add-on versions against the target
// Kubernetes version. Managed add-ons that are incompatible with the target k8s
// version will be rejected or will malfunction after the cluster upgrade —
// typically without a clear error message to the operator.
//
// Add-ons covered: vpc-cni, coredns, kube-proxy, aws-ebs-csi-driver,
// aws-efs-csi-driver, and any other managed add-on present in the cluster.
//
// SOURCE OF TRUTH: AWS EKS DescribeAddonVersions API (queried at runtime).
//
// LAST VERIFIED: n/a — compatibility data is fetched live from AWS.
package eksaddons

import (
	"context"
	"fmt"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	eksv2 "github.com/aws/aws-sdk-go-v2/service/eks"
	eksv2types "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"upgrade-guardian/internal/checker"
)

const Name = "eks-addons"

type eksClient interface {
	ListAddons(ctx context.Context, in *eksv2.ListAddonsInput, optFns ...func(*eksv2.Options)) (*eksv2.ListAddonsOutput, error)
	DescribeAddon(ctx context.Context, in *eksv2.DescribeAddonInput, optFns ...func(*eksv2.Options)) (*eksv2.DescribeAddonOutput, error)
	DescribeAddonVersions(ctx context.Context, in *eksv2.DescribeAddonVersionsInput, optFns ...func(*eksv2.Options)) (*eksv2.DescribeAddonVersionsOutput, error)
}

type Checker struct {
	newEKS func(ctx context.Context, region string) (eksClient, error)
}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker {
	return &Checker{
		newEKS: func(ctx context.Context, region string) (eksClient, error) {
			awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
			if err != nil {
				return nil, err
			}
			return eksv2.NewFromConfig(awsCfg), nil
		},
	}
}

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(ct checker.ClusterType) bool {
	return ct == checker.ClusterTypeEKS
}

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	if cfg.EKSConfig == nil || cfg.EKSConfig.ClusterName == "" {
		return nil, map[string]string{"skip_reason": "EKSConfig missing"}, nil
	}

	client, err := c.newEKS(ctx, cfg.EKSConfig.Region)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: create EKS client: %w", Name, err)
	}

	// List all managed add-ons in the cluster.
	listOut, err := client.ListAddons(ctx, &eksv2.ListAddonsInput{
		ClusterName: &cfg.EKSConfig.ClusterName,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: ListAddons: %w", Name, err)
	}

	meta := map[string]string{
		"addons_found": fmt.Sprintf("%d", len(listOut.Addons)),
	}

	if len(listOut.Addons) == 0 {
		return nil, meta, nil
	}

	targetStr := fmt.Sprintf("1.%s", minorStr(cfg.TargetVersion))

	var findings []checker.Finding
	for _, addonName := range listOut.Addons {
		f, err := c.checkAddon(ctx, client, cfg, addonName, targetStr)
		if err != nil {
			// Non-fatal: log as medium finding and continue.
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityMedium,
				Blocker:     false,
				Title:       fmt.Sprintf("Cannot check addon %s compatibility", addonName),
				Description: err.Error(),
				Remediation: "Verify addon compatibility manually in the EKS console.",
				Source:      Name,
				DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-add-ons.html",
			})
		} else {
			findings = append(findings, f...)
		}
	}

	return findings, meta, nil
}

func (c *Checker) checkAddon(ctx context.Context, client eksClient, cfg *checker.CheckConfig, addonName, targetK8s string) ([]checker.Finding, error) {
	// Get installed version.
	descOut, err := client.DescribeAddon(ctx, &eksv2.DescribeAddonInput{
		ClusterName: &cfg.EKSConfig.ClusterName,
		AddonName:   &addonName,
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeAddon: %w", err)
	}
	if descOut.Addon == nil || descOut.Addon.AddonVersion == nil {
		return nil, nil
	}
	installedVersion := *descOut.Addon.AddonVersion

	// Get versions compatible with the target k8s.
	verOut, err := client.DescribeAddonVersions(ctx, &eksv2.DescribeAddonVersionsInput{
		AddonName:         &addonName,
		KubernetesVersion: &targetK8s,
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeAddonVersions: %w", err)
	}

	if len(verOut.Addons) == 0 {
		return nil, nil
	}

	compatibleVersions := collectCompatibleVersions(verOut.Addons)
	if len(compatibleVersions) == 0 {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityHigh,
			Blocker:     false,
			Title:       fmt.Sprintf("No compatible versions found for addon %s with k8s %s", addonName, targetK8s),
			Description: fmt.Sprintf("AWS returned no addon versions for %s compatible with k8s %s. This addon may not be supported in the target version.", addonName, targetK8s),
			Remediation: "Check the EKS console for supported addon versions or remove the addon before upgrading.",
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-add-ons.html",
		}}, nil
	}

	// Check if installed version is in the compatible set.
	if isCompatible(installedVersion, compatibleVersions) {
		// Check if there's a newer version available (informational).
		defaultVer := findDefaultVersion(verOut.Addons)
		if defaultVer != "" && defaultVer != installedVersion {
			return []checker.Finding{{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityInfo,
				Blocker:     false,
				Title:       fmt.Sprintf("Addon %s %s is compatible but not the latest (%s)", addonName, installedVersion, defaultVer),
				Description: fmt.Sprintf("Installed: %s. Latest compatible with k8s %s: %s. Upgrading reduces risk during the cluster upgrade.", installedVersion, targetK8s, defaultVer),
				Remediation: fmt.Sprintf("aws eks update-addon --cluster-name %s --addon-name %s --addon-version %s", cfg.EKSConfig.ClusterName, addonName, defaultVer),
				Source:      Name,
				DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-add-ons.html",
			}}, nil
		}
		return nil, nil
	}

	// Installed version is NOT compatible with the target.
	defaultVer := findDefaultVersion(verOut.Addons)
	return []checker.Finding{{
		CheckerName: Name,
		ClusterType: cfg.ClusterType,
		Severity:    checker.SeverityCritical,
		Blocker:     true,
		Title:       fmt.Sprintf("Addon %s %s is incompatible with k8s %s", addonName, installedVersion, targetK8s),
		Description: fmt.Sprintf("The installed version %s of addon %s is not compatible with k8s %s. After upgrade, the addon will fail to start or will be rejected by the API server.", installedVersion, addonName, targetK8s),
		Remediation: fmt.Sprintf("Upgrade the addon before upgrading k8s:\n  aws eks update-addon --cluster-name %s --addon-name %s --addon-version %s", cfg.EKSConfig.ClusterName, addonName, defaultVer),
		Source:      Name,
		DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-add-ons.html",
	}}, nil
}

func collectCompatibleVersions(addons []eksv2types.AddonInfo) []string {
	var versions []string
	for _, a := range addons {
		for _, v := range a.AddonVersions {
			if v.AddonVersion != nil {
				versions = append(versions, *v.AddonVersion)
			}
		}
	}
	return versions
}

func findDefaultVersion(addons []eksv2types.AddonInfo) string {
	for _, a := range addons {
		for _, v := range a.AddonVersions {
			for _, compat := range v.Compatibilities {
				if compat.DefaultVersion && v.AddonVersion != nil {
					return *v.AddonVersion
				}
			}
		}
	}
	// Fall back to the first compatible version.
	for _, a := range addons {
		for _, v := range a.AddonVersions {
			if v.AddonVersion != nil {
				return *v.AddonVersion
			}
		}
	}
	return ""
}

func isCompatible(installed string, compatible []string) bool {
	for _, v := range compatible {
		if v == installed {
			return true
		}
	}
	return false
}

// minorStr extracts the minor version number as a string from "1.28" or "v1.28.0".
func minorStr(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return "0"
	}
	return parts[1]
}
