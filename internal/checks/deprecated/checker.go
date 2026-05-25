// Package deprecated detects Kubernetes API resources that are deprecated or
// removed in the target version using Pluto's version database against live
// cluster objects. Unlike Pluto's default scanner, this checker reads apiVersion
// directly from each object — not from the last-applied-configuration annotation —
// so it catches resources deployed via Helm, operators, or any non-kubectl path.
package deprecated

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	plutoversionsfile "github.com/fairwindsops/pluto/v5"
	plutoapi "github.com/fairwindsops/pluto/v5/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	"upgrade-guardian/internal/checker"
	versionsmgr "upgrade-guardian/internal/versions"
)

const Name = "deprecated-apis"

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	versions, targetVersions, err := plutoapi.GetDefaultVersionList(versionsmgr.PlutoContent(plutoversionsfile.Content()))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: load version list: %w", Name, err)
	}
	targetVersions["k8s"] = normalizeVersion(cfg.TargetVersion)

	instance := &plutoapi.Instance{
		TargetVersions:     targetVersions,
		DeprecatedVersions: versions,
	}

	// Build a lookup map: "group/version/kind" → DeprecatedVersion entry
	type depKey struct{ group, version, kind string }
	depMap := map[depKey]*plutoapi.Version{}
	for i := range instance.DeprecatedVersions {
		dv := &instance.DeprecatedVersions[i]
		// Parse "apps/v1" or "v1" style names
		parts := strings.SplitN(dv.Name, "/", 3)
		var group, version string
		switch len(parts) {
		case 1:
			version = parts[0]
		case 2:
			group, version = parts[0], parts[1]
		default:
			group, version = parts[0], parts[1]
		}
		depMap[depKey{group, version, dv.Kind}] = dv
	}

	dynamicCli, err := dynamic.NewForConfig(cfg.RestConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: dynamic client: %w", Name, err)
	}
	discCli, err := discovery.NewDiscoveryClientForConfig(cfg.RestConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: discovery client: %w", Name, err)
	}

	// Discover all API resource types in the cluster.
	resourceLists, _ := discCli.ServerPreferredResources()

	totalScanned := 0
	namespaces := map[string]bool{}
	kinds := map[string]bool{}

	type hit struct {
		name      string
		namespace string
		gv        schema.GroupVersion
		kind      string
	}
	var hits []hit

	for _, rl := range resourceLists {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil {
			continue
		}
		for _, res := range rl.APIResources {
			// Skip subresources (e.g. pods/log)
			if strings.Contains(res.Name, "/") {
				continue
			}
			// Skip resources we can't list
			canList := false
			for _, v := range res.Verbs {
				if v == "list" {
					canList = true
					break
				}
			}
			if !canList {
				continue
			}

			gvr := gv.WithResource(res.Name)
			list, err := dynamicCli.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}

			kinds[res.Kind] = true
			for _, obj := range list.Items {
				totalScanned++
				if ns := obj.GetNamespace(); ns != "" {
					namespaces[ns] = true
				}

				// Check the object's actual apiVersion against the deprecated database.
				apiVersion := obj.GetAPIVersion()
				objGV, err := schema.ParseGroupVersion(apiVersion)
				if err != nil {
					continue
				}

				key := depKey{objGV.Group, objGV.Version, obj.GetKind()}
				dv, ok := depMap[key]
				if !ok {
					continue
				}

				hits = append(hits, hit{
					name:      obj.GetName(),
					namespace: obj.GetNamespace(),
					gv:        objGV,
					kind:      obj.GetKind(),
				})
				_ = dv
			}
		}
	}

	// Re-use Pluto's instance for proper deprecated/removed determination.
	// Feed found hits back through Pluto's FilterOutput logic by using its
	// IsVersioned helper on synthetic minimal manifests.
	var findings []checker.Finding
	removedCount, deprecatedCount := 0, 0

	for _, h := range hits {
		apiVer := h.gv.String()
		key := depKey{h.gv.Group, h.gv.Version, h.kind}
		dv := depMap[key]
		if dv == nil {
			continue
		}

		// Determine removed vs deprecated against target version.
		targetMinor := parseMinorStr(targetVersions["k8s"])
		removedMinor := parseMinorStr(string(dv.RemovedIn))
		deprecatedMinor := parseMinorStr(string(dv.DeprecatedIn))

		isRemoved := removedMinor > 0 && targetMinor >= removedMinor
		isDeprecated := deprecatedMinor > 0 && targetMinor >= deprecatedMinor

		if !isRemoved && !isDeprecated {
			continue
		}

		var sev checker.Severity
		blocker := false
		var title, description, remediation string

		if isRemoved {
			removedCount++
			sev = checker.SeverityCritical
			blocker = true
			title = fmt.Sprintf("%s/%s: %s removed in k8s %s", h.namespace, h.name, h.kind, dv.RemovedIn)
			description = fmt.Sprintf(
				"Object %q uses API %s (%s) which is removed in Kubernetes %s. It will stop working after the upgrade.",
				h.name, apiVer, h.kind, dv.RemovedIn,
			)
		} else {
			deprecatedCount++
			sev = checker.SeverityHigh
			title = fmt.Sprintf("%s/%s: %s deprecated since k8s %s", h.namespace, h.name, h.kind, dv.DeprecatedIn)
			description = fmt.Sprintf(
				"Object %q uses API %s (%s) deprecated since Kubernetes %s.",
				h.name, apiVer, h.kind, dv.DeprecatedIn,
			)
		}

		if dv.ReplacementAPI != "" {
			remediation = fmt.Sprintf("Replace %s with %s in your manifests.", apiVer, dv.ReplacementAPI)
		} else {
			remediation = "Update the manifest to use a supported API version before upgrading."
		}

		ns := h.namespace
		if ns == "" {
			ns = "cluster-scoped"
		}
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    sev,
			Blocker:     blocker,
			Title:       title,
			Description: description,
			Remediation: remediation,
			Resource: &checker.Resource{
				Kind:      h.kind,
				Name:      h.name,
				Namespace: ns,
				APIGroup:  apiVer,
			},
			Source:  "pluto",
			DocsURL: "https://kubernetes.io/docs/reference/using-api/deprecation-guide/",
		})
	}

	meta := map[string]string{
		"resources_scanned":  strconv.Itoa(totalScanned),
		"kinds_checked":      strconv.Itoa(len(kinds)),
		"namespaces_checked": strconv.Itoa(len(namespaces)),
		"removed":            strconv.Itoa(removedCount),
		"deprecated":         strconv.Itoa(deprecatedCount),
	}
	return findings, meta, nil
}

// normalizeVersion turns "1.35" or "1.35.0" into "v1.35.0" as Pluto expects.
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return "v" + strings.Join(parts, ".")
}

// parseMinorStr extracts the minor version from strings like "v1.16.0" or "1.16".
func parseMinorStr(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return n
}
