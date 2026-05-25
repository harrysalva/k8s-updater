// Package helm lists Helm releases and checks whether each chart's declared
// kubeVersion constraint is satisfied by the upgrade target version.
// Outdated charts that declare incompatible constraints are upgrade blockers.
package helm

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	novakube "github.com/fairwindsops/nova/pkg/kube"
	novahelm "github.com/fairwindsops/nova/pkg/helm"

	"upgrade-guardian/internal/checker"
)

const Name = "helm-cves"

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(_ context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	// Inject our existing kubernetes client into Nova's Helm scanner
	// so Nova doesn't spin up a second kubeconfig lookup.
	h := &novahelm.Helm{
		Kube: &novakube.Connection{Client: cfg.KubeClient},
	}

	// List all deployed Helm releases across all namespaces.
	releases, err := h.GetHelmReleases("", nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: list helm releases: %w", Name, err)
	}

	targetSemver, err := semver.NewVersion(normalizeVersion(cfg.TargetVersion))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: parse target version %q: %w", Name, cfg.TargetVersion, err)
	}

	var findings []checker.Finding
	withConstraints, incompatible := 0, 0

	for _, rel := range releases {
		if rel.Chart == nil || rel.Chart.Metadata == nil {
			continue
		}
		chartMeta := rel.Chart.Metadata
		kubeVer := strings.TrimSpace(chartMeta.KubeVersion)

		if kubeVer == "" {
			continue
		}
		withConstraints++

		constraint, err := semver.NewConstraint(kubeVer)
		if err != nil {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityMedium,
				Blocker:     false,
				Title:       fmt.Sprintf("%s/%s: unparseable kubeVersion constraint", rel.Namespace, rel.Name),
				Description: fmt.Sprintf("Chart v%s declares kubeVersion %q which cannot be parsed as a semver constraint.", chartMeta.Version, kubeVer),
				Remediation: "Check chart documentation for supported Kubernetes versions.",
				Resource:    &checker.Resource{Kind: "HelmRelease", Name: rel.Name, Namespace: rel.Namespace},
				Source:      "nova/helm",
			})
			continue
		}

		if !constraint.Check(targetSemver) {
			incompatible++
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("%s/%s v%s: incompatible with k8s %s", rel.Namespace, rel.Name, chartMeta.Version, cfg.TargetVersion),
				Description: fmt.Sprintf(
					"Chart %q v%s requires kubeVersion %q. Kubernetes %s does not satisfy this constraint.",
					chartMeta.Name, chartMeta.Version, kubeVer, cfg.TargetVersion),
				Remediation: fmt.Sprintf(
					"Upgrade the %q chart to a version that supports Kubernetes %s before upgrading the cluster.",
					chartMeta.Name, cfg.TargetVersion),
				Resource: &checker.Resource{Kind: "HelmRelease", Name: rel.Name, Namespace: rel.Namespace},
				Source:   "nova/helm",
			})
		}
	}

	resultMeta := map[string]string{
		"releases_scanned":   strconv.Itoa(len(releases)),
		"with_constraints":   strconv.Itoa(withConstraints),
		"incompatible":       strconv.Itoa(incompatible),
	}
	return findings, resultMeta, nil
}

// normalizeVersion turns "1.35" or "1.35.0" into "1.35.0" (no v prefix — semver library parses both).
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, ".")
}
