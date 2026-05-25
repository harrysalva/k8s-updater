// Package istio validates that the installed Istio version supports the target
// Kubernetes version. Mismatches cause sidecar injection failures, control-plane
// crashes, and webhook validation timeouts during/after upgrade.
//
// SOURCE OF TRUTH: https://istio.io/latest/docs/releases/supported-releases/
// LAST VERIFIED: 2026-05-25 (via WebFetch on istio.io)
//
// Istio publishes both:
//   1. A k8s compatibility range per Istio minor (clear table)
//   2. A "Support Status" telling which minors are still patched upstream
//
// To update: re-run WebFetch on the source URL, regenerate the matrix and
// supportedReleases map, and bump LAST VERIFIED.
package istio

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "istio-compatibility"

type versionRange struct {
	minMinor, maxMinor int
}

// compatibilityMatrix maps Istio "MAJOR.MINOR" → supported k8s minor range.
// Verified from upstream "Supported Kubernetes Releases" table.
var compatibilityMatrix = map[string]versionRange{
	"1.27": {24, 33},
	"1.28": {25, 34},
	"1.29": {26, 35},
	"1.30": {27, 36},
}

const istioNamespace = "istio-system"

// supportedReleases marks Istio versions that are still under upstream support.
// Anything older receives no security patches. Sourced from the upstream
// "Support Status of Istio Releases" table.
var supportedReleases = map[string]bool{
	"1.27": true, // EOL: April 7, 2026 — flag as expiring soon if today is past then
	"1.28": true,
	"1.29": true,
	"1.30": true,
}

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	installed, version, image, err := detectIstio(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", Name, err)
	}
	if !installed {
		return nil, map[string]string{"installed": "false"}, nil
	}

	targetMinor := parseMinor(cfg.TargetVersion)
	verSeries := majorMinor(version)
	rng, known := compatibilityMatrix[verSeries]

	meta := map[string]string{
		"installed":  "true",
		"namespace":  istioNamespace,
		"version":    version,
		"image":      image,
		"target_k8s": fmt.Sprintf("v1.%d", targetMinor),
	}

	var findings []checker.Finding

	if !known {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityMedium,
			Blocker:     false,
			Title:       fmt.Sprintf("Istio %s is not in the compatibility matrix", version),
			Description: fmt.Sprintf("Could not look up support range for Istio series %q. The version may be newer than the bundled matrix.", verSeries),
			Remediation: "Verify the version manually against https://istio.io/latest/docs/releases/supported-releases/ before upgrading.",
			Source:      "istio",
			DocsURL:     "https://istio.io/latest/docs/releases/supported-releases/",
		})
		meta["supported_range"] = "unknown"
		return findings, meta, nil
	}

	meta["supported_range"] = fmt.Sprintf("v1.%d - v1.%d", rng.minMinor, rng.maxMinor)
	meta["recommended_upgrade"] = recommendedUpgrade(targetMinor)
	meta["upstream_supported"] = strconv.FormatBool(supportedReleases[verSeries])

	switch {
	case targetMinor > rng.maxMinor:
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       fmt.Sprintf("Istio %s does not support k8s v1.%d", version, targetMinor),
			Description: fmt.Sprintf("Istio %s only supports Kubernetes v1.%d through v1.%d. Upgrading the cluster will cause istiod to fail; sidecar injection breaks, validating webhooks reject new configs, and existing mesh traffic continues only until pods restart.", version, rng.minMinor, rng.maxMinor),
			Remediation: fmt.Sprintf("Upgrade Istio to %s before upgrading the cluster. Use the istioctl canary install + traffic shift pattern to minimize downtime.", meta["recommended_upgrade"]),
			Source:      "istio",
			DocsURL:     "https://istio.io/latest/docs/setup/upgrade/",
			Resource: &checker.Resource{
				Kind:      "Deployment",
				Name:      "istiod",
				Namespace: istioNamespace,
				APIGroup:  "apps/v1",
			},
		})
	case targetMinor < rng.minMinor:
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityHigh,
			Blocker:     true,
			Title:       fmt.Sprintf("Istio %s requires k8s ≥ v1.%d (target is v1.%d)", version, rng.minMinor, targetMinor),
			Description: fmt.Sprintf("Istio %s requires at least Kubernetes v1.%d. The target is too old.", version, rng.minMinor),
			Remediation: "Either pick a higher target Kubernetes version or downgrade Istio.",
			Source:      "istio",
			DocsURL:     "https://istio.io/latest/docs/releases/supported-releases/",
		})
	case targetMinor == rng.maxMinor:
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityInfo,
			Blocker:     false,
			Title:       fmt.Sprintf("Istio %s is at end of supported range for v1.%d", version, targetMinor),
			Description: fmt.Sprintf("Istio %s officially supports up to k8s v1.%d. This upgrade is OK but the next Kubernetes minor will break the mesh.", version, rng.maxMinor),
			Remediation: fmt.Sprintf("After this upgrade, schedule an Istio upgrade to %s before the next Kubernetes minor bump.", meta["recommended_upgrade"]),
			Source:      "istio",
			DocsURL:     "https://istio.io/latest/docs/setup/upgrade/",
		})
	}

	// Independent of k8s compat — flag if the installed Istio is out of upstream support.
	if !supportedReleases[verSeries] {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityHigh,
			Blocker:     false,
			Title:       fmt.Sprintf("Istio %s is no longer supported upstream", version),
			Description: "This Istio version no longer receives security patches. CVEs that affect the mesh control plane will not be fixed in this branch.",
			Remediation: "Plan an upgrade to a supported Istio minor (currently 1.24+). Use canary upgrade with revision tags.",
			Source:      "istio",
			DocsURL:     "https://istio.io/latest/docs/releases/supported-releases/",
		})
	}

	return findings, meta, nil
}

// detectIstio looks for the istiod Deployment in istio-system.
func detectIstio(ctx context.Context, cfg *checker.CheckConfig) (installed bool, version, image string, err error) {
	dep, getErr := cfg.KubeClient.AppsV1().Deployments(istioNamespace).Get(ctx, "istiod", metav1.GetOptions{})
	if apierrors.IsNotFound(getErr) {
		return false, "", "", nil
	}
	if getErr != nil {
		return false, "", "", getErr
	}
	img, ver := extractIstiodImage(dep.Spec.Template.Spec.Containers)
	return true, ver, img, nil
}

func extractIstiodImage(containers []corev1.Container) (image, version string) {
	for _, c := range containers {
		// istiod container is typically named "discovery"; image contains "pilot" or "istiod".
		if strings.ToLower(c.Name) == "discovery" ||
			strings.Contains(c.Image, "pilot") ||
			strings.Contains(c.Image, "istiod") {
			image = c.Image
			version = parseImageTag(c.Image)
			if version != "" {
				return
			}
		}
	}
	if len(containers) > 0 && image == "" {
		image = containers[0].Image
		version = parseImageTag(containers[0].Image)
	}
	return
}

func parseImageTag(image string) string {
	// Strip digest first so "@sha256:" colons don't confuse LastIndex.
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
	// Istio sometimes tags as "1.24-distroless" — strip the suffix.
	if hyphen := strings.Index(tag, "-"); hyphen >= 0 {
		tag = tag[:hyphen]
	}
	return strings.TrimPrefix(tag, "v")
}

func majorMinor(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

func parseMinor(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(parts[1])
	return n
}

func recommendedUpgrade(targetMinor int) string {
	type pair struct {
		series string
		rng    versionRange
	}
	all := make([]pair, 0, len(compatibilityMatrix))
	for s, r := range compatibilityMatrix {
		// Only recommend upstream-supported series.
		if supportedReleases[s] {
			all = append(all, pair{s, r})
		}
	}
	sort.Slice(all, func(i, j int) bool { return seriesLess(all[i].series, all[j].series) })
	for _, p := range all {
		if targetMinor >= p.rng.minMinor && targetMinor <= p.rng.maxMinor {
			return "v" + p.series + ".0+"
		}
	}
	return "latest supported"
}

func seriesLess(a, b string) bool {
	ax := strings.Split(a, ".")
	bx := strings.Split(b, ".")
	for i := 0; i < len(ax) && i < len(bx); i++ {
		ai, _ := strconv.Atoi(ax[i])
		bi, _ := strconv.Atoi(bx[i])
		if ai != bi {
			return ai < bi
		}
	}
	return false
}
