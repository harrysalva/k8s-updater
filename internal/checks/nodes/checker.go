// Package nodes checks node-level OS/hardware problems.
//
// Two detection layers:
//  1. Kubelet-native conditions: always present regardless of NPD
//     (MemoryPressure, DiskPressure, PIDPressure, NetworkUnavailable).
//  2. NPD conditions: only present when node-problem-detector is deployed.
//     If NPD is missing, a medium finding instructs the operator to install it
//     via POST /api/v1/npd/install.
package nodes

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "node-health"

// kubeletBlockerConditions are standard conditions reported by the kubelet itself.
// Status=True means the problem is active. DiskPressure is a blocker because etcd
// needs disk space for WAL snapshots during the upgrade.
var kubeletBlockerConditions = map[corev1.NodeConditionType]string{
	corev1.NodeDiskPressure:       "Node has disk pressure. etcd requires free disk space for upgrade snapshots.",
	corev1.NodeNetworkUnavailable: "Node network is unavailable. Upgrade requires healthy inter-node communication.",
}

// kubeletWarnConditions are standard conditions that warrant investigation but are not hard blockers.
var kubeletWarnConditions = map[corev1.NodeConditionType]string{
	corev1.NodeMemoryPressure: "Node has memory pressure. Upgrade workload may be evicted mid-rollout.",
	corev1.NodePIDPressure:    "Node has PID pressure. New control-plane processes may fail to start.",
}

// npdBlockerConditions are conditions published by node-problem-detector that block upgrades.
// Source: https://github.com/kubernetes/node-problem-detector
var npdBlockerConditions = map[corev1.NodeConditionType]bool{
	"KernelDeadlock":             true,
	"ReadonlyFilesystem":         true,
	"CorruptDockerOverlay":       true,
	"FrequentUnregisterNetDevice": true,
}

// npdWarnConditions are NPD conditions to investigate before upgrading.
var npdWarnConditions = map[corev1.NodeConditionType]bool{
	"OOMKilling":       true,
	"TaskHangAnalysis": true,
}

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	nodes, err := cfg.KubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	// Count roles and collect kubelet versions.
	cpCount, workerCount := 0, 0
	kubeletVersions := map[string]bool{}
	for _, node := range nodes.Items {
		if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
			cpCount++
		} else {
			workerCount++
		}
		if v := node.Status.NodeInfo.KubeletVersion; v != "" {
			kubeletVersions[v] = true
		}
	}

	meta := map[string]string{
		"nodes_checked":        strconv.Itoa(len(nodes.Items)),
		"control_plane_nodes":  strconv.Itoa(cpCount),
		"worker_nodes":         strconv.Itoa(workerCount),
	}

	var findings []checker.Finding

	if !c.isNPDDeployed(ctx, cfg) {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityMedium,
			Blocker:     false,
			Title:       "node-problem-detector is not deployed",
			Description: "No node-problem-detector DaemonSet found in kube-system. OS-level problems (kernel deadlocks, readonly filesystem, OOM) will not be surfaced as NodeConditions.",
			Remediation: "Deploy node-problem-detector before upgrading: kubectl apply -f https://k8s.io/examples/debug/node-problem-detector.yaml",
			Source:      Name,
			DocsURL:     "https://kubernetes.io/docs/tasks/debug/debug-cluster/monitor-node-health/",
		})
	}

	for _, node := range nodes.Items {
		findings = append(findings, c.checkKubeletConditions(node, cfg.ClusterType)...)
		findings = append(findings, c.checkNPDConditions(node, cfg.ClusterType)...)
		findings = append(findings, c.checkNodeReady(node, cfg.ClusterType)...)
		findings = append(findings, c.checkKubeletVersionSkew(node, cfg)...)
	}

	return findings, meta, nil
}

// isNPDDeployed returns true if a node-problem-detector DaemonSet is present in kube-system.
func (c *Checker) isNPDDeployed(ctx context.Context, cfg *checker.CheckConfig) bool {
	ds, err := cfg.KubeClient.AppsV1().DaemonSets("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	for _, d := range ds.Items {
		if d.Name == "node-problem-detector" {
			return true
		}
		// Some distributions use a label-based name.
		if v, ok := d.Labels["app"]; ok && v == "node-problem-detector" {
			return true
		}
	}
	return false
}

// checkKubeletConditions checks standard conditions reported by the kubelet.
// These are always available regardless of whether NPD is deployed.
func (c *Checker) checkKubeletConditions(node corev1.Node, ct checker.ClusterType) []checker.Finding {
	var findings []checker.Finding

	for _, cond := range node.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}

		if desc, ok := kubeletBlockerConditions[cond.Type]; ok {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: ct,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("Node %s: %s", node.Name, cond.Type),
				Description: desc,
				Remediation: cond.Message,
				Resource:    &checker.Resource{Kind: "Node", Name: node.Name},
				Source:      "kubelet",
				DocsURL:     "https://kubernetes.io/docs/concepts/architecture/nodes/#condition",
			})
			continue
		}

		if desc, ok := kubeletWarnConditions[cond.Type]; ok {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: ct,
				Severity:    checker.SeverityHigh,
				Blocker:     false,
				Title:       fmt.Sprintf("Node %s: %s", node.Name, cond.Type),
				Description: desc,
				Remediation: cond.Message,
				Resource:    &checker.Resource{Kind: "Node", Name: node.Name},
				Source:      "kubelet",
				DocsURL:     "https://kubernetes.io/docs/concepts/architecture/nodes/#condition",
			})
		}
	}

	return findings
}

// checkNPDConditions checks conditions published by node-problem-detector.
func (c *Checker) checkNPDConditions(node corev1.Node, ct checker.ClusterType) []checker.Finding {
	var findings []checker.Finding

	for _, cond := range node.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}

		if npdBlockerConditions[cond.Type] {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: ct,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("Node %s: %s", node.Name, cond.Type),
				Description: cond.Message,
				Remediation: "Resolve the node problem before upgrading. Check node-problem-detector logs.",
				Resource:    &checker.Resource{Kind: "Node", Name: node.Name},
				Source:      "node-problem-detector",
				DocsURL:     "https://github.com/kubernetes/node-problem-detector",
			})
			continue
		}

		if npdWarnConditions[cond.Type] {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: ct,
				Severity:    checker.SeverityHigh,
				Blocker:     false,
				Title:       fmt.Sprintf("Node %s: %s detected", node.Name, cond.Type),
				Description: cond.Message,
				Remediation: "Investigate before upgrading; may indicate instability.",
				Resource:    &checker.Resource{Kind: "Node", Name: node.Name},
				Source:      "node-problem-detector",
			})
		}
	}

	return findings
}

func (c *Checker) checkNodeReady(node corev1.Node, ct checker.ClusterType) []checker.Finding {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
			return []checker.Finding{{
				CheckerName: Name,
				ClusterType: ct,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("Node %s is NotReady", node.Name),
				Description: cond.Message,
				Remediation: "All nodes must be Ready before upgrading.",
				Resource:    &checker.Resource{Kind: "Node", Name: node.Name},
				Source:      Name,
			}}
		}
	}
	return nil
}

// checkKubeletVersionSkew enforces the Kubernetes version skew policy:
// kubelet must be within 2 minor versions of the target API server.
// Ref: https://kubernetes.io/releases/version-skew-policy/#kubelet
func (c *Checker) checkKubeletVersionSkew(node corev1.Node, cfg *checker.CheckConfig) []checker.Finding {
	kubeletVer := strings.TrimPrefix(node.Status.NodeInfo.KubeletVersion, "v")
	if kubeletVer == "" {
		return nil
	}

	kubeletMinor := parseMinor(kubeletVer)
	targetMinor  := parseMinor(strings.TrimPrefix(cfg.TargetVersion, "v"))
	if kubeletMinor < 0 || targetMinor < 0 {
		return nil
	}

	skew := targetMinor - kubeletMinor
	if skew <= 2 {
		return nil
	}

	role := "worker"
	if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
		role = "control-plane"
	}

	return []checker.Finding{{
		CheckerName: Name,
		ClusterType: cfg.ClusterType,
		Severity:    checker.SeverityCritical,
		Blocker:     true,
		Title:       fmt.Sprintf("Node %s (%s): kubelet v%s is %d minor versions behind target", node.Name, role, kubeletVer, skew),
		Description: fmt.Sprintf("kubelet v%s on node %s would be %d minor versions behind the target API server v%s. Kubernetes supports at most 2 minor versions of skew.", kubeletVer, node.Name, skew, cfg.TargetVersion),
		Remediation: fmt.Sprintf("Upgrade kubelet on %s to v%s before upgrading the cluster.", node.Name, cfg.TargetVersion),
		Resource:    &checker.Resource{Kind: "Node", Name: node.Name},
		Source:      Name,
		DocsURL:     "https://kubernetes.io/releases/version-skew-policy/#kubelet",
	}}
}

// parseMinor extracts the minor version integer from a semver string like "1.29.4".
func parseMinor(v string) int {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return -1
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return -1
	}
	return n
}
