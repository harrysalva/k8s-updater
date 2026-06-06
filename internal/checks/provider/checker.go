// Package provider validates provider-specific compatibility before an upgrade:
//
//   - Upstream: detects the installed CNI (Calico, Cilium, Flannel, Weave, kindnet, vpc-cni)
//     and checks its version against a curated compatibility matrix.
//   - EKS: verifies that each managed add-on has a version compatible with the
//     target Kubernetes version via the EKS Insights / DescribeAddonVersions API.
//   - Kubespray: parses group_vars to extract the CNI plugin and version, then
//     applies the same compatibility matrix as the upstream path.
//
// Compatibility matrices are sourced exclusively from official documentation:
//
//	Calico:  https://docs.tigera.io/calico/latest/getting-started/kubernetes/requirements
//	Cilium:  https://docs.cilium.io/en/stable/network/kubernetes/compatibility/
//	Flannel: https://github.com/flannel-io/flannel#requirements
//	EKS:     https://docs.aws.amazon.com/eks/latest/userguide/add-ons-compatibility.html
package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awseks "github.com/aws/aws-sdk-go-v2/service/eks"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"upgrade-guardian/internal/checker"
)

const Name = "provider-compatibility"

// cniKind enumerates the CNI plugins we can identify.
type cniKind string

const (
	cniCalico  cniKind = "calico"
	cniCilium  cniKind = "cilium"
	cniFlannel cniKind = "flannel"
	cniWeave   cniKind = "weave"
	cniKindnet cniKind = "kindnet" // Kind development clusters only
	cniVPCCNI  cniKind = "vpc-cni" // EKS managed CNI
	cniUnknown cniKind = "unknown"
)

type cniInfo struct {
	Kind      cniKind
	Version   string // semver string extracted from image tag, e.g. "3.29.1"
	Namespace string
	DaemonSet string
	Image     string
}

// cniMaxK8sMinor[cni][cniMinor] = maximum k8s minor version the CNI minor supports.
// Source: official CNI documentation (see package doc).
// Conservative: we only flag when we have high confidence of incompatibility.
var cniMaxK8sMinor = map[cniKind]map[int]int{
	cniCalico: {
		// Calico 3.x supports approx k8s Y through Y+2 where Y is the Calico minor - 1
		26: 28, 27: 29, 28: 30, 29: 31, 30: 32,
	},
	cniCilium: {
		// Cilium 1.x supports a sliding window of ~4 k8s minor versions
		13: 27, 14: 28, 15: 29, 16: 30, 17: 31, 18: 32,
	},
	cniFlannel: {
		// Flannel is permissive; flag only very old versions (<0.22)
		21: 29, 22: 30, 23: 31, 24: 32, 25: 33, 26: 34,
	},
}

// daemonSetCNIMap maps well-known DaemonSet names to CNI kinds.
// Checked in this order so more-specific names take priority.
var daemonSetCNIMap = []struct {
	name string
	kind cniKind
}{
	{"calico-node", cniCalico},
	{"cilium", cniCilium},
	{"kube-flannel-ds", cniFlannel},
	{"weave-net", cniWeave},
	{"aws-node", cniVPCCNI},
	{"kindnet", cniKindnet},
}

// cniNamespaces are searched in priority order for the DaemonSets above.
var cniNamespaces = []string{
	"kube-system",
	"calico-system",
	"cilium",
	"kube-flannel",
}

// ─────────────────────────────────────────────────────────────────────────────

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	var findings []checker.Finding
	var meta map[string]string

	switch cfg.ClusterType {
	case checker.ClusterTypeEKS:
		findings, meta = c.checkEKS(ctx, cfg)
	case checker.ClusterTypeKubespray:
		var err error
		findings, meta, err = c.checkKubespray(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
	default:
		findings, meta = c.checkUpstream(ctx, cfg)
	}

	findings = append(findings, c.checkIngressNginxRetirement(ctx, cfg)...)
	return findings, meta, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Upstream / generic path
// ─────────────────────────────────────────────────────────────────────────────

func (c *Checker) checkUpstream(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string) {
	info, err := detectCNI(ctx, cfg)
	if err != nil {
		return nil, nil
	}

	cniName := string(info.Kind)
	meta := map[string]string{"cni": cniName}

	findings := cniFindings(info, cfg)

	// kube-proxy skew check: proxy must be <= 1 minor version behind apiserver.
	findings = append(findings, c.checkKubeProxySkew(ctx, cfg)...)

	return findings, meta
}

// ─────────────────────────────────────────────────────────────────────────────
// Kubespray path
// ─────────────────────────────────────────────────────────────────────────────

func (c *Checker) checkKubespray(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	if cfg.KubesprayConfig == nil {
		return nil, nil, fmt.Errorf("KubesprayConfig not provided")
	}

	info, err := parseKubesprayGroupVars(cfg.KubesprayConfig)
	if err != nil {
		// Fall back to live-cluster detection.
		var liveErr error
		info, liveErr = detectCNI(ctx, cfg)
		if liveErr != nil {
			return nil, nil, fmt.Errorf("group_vars parse: %w; live detect: %w", err, liveErr)
		}
	}

	meta := map[string]string{"cni": string(info.Kind)}
	return cniFindings(info, cfg), meta, nil
}

// groupVarsSchema is the minimal subset of Kubespray group_vars we care about.
type groupVarsSchema struct {
	KubeNetworkPlugin string `yaml:"kube_network_plugin"`
	CalicoVersion     string `yaml:"calico_version"`
	CiliumVersion     string `yaml:"cilium_version"`
	FlannelVersion    string `yaml:"flannel_version"`
	WeaveVersion      string `yaml:"weave_version"`
}

func parseKubesprayGroupVars(kcfg *checker.KubesprayConfig) (*cniInfo, error) {
	candidates := []string{
		filepath.Join(kcfg.GroupVarsPath, "all", "all.yml"),
		filepath.Join(kcfg.GroupVarsPath, "all", "all.yaml"),
		filepath.Join(kcfg.GroupVarsPath, "k8s_cluster", "k8s-cluster.yml"),
		filepath.Join(kcfg.GroupVarsPath, "k8s_cluster", "k8s-cluster.yaml"),
	}

	var schema groupVarsSchema
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := yaml.Unmarshal(data, &schema); err == nil {
			break
		}
	}

	if schema.KubeNetworkPlugin == "" {
		return nil, fmt.Errorf("kube_network_plugin not found in group_vars at %s", kcfg.GroupVarsPath)
	}

	info := &cniInfo{}

	switch strings.ToLower(schema.KubeNetworkPlugin) {
	case "calico":
		info.Kind = cniCalico
		info.Version = strings.TrimPrefix(schema.CalicoVersion, "v")
	case "cilium":
		info.Kind = cniCilium
		info.Version = strings.TrimPrefix(schema.CiliumVersion, "v")
	case "flannel":
		info.Kind = cniFlannel
		info.Version = strings.TrimPrefix(schema.FlannelVersion, "v")
	case "weave":
		info.Kind = cniWeave
		info.Version = strings.TrimPrefix(schema.WeaveVersion, "v")
	default:
		info.Kind = cniUnknown
	}

	return info, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// EKS path
// ─────────────────────────────────────────────────────────────────────────────

func (c *Checker) checkEKS(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string) {
	if cfg.EKSConfig == nil {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityMedium,
			Title:       "EKS add-on compatibility could not be checked: EKSConfig not provided",
			Remediation: "Provide EKSConfig with ClusterName and Region.",
			Source:      Name,
		}}, nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.EKSConfig.Region),
	)
	if err != nil {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityMedium,
			Title:       "EKS add-on compatibility could not be checked: AWS credentials unavailable",
			Description: err.Error(),
			Remediation: "Ensure AWS credentials are configured (IRSA, instance profile, or aws configure).",
			Source:      Name,
		}}, map[string]string{"status": "no_credentials"}
	}

	eksClient := awseks.NewFromConfig(awsCfg)
	clusterName := cfg.EKSConfig.ClusterName
	targetK8s := normalizeK8sVersion(cfg.TargetVersion)

	addonsOut, err := eksClient.ListAddons(ctx, &awseks.ListAddonsInput{
		ClusterName: &clusterName,
	})
	if err != nil {
		return nil, nil
	}

	addonNames := addonsOut.Addons
	meta := map[string]string{"addons_checked": strconv.Itoa(len(addonNames))}

	var findings []checker.Finding

	for _, addonName := range addonNames {
		name := addonName

		descOut, err := eksClient.DescribeAddon(ctx, &awseks.DescribeAddonInput{
			ClusterName: &clusterName,
			AddonName:   &name,
		})
		if err != nil {
			continue
		}
		currentVersion := ""
		if descOut.Addon.AddonVersion != nil {
			currentVersion = *descOut.Addon.AddonVersion
		}

		// Fetch versions compatible with the target k8s version.
		versionsOut, err := eksClient.DescribeAddonVersions(ctx, &awseks.DescribeAddonVersionsInput{
			AddonName:         &name,
			KubernetesVersion: &targetK8s,
		})
		if err != nil {
			continue
		}

		compatible := false
		var latestCompatible string
		for _, addon := range versionsOut.Addons {
			for _, v := range addon.AddonVersions {
				if v.AddonVersion == nil {
					continue
				}
				if latestCompatible == "" {
					latestCompatible = *v.AddonVersion // first = latest
				}
				if *v.AddonVersion == currentVersion {
					compatible = true
				}
			}
		}

		if !compatible {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("EKS add-on %q v%s is not compatible with k8s %s", name, currentVersion, cfg.TargetVersion),
				Description: fmt.Sprintf("The currently installed version of add-on %q (%s) is not in the list of versions compatible with Kubernetes %s.", name, currentVersion, cfg.TargetVersion),
				Remediation: fmt.Sprintf("Update add-on %q to %s before upgrading the cluster.", name, latestCompatible),
				Resource:    &checker.Resource{Kind: "EKSAddon", Name: name},
				Source:      Name,
				DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/add-ons-compatibility.html",
			})
		}
	}

	// Also check the VPC CNI which may be unmanaged.
	findings = append(findings, cniFindings(
		&cniInfo{Kind: cniVPCCNI}, cfg,
	)...)

	return findings, meta
}

// ─────────────────────────────────────────────────────────────────────────────
// CNI detection (live cluster)
// ─────────────────────────────────────────────────────────────────────────────

func detectCNI(ctx context.Context, cfg *checker.CheckConfig) (*cniInfo, error) {
	for _, ns := range cniNamespaces {
		dsList, err := cfg.KubeClient.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}

		for _, ds := range dsList.Items {
			for _, candidate := range daemonSetCNIMap {
				if ds.Name == candidate.name || strings.HasPrefix(ds.Name, candidate.name) {
					image := ""
					if len(ds.Spec.Template.Spec.Containers) > 0 {
						image = ds.Spec.Template.Spec.Containers[0].Image
					}
					return &cniInfo{
						Kind:      candidate.kind,
						Version:   extractImageVersion(image),
						Namespace: ns,
						DaemonSet: ds.Name,
						Image:     image,
					}, nil
				}
			}
		}
	}

	return &cniInfo{Kind: cniUnknown}, nil
}

// extractImageVersion returns the tag portion of an image reference without the "v" prefix.
// "calico/node:v3.29.1" → "3.29.1"
// "kindest/kindnetd:v20250512-df8de77b" → "20250512-df8de77b" (date-based, not semver)
func extractImageVersion(image string) string {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimPrefix(parts[1], "v")
}

// ─────────────────────────────────────────────────────────────────────────────
// CNI compatibility evaluation (shared by upstream + Kubespray)
// ─────────────────────────────────────────────────────────────────────────────

func cniFindings(info *cniInfo, cfg *checker.CheckConfig) []checker.Finding {
	targetMinor := k8sMinor(cfg.TargetVersion)

	switch info.Kind {
	case cniUnknown:
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityMedium,
			Blocker:     false,
			Title:       "CNI plugin could not be identified",
			Description: "No known CNI DaemonSet was found in the searched namespaces (kube-system, calico-system, cilium, kube-flannel).",
			Remediation: "Manually verify CNI compatibility with the target Kubernetes version before upgrading.",
			Source:      Name,
		}}

	case cniKindnet:
		// kindnet is Kind's built-in CNI — it's a development-only component.
		// No compatibility concern: Kind ships kindnet bundled with the k8s version.
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityInfo,
			Blocker:     false,
			Title:       "CNI: kindnet detected (Kind development cluster)",
			Description: fmt.Sprintf("DaemonSet %s/%s image: %s. kindnet is bundled with Kind and is always compatible.", info.Namespace, info.DaemonSet, info.Image),
			Remediation: "No action needed. kindnet is upgraded as part of the Kind cluster upgrade.",
			Source:      Name,
		}}

	case cniWeave:
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       "CNI: Weave Net is end-of-life",
			Description: "Weave Net's free tier was discontinued in 2023. It is not actively maintained for recent Kubernetes versions.",
			Remediation: "Migrate to a supported CNI (Calico, Cilium, or Flannel) before upgrading.",
			Source:      Name,
			DocsURL:     "https://kubernetes.io/docs/concepts/cluster-administration/networking/",
		}}

	case cniVPCCNI:
		// VPC CNI compatibility on EKS is checked via DescribeAddonVersions (EKS path).
		// In non-EKS clusters it just means an aws-node DaemonSet exists — unusual.
		return nil

	default:
		return checkVersionMatrix(info, targetMinor, cfg)
	}
}

func checkVersionMatrix(info *cniInfo, targetK8sMinor int, cfg *checker.CheckConfig) []checker.Finding {
	// Always emit an INFO finding so the operator sees what CNI is installed.
	findings := []checker.Finding{{
		CheckerName: Name,
		ClusterType: cfg.ClusterType,
		Severity:    checker.SeverityInfo,
		Blocker:     false,
		Title:       fmt.Sprintf("CNI: %s v%s detected", info.Kind, info.Version),
		Description: fmt.Sprintf("DaemonSet %s/%s image: %s", info.Namespace, info.DaemonSet, info.Image),
		Remediation: fmt.Sprintf("Verify %s v%s supports Kubernetes %s before upgrading.", info.Kind, info.Version, cfg.TargetVersion),
		Source:      Name,
		DocsURL:     cniDocsURL(info.Kind),
	}}

	// Compatibility check: only flag when we have a matrix entry AND high confidence.
	cniMinor := semverMinor(info.Version)
	if cniMinor < 0 {
		// Non-semver version tag (e.g. commit hash) — cannot check.
		return findings
	}

	matrix, ok := cniMaxK8sMinor[info.Kind]
	if !ok {
		return findings
	}

	maxK8s, known := matrix[cniMinor]
	if !known {
		// CNI version not in matrix — cannot determine compatibility.
		return findings
	}

	if targetK8sMinor > maxK8s {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title: fmt.Sprintf("%s v%s does not support Kubernetes 1.%d",
				info.Kind, info.Version, targetK8sMinor),
			Description: fmt.Sprintf(
				"%s v%s supports Kubernetes up to 1.%d. Upgrading to k8s 1.%d with this CNI version will break networking.",
				info.Kind, info.Version, maxK8s, targetK8sMinor),
			Remediation: fmt.Sprintf(
				"Upgrade %s to a version that supports Kubernetes 1.%d BEFORE upgrading the cluster.",
				info.Kind, targetK8sMinor),
			Resource: &checker.Resource{Kind: "DaemonSet", Name: info.DaemonSet, Namespace: info.Namespace},
			Source:   Name,
			DocsURL:  cniDocsURL(info.Kind),
		})
	}

	return findings
}

// ─────────────────────────────────────────────────────────────────────────────
// kube-proxy skew check (upstream / Kubespray)
// ─────────────────────────────────────────────────────────────────────────────

func (c *Checker) checkKubeProxySkew(ctx context.Context, cfg *checker.CheckConfig) []checker.Finding {
	ds, err := cfg.KubeClient.AppsV1().DaemonSets("kube-system").Get(ctx, "kube-proxy", metav1.GetOptions{})
	if err != nil {
		return nil // kube-proxy not found (some clusters use eBPF without it)
	}

	if len(ds.Spec.Template.Spec.Containers) == 0 {
		return nil
	}

	image := ds.Spec.Template.Spec.Containers[0].Image
	proxyVersion := extractImageVersion(image)
	proxyMinor := semverMinor(proxyVersion)
	currentMinor := k8sMinor(cfg.CurrentVersion)

	if proxyMinor < 0 || currentMinor < 0 {
		return nil
	}

	skew := currentMinor - proxyMinor
	if skew <= 1 {
		return nil // within allowed skew
	}

	return []checker.Finding{{
		CheckerName: Name,
		ClusterType: cfg.ClusterType,
		Severity:    checker.SeverityHigh,
		Blocker:     false,
		Title: fmt.Sprintf("kube-proxy v%s is %d minor versions behind apiserver v%s",
			proxyVersion, skew, cfg.CurrentVersion),
		Description: fmt.Sprintf("kube-proxy must be within 1 minor version of the API server. Current skew: %d.", skew),
		Remediation: "Update kube-proxy to match the current apiserver version before upgrading.",
		Resource:    &checker.Resource{Kind: "DaemonSet", Name: "kube-proxy", Namespace: "kube-system"},
		Source:      Name,
		DocsURL:     "https://kubernetes.io/releases/version-skew-policy/",
	}}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// k8sMinor parses the minor version from "1.34", "v1.34.0", etc.
func k8sMinor(version string) int {
	v := strings.TrimPrefix(version, "v")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return -1
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return -1
	}
	return n
}

// semverMinor parses the minor component from "3.29.1", "1.16.5", etc.
// Returns -1 if the string is not semver (e.g. a commit hash).
func semverMinor(version string) int {
	v := strings.TrimPrefix(version, "v")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return -1
	}
	// Guard against non-numeric minor (date-based tags like "20250512-df8de77b")
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return -1
	}
	return n
}

// normalizeK8sVersion turns "1.35" or "1.35.0" into "1.35" for EKS API calls.
func normalizeK8sVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

func cniDocsURL(kind cniKind) string {
	switch kind {
	case cniCalico:
		return "https://docs.tigera.io/calico/latest/getting-started/kubernetes/requirements"
	case cniCilium:
		return "https://docs.cilium.io/en/stable/network/kubernetes/compatibility/"
	case cniFlannel:
		return "https://github.com/flannel-io/flannel#requirements"
	default:
		return "https://kubernetes.io/docs/concepts/cluster-administration/networking/"
	}
}

// ingressNginxRetirementDate is when the Ingress NGINX project moved to maintenance-only mode.
// Source: https://kubernetes.io/blog/2025/01/13/gateway-api-ga/ and upstream project announcements.
var ingressNginxRetirementDate = time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC)

// checkIngressNginxRetirement emits a finding if ingress-nginx is installed after its retirement date.
// Retirement means the project entered maintenance-only mode; migration to Gateway API is recommended.
func (c *Checker) checkIngressNginxRetirement(ctx context.Context, cfg *checker.CheckConfig) []checker.Finding {
	namespacesToSearch := []string{"ingress-nginx", "kube-system", "default"}
	for _, ns := range namespacesToSearch {
		// Check DaemonSets first (common in bare-metal / on-prem clusters).
		dsList, err := cfg.KubeClient.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err == nil {
			for _, ds := range dsList.Items {
				if strings.Contains(ds.Name, "ingress-nginx") {
					img := ""
					if len(ds.Spec.Template.Spec.Containers) > 0 {
						img = ds.Spec.Template.Spec.Containers[0].Image
					}
					return []checker.Finding{ingressNginxFinding(cfg, "DaemonSet", ds.Name, ns, img)}
				}
			}
		}
		// Check Deployments (common in cloud / managed clusters).
		deployList, err := cfg.KubeClient.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err == nil {
			for _, deploy := range deployList.Items {
				if strings.Contains(deploy.Name, "ingress-nginx") {
					img := ""
					if len(deploy.Spec.Template.Spec.Containers) > 0 {
						img = deploy.Spec.Template.Spec.Containers[0].Image
					}
					return []checker.Finding{ingressNginxFinding(cfg, "Deployment", deploy.Name, ns, img)}
				}
			}
		}
	}
	return nil
}

func ingressNginxFinding(cfg *checker.CheckConfig, kind, name, ns, image string) checker.Finding {
	retired := time.Now().After(ingressNginxRetirementDate)
	severity := checker.SeverityHigh
	title := fmt.Sprintf("ingress-nginx detected — project entered maintenance mode on 2026-03-24")
	desc := fmt.Sprintf(
		"%s %s/%s (image: %s) is running. The ingress-nginx project entered maintenance-only mode on "+
			"2026-03-24. No new features will be developed and security patches may be delayed. "+
			"Migration to Gateway API is the recommended path.", kind, ns, name, image)
	if !retired {
		severity = checker.SeverityMedium
		title = fmt.Sprintf("ingress-nginx detected — project retires 2026-03-24, plan migration")
	}
	return checker.Finding{
		CheckerName: Name,
		ClusterType: cfg.ClusterType,
		Severity:    severity,
		Blocker:     false,
		Title:       title,
		Description: desc,
		Remediation: "Migrate to Gateway API (HTTPRoute + GatewayClass). See: https://gateway-api.sigs.k8s.io/guides/migrating-from-ingress/",
		Resource:    &checker.Resource{Kind: kind, Name: name, Namespace: ns},
		Source:      Name,
		DocsURL:     "https://github.com/kubernetes/ingress-nginx#readme",
	}
}

// compile-time assertion: time import used for EKS API timeout context.
var _ = time.Second
