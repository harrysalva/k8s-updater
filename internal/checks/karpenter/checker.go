// Package karpenter validates that the installed Karpenter version supports
// the target Kubernetes version. Karpenter has a strict compatibility matrix:
// running an unsupported pair causes the controller to crash-loop or, worse,
// silently mis-schedule nodes after the upgrade.
//
// SOURCE OF TRUTH: https://karpenter.sh/docs/upgrading/compatibility/
// LAST VERIFIED: 2026-05-25 (via WebFetch on karpenter.sh)
//
// The upstream table is structured as "minimum Karpenter version required for
// each Kubernetes minor". We use it to derive the maximum k8s a Karpenter
// version supports (= the k8s where the NEXT Karpenter becomes mandatory − 1).
// The upstream docs do NOT publish a minimum k8s per Karpenter version, so we
// only enforce the upper bound — the lower bound is reported as informational.
//
// To update: re-run WebFetch on the source URL, regenerate the matrix below,
// and bump LAST VERIFIED.
package karpenter

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

const Name = "karpenter-compatibility"

// versionRange is a closed inclusive interval of supported k8s minor versions
// for a Karpenter minor series.
type versionRange struct {
	minMinor, maxMinor int
}

// compatibilityMatrix maps Karpenter "MAJOR.MINOR" → maximum supported k8s minor.
// The minimum is set to the k8s minor at which this Karpenter version became
// the required minimum (i.e., the start of its support window). It is NOT a
// hard lower bound — Karpenter typically supports older k8s too, but upstream
// does not publish a definitive minimum, so we only enforce the upper bound.
//
// Derivation from the upstream table:
//   "For k8s X.Y, Karpenter >= Z is required" means Z supports up to X.Y
//   (until the next Karpenter version takes over for X.Y+1).
var compatibilityMatrix = map[string]versionRange{
	"0.34": {29, 29}, // upstream: required for k8s 1.29
	"0.37": {29, 30}, // upstream: required for k8s 1.30
	"1.0":  {29, 31}, // upstream: 1.0.5+ required for k8s 1.31
	"1.2":  {29, 32}, // upstream: required for k8s 1.32
	"1.5":  {29, 33}, // upstream: required for k8s 1.33
	"1.6":  {29, 34}, // upstream: required for k8s 1.34
	"1.9":  {29, 35}, // upstream: required for k8s 1.35
}

var candidateNamespaces = []string{"karpenter", "kube-system"}

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	installed, ns, version, image, err := detectKarpenter(ctx, cfg)
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
		"namespace":  ns,
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
			Title:       fmt.Sprintf("Karpenter %s is not in the compatibility matrix", version),
			Description: fmt.Sprintf("Could not look up support range for Karpenter series %q. The version may be newer than the bundled matrix.", verSeries),
			Remediation: "Verify the version manually against https://karpenter.sh/docs/upgrading/compatibility/ before upgrading.",
			Source:      "karpenter",
			DocsURL:     "https://karpenter.sh/docs/upgrading/compatibility/",
		})
		meta["supported_range"] = "unknown"
		return findings, meta, nil
	}

	meta["supported_range"] = fmt.Sprintf("v1.%d - v1.%d", rng.minMinor, rng.maxMinor)
	meta["recommended_upgrade"] = recommendedUpgrade(targetMinor)

	switch {
	case targetMinor > rng.maxMinor:
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       fmt.Sprintf("Karpenter %s does not support k8s v1.%d", version, targetMinor),
			Description: fmt.Sprintf("Karpenter %s only supports Kubernetes v1.%d through v1.%d. Upgrading the cluster will leave Karpenter unable to reconcile NodePools, freezing all node-autoscaling activity.", version, rng.minMinor, rng.maxMinor),
			Remediation: fmt.Sprintf("Upgrade Karpenter to at least %s before upgrading the cluster. Crossing v0.x→v1.x requires a migration step — see release notes.", meta["recommended_upgrade"]),
			Source:      "karpenter",
			DocsURL:     "https://karpenter.sh/docs/upgrading/compatibility/",
			Resource: &checker.Resource{
				Kind:      "Deployment",
				Name:      "karpenter",
				Namespace: ns,
				APIGroup:  "apps/v1",
			},
		})
	case targetMinor < rng.minMinor:
		// Upstream does not publish a hard minimum — only the maximum is enforced.
		// Flag as informational; do not block.
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityInfo,
			Blocker:     false,
			Title:       fmt.Sprintf("Karpenter %s is newer than typical for k8s v1.%d", version, targetMinor),
			Description: fmt.Sprintf("Karpenter %s entered the support window at k8s v1.%d (target is v1.%d). The pairing is usually fine but is not formally documented upstream — verify before upgrading.", version, rng.minMinor, targetMinor),
			Remediation: "Test the pairing in a non-production cluster, or check release notes for the installed Karpenter version.",
			Source:      "karpenter",
			DocsURL:     "https://karpenter.sh/docs/upgrading/compatibility/",
		})
	case targetMinor == rng.maxMinor:
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityInfo,
			Blocker:     false,
			Title:       fmt.Sprintf("Karpenter %s is at end of supported range for v1.%d", version, targetMinor),
			Description: fmt.Sprintf("Karpenter %s officially supports up to k8s v1.%d. This upgrade is OK, but plan a Karpenter upgrade before the next Kubernetes minor.", version, rng.maxMinor),
			Remediation: fmt.Sprintf("After this upgrade, schedule a Karpenter upgrade to %s to keep support headroom.", meta["recommended_upgrade"]),
			Source:      "karpenter",
			DocsURL:     "https://karpenter.sh/docs/upgrading/compatibility/",
		})
	}

	return findings, meta, nil
}

// detectKarpenter searches conventional namespaces for the karpenter Deployment.
func detectKarpenter(ctx context.Context, cfg *checker.CheckConfig) (installed bool, ns, version, image string, err error) {
	for _, n := range candidateNamespaces {
		dep, getErr := cfg.KubeClient.AppsV1().Deployments(n).Get(ctx, "karpenter", metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			continue
		}
		if getErr != nil {
			return false, "", "", "", getErr
		}
		img, ver := extractControllerImage(dep.Spec.Template.Spec.Containers)
		return true, n, ver, img, nil
	}
	return false, "", "", "", nil
}

// extractControllerImage finds the karpenter controller container and returns its image+tag.
func extractControllerImage(containers []corev1.Container) (image, version string) {
	for _, c := range containers {
		name := strings.ToLower(c.Name)
		if name == "controller" || strings.Contains(name, "karpenter") {
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

// parseImageTag returns the tag from "registry/image:tag" — empty if no tag.
// Strips a leading "v" and strips any "@sha256:..." digest suffix.
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
	// Reject port-style colons in registry, e.g., "registry:5000/img" with no tag.
	if strings.Contains(tag, "/") {
		return ""
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

// recommendedUpgrade returns the lowest Karpenter version that covers targetMinor.
func recommendedUpgrade(targetMinor int) string {
	type pair struct {
		series string
		rng    versionRange
	}
	all := make([]pair, 0, len(compatibilityMatrix))
	for s, r := range compatibilityMatrix {
		all = append(all, pair{s, r})
	}
	sort.Slice(all, func(i, j int) bool { return seriesLess(all[i].series, all[j].series) })
	for _, p := range all {
		if targetMinor >= p.rng.minMinor && targetMinor <= p.rng.maxMinor {
			return "v" + p.series + ".0+"
		}
	}
	return "latest"
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
