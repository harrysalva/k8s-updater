// Package vpccni validates that the AWS VPC CNI plugin (aws-node) installed in
// the cluster is compatible with the target Kubernetes version, and checks for
// prefix-delegation misconfiguration that can cause IP exhaustion after upgrade.
//
// SOURCE OF TRUTH:
//   https://docs.aws.amazon.com/eks/latest/userguide/managing-vpc-cni.html
//   https://github.com/aws/amazon-vpc-cni-k8s/blob/master/CHANGELOG.md
//
// LAST VERIFIED: 2026-05-25
//
// To update: check the EKS addon version table for each k8s minor version and
// bump the minVersionByK8sMinor table below. Also bump LAST VERIFIED.
package vpccni

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	eksv2 "github.com/aws/aws-sdk-go-v2/service/eks"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "vpc-cni-version"

// MatrixLastVerified is the date the minVersionByK8sMinor table was last checked against upstream.
var MatrixLastVerified = time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)

// minVersionByK8sMinor is the minimum aws-node version required for each k8s minor.
// Below this version, the VPC CNI may fail to attach ENIs after the k8s upgrade.
// Derived from EKS managed addon default version table.
var minVersionByK8sMinor = map[int]string{
	25: "1.11.4",
	26: "1.12.6",
	27: "1.14.1",
	28: "1.16.0",
	29: "1.17.1",
	30: "1.18.1",
	31: "1.19.0",
	32: "1.19.0",
	33: "1.20.0",
	34: "1.20.0",
	35: "1.20.0",
	36: "1.21.0",
}

// prefixDelegationMinVersion is the minimum vpc-cni version that supports
// prefix delegation (ENABLE_PREFIX_DELEGATION). Below this, enabling PD
// causes IP allocation failures.
const prefixDelegationMinVersion = "1.11.0"

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(ct checker.ClusterType) bool {
	return ct == checker.ClusterTypeEKS
}

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	installedVersion, err := detectVPCCNIVersion(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: detect aws-node: %w", Name, err)
	}

	meta := map[string]string{
		"installed_version": installedVersion,
	}

	if installedVersion == "" {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityHigh,
			Blocker:     false,
			Title:       "VPC CNI (aws-node) not found in kube-system",
			Description: "The aws-node DaemonSet was not found. EKS clusters should always have VPC CNI installed.",
			Remediation: "Check your EKS cluster configuration. Reinstall the vpc-cni managed addon if missing.",
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-vpc-cni.html",
		}}, meta, nil
	}

	targetMinor := parseMinor(cfg.TargetVersion)
	var findings []checker.Finding

	// Check 1: version compatibility with target k8s.
	findings = append(findings, c.checkVersionCompat(cfg, installedVersion, targetMinor, meta)...)

	// Check 2: prefix delegation + version constraint.
	findings = append(findings, c.checkPrefixDelegation(ctx, cfg, installedVersion)...)

	// Check 3: try AWS API for managed addon default version (best-effort).
	if cfg.EKSConfig != nil && cfg.EKSConfig.ClusterName != "" {
		if apiFindings, defaultVer, apiErr := c.checkViaAWSAPI(ctx, cfg, installedVersion); apiErr == nil {
			meta["default_addon_version"] = defaultVer
			findings = append(findings, apiFindings...)
		}
		// If API call fails, silently skip — static table already covers this.
	}

	return findings, meta, nil
}

func (c *Checker) checkVersionCompat(cfg *checker.CheckConfig, installed string, targetMinor int, meta map[string]string) []checker.Finding {
	minimum, ok := minVersionByK8sMinor[targetMinor]
	if !ok {
		// Unknown target minor — emit info finding.
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityInfo,
			Blocker:     false,
			Title:       fmt.Sprintf("VPC CNI compatibility for k8s v1.%d is unknown", targetMinor),
			Description: fmt.Sprintf("No minimum vpc-cni version is recorded for k8s v1.%d. Installed: %s.", targetMinor, installed),
			Remediation: "Check the EKS documentation for the minimum vpc-cni version for your target.",
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-vpc-cni.html",
		}}
	}

	meta["minimum_required"] = minimum
	cmp := versionCompare(installed, minimum)
	if cmp < 0 {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       fmt.Sprintf("VPC CNI %s is too old for k8s v1.%d (minimum %s)", installed, targetMinor, minimum),
			Description: fmt.Sprintf("aws-node %s does not support k8s v1.%d. After upgrade, the CNI may fail to attach ENIs, causing new pods to stay Pending.", installed, targetMinor),
			Remediation: fmt.Sprintf("Upgrade the vpc-cni managed addon to at least %s before upgrading k8s:\n  eksctl update addon --name vpc-cni --version %s --cluster <name> --force", minimum, minimum),
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-vpc-cni.html",
		}}
	}
	return nil
}

func (c *Checker) checkPrefixDelegation(ctx context.Context, cfg *checker.CheckConfig, installed string) []checker.Finding {
	cm, err := cfg.KubeClient.CoreV1().ConfigMaps("kube-system").Get(ctx, "amazon-vpc-cni", metav1.GetOptions{})
	if err != nil {
		return nil // ConfigMap absent — prefix delegation not configured
	}

	enabled := strings.EqualFold(cm.Data["ENABLE_PREFIX_DELEGATION"], "true")
	if !enabled {
		return nil
	}

	if versionCompare(installed, prefixDelegationMinVersion) < 0 {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       fmt.Sprintf("Prefix delegation enabled but VPC CNI %s is too old (minimum %s)", installed, prefixDelegationMinVersion),
			Description: "ENABLE_PREFIX_DELEGATION=true requires vpc-cni >= 1.11.0. With an older version, prefix delegation causes IP allocation failures — pods may fail to start.",
			Remediation: fmt.Sprintf("Upgrade vpc-cni to at least %s before upgrading k8s.", prefixDelegationMinVersion),
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/cni-prefix-delegation.html",
		}}
	}
	return nil
}

func (c *Checker) checkViaAWSAPI(ctx context.Context, cfg *checker.CheckConfig, installed string) ([]checker.Finding, string, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.EKSConfig.Region))
	if err != nil {
		return nil, "", err
	}
	client := eksv2.NewFromConfig(awsCfg)

	targetStr := fmt.Sprintf("1.%d", parseMinor(cfg.TargetVersion))
	out, err := client.DescribeAddonVersions(ctx, &eksv2.DescribeAddonVersionsInput{
		AddonName:         strPtr("vpc-cni"),
		KubernetesVersion: &targetStr,
	})
	if err != nil || len(out.Addons) == 0 {
		return nil, "", err
	}

	// Find the default version for this k8s minor.
	defaultVersion := ""
	for _, addon := range out.Addons {
		for _, ver := range addon.AddonVersions {
			for _, compat := range ver.Compatibilities {
				if compat.DefaultVersion && ver.AddonVersion != nil {
					defaultVersion = *ver.AddonVersion
					break
				}
			}
			if defaultVersion != "" {
				break
			}
		}
	}

	if defaultVersion == "" {
		return nil, "", nil
	}

	// Strip the "-eksbuild.N" suffix for comparison.
	normalizedDefault := stripEKSBuildSuffix(defaultVersion)
	normalizedInstalled := stripEKSBuildSuffix(installed)

	if versionCompare(normalizedInstalled, normalizedDefault) < 0 {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityMedium,
			Blocker:     false,
			Title:       fmt.Sprintf("VPC CNI %s is behind EKS default %s for k8s v1.%d", installed, defaultVersion, parseMinor(cfg.TargetVersion)),
			Description: "A newer vpc-cni version is available as the EKS managed addon default. While not blocking, upgrading before the k8s upgrade reduces risk.",
			Remediation: fmt.Sprintf("eksctl update addon --name vpc-cni --version %s --cluster %s --force", defaultVersion, cfg.EKSConfig.ClusterName),
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/managing-vpc-cni.html",
		}}, defaultVersion, nil
	}

	return nil, defaultVersion, nil
}

// detectVPCCNIVersion reads the aws-node DaemonSet image tag from kube-system.
func detectVPCCNIVersion(ctx context.Context, cfg *checker.CheckConfig) (string, error) {
	ds, err := cfg.KubeClient.AppsV1().DaemonSets("kube-system").Get(ctx, "aws-node", metav1.GetOptions{})
	if err != nil {
		return "", nil // not found = not installed
	}
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == "aws-node" || strings.Contains(c.Name, "cni") {
			ver := parseImageTag(c.Image)
			if ver != "" {
				return ver, nil
			}
		}
	}
	if len(ds.Spec.Template.Spec.Containers) > 0 {
		return parseImageTag(ds.Spec.Template.Spec.Containers[0].Image), nil
	}
	return "", nil
}

func parseImageTag(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	idx := strings.LastIndex(image, ":")
	if idx < 0 {
		return ""
	}
	tag := image[idx+1:]
	if strings.Contains(tag, "/") {
		return ""
	}
	return strings.TrimPrefix(tag, "v")
}

func stripEKSBuildSuffix(v string) string {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.Index(v, "-eksbuild"); idx >= 0 {
		return v[:idx]
	}
	return v
}

// parseMinor extracts the minor version number from "1.28" or "v1.28.0".
func parseMinor(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(parts[1])
	return n
}

// versionCompare returns negative if a < b, 0 if equal, positive if a > b.
// Handles semver triples like "1.18.1". Non-numeric suffixes are ignored.
func versionCompare(a, b string) int {
	a = stripEKSBuildSuffix(strings.TrimPrefix(a, "v"))
	b = stripEKSBuildSuffix(strings.TrimPrefix(b, "v"))
	ap := versionParts(a)
	bp := versionParts(b)
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av != bv {
			return av - bv
		}
	}
	return 0
}

func versionParts(v string) []int {
	parts := strings.SplitN(v, ".", 3)
	out := make([]int, len(parts))
	for i, p := range parts {
		// strip non-numeric suffix (e.g. "1-eksbuild" → already stripped, but safety)
		for j, ch := range p {
			if ch < '0' || ch > '9' {
				p = p[:j]
				break
			}
		}
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

func strPtr(s string) *string { return &s }
