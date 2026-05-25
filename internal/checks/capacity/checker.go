// Package capacity simulates a rolling node drain to confirm there is
// enough headroom for pods to be rescheduled during upgrade. Without
// headroom, drains leave pods Pending indefinitely.
package capacity

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "capacity-headroom"

// minHeadroomPct is the cluster-wide CPU/memory headroom below which we warn.
const minHeadroomPct = 20

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	nodes, err := cfg.KubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: list nodes: %w", Name, err)
	}

	if len(nodes.Items) < 2 {
		return nil, map[string]string{
			"nodes":      strconv.Itoa(len(nodes.Items)),
			"skip_reason": "single-node cluster — drain simulation not applicable",
		}, nil
	}

	pods, err := cfg.KubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: list pods: %w", Name, err)
	}

	// Aggregate requests per node, total allocatable per node.
	nodeUsage := map[string]*nodeStat{}
	for _, n := range nodes.Items {
		nodeUsage[n.Name] = &nodeStat{
			allocCPU: n.Status.Allocatable.Cpu().MilliValue(),
			allocMem: n.Status.Allocatable.Memory().Value(),
		}
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning && p.Status.Phase != corev1.PodPending {
			continue
		}
		ns, ok := nodeUsage[p.Spec.NodeName]
		if !ok {
			continue
		}
		cpu, mem := podRequests(&p)
		ns.usedCPU += cpu
		ns.usedMem += mem
	}

	var findings []checker.Finding

	// 1. Worst-case drain simulation.
	worstNode, worstDrainFits := simulateWorstDrain(nodeUsage)
	if !worstDrainFits {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       fmt.Sprintf("Draining %s would leave pods unschedulable", worstNode),
			Description: "Simulated draining the most-loaded node — its pods do not fit on the remaining nodes given current requests. During upgrade those pods would stay Pending.",
			Remediation: "Add a node (temporarily), reduce pod requests, or scale down low-priority workloads before upgrading.",
			Resource:    &checker.Resource{Kind: "Node", Name: worstNode, APIGroup: "v1"},
			Source:      "capacity",
		})
	}

	// 2. Cluster-wide headroom.
	totalAllocCPU, totalAllocMem := int64(0), int64(0)
	totalUsedCPU, totalUsedMem := int64(0), int64(0)
	for _, s := range nodeUsage {
		totalAllocCPU += s.allocCPU
		totalAllocMem += s.allocMem
		totalUsedCPU += s.usedCPU
		totalUsedMem += s.usedMem
	}
	cpuHeadroom := pct(totalAllocCPU-totalUsedCPU, totalAllocCPU)
	memHeadroom := pct(totalAllocMem-totalUsedMem, totalAllocMem)

	if cpuHeadroom < minHeadroomPct {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityHigh,
			Blocker:     false,
			Title:       fmt.Sprintf("Cluster CPU headroom is %d%% (recommended: >=%d%%)", cpuHeadroom, minHeadroomPct),
			Description: "Low CPU headroom means a node drain may not find space for evicted pods.",
			Remediation: "Add capacity, reduce CPU requests, or upgrade during a low-traffic window with one node drained at a time.",
			Source:      "capacity",
		})
	}
	if memHeadroom < minHeadroomPct {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityHigh,
			Blocker:     false,
			Title:       fmt.Sprintf("Cluster memory headroom is %d%% (recommended: >=%d%%)", memHeadroom, minHeadroomPct),
			Description: "Low memory headroom can leave evicted pods Pending during rolling drains.",
			Remediation: "Add capacity, reduce memory requests, or right-size noisy workloads before upgrading.",
			Source:      "capacity",
		})
	}

	// 3. Heavily committed individual nodes.
	for name, s := range nodeUsage {
		cpuCommit := pct(s.usedCPU, s.allocCPU)
		memCommit := pct(s.usedMem, s.allocMem)
		if cpuCommit > 85 || memCommit > 85 {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				Severity:    checker.SeverityMedium,
				Blocker:     false,
				Title:       fmt.Sprintf("Node %s heavily committed (CPU=%d%%, mem=%d%%)", name, cpuCommit, memCommit),
				Description: "When this node drains, its pods need ~equal capacity elsewhere; high commitment elsewhere may prevent that.",
				Remediation: "Right-size requests on this node's workloads, or add a node.",
				Resource:    &checker.Resource{Kind: "Node", Name: name, APIGroup: "v1"},
				Source:      "capacity",
			})
		}
	}

	// 4. ResourceQuota saturation.
	quotas, err := cfg.KubeClient.CoreV1().ResourceQuotas("").List(ctx, metav1.ListOptions{})
	saturatedQuotas := 0
	if err == nil {
		for _, q := range quotas.Items {
			for resName, hard := range q.Status.Hard {
				used := q.Status.Used[resName]
				if pctQuantity(used, hard) > 90 {
					saturatedQuotas++
					findings = append(findings, checker.Finding{
						CheckerName: Name,
						Severity:    checker.SeverityMedium,
						Blocker:     false,
						Title:       fmt.Sprintf("%s/%s: ResourceQuota %s at %d%%", q.Namespace, q.Name, resName, pctQuantity(used, hard)),
						Description: "Near-saturated quota may block pod rescheduling during drain.",
						Remediation: "Raise the quota or free up usage in this namespace before upgrading.",
						Resource:    &checker.Resource{Kind: "ResourceQuota", Name: q.Name, Namespace: q.Namespace, APIGroup: "v1"},
						Source:      "capacity",
					})
				}
			}
		}
	}

	meta := map[string]string{
		"nodes":                  strconv.Itoa(len(nodes.Items)),
		"cluster_cpu_headroom":   strconv.Itoa(cpuHeadroom),
		"cluster_mem_headroom":   strconv.Itoa(memHeadroom),
		"worst_node_drain_fits":  strconv.FormatBool(worstDrainFits),
		"saturated_quotas":       strconv.Itoa(saturatedQuotas),
	}
	return findings, meta, nil
}

type nodeStat struct {
	allocCPU, allocMem int64
	usedCPU, usedMem   int64
}

// podRequests returns total CPU (milli) and memory (bytes) requests for a pod.
// Includes init containers (max) per Kubernetes scheduler semantics.
func podRequests(p *corev1.Pod) (cpu, mem int64) {
	var initMaxCPU, initMaxMem int64
	for _, ctr := range p.Spec.InitContainers {
		c := ctr.Resources.Requests.Cpu().MilliValue()
		m := ctr.Resources.Requests.Memory().Value()
		if c > initMaxCPU {
			initMaxCPU = c
		}
		if m > initMaxMem {
			initMaxMem = m
		}
	}
	for _, ctr := range p.Spec.Containers {
		cpu += ctr.Resources.Requests.Cpu().MilliValue()
		mem += ctr.Resources.Requests.Memory().Value()
	}
	if initMaxCPU > cpu {
		cpu = initMaxCPU
	}
	if initMaxMem > mem {
		mem = initMaxMem
	}
	return
}

// simulateWorstDrain finds the most CPU+memory-loaded node and checks whether
// its load fits on the remaining nodes' free capacity.
func simulateWorstDrain(nodeUsage map[string]*nodeStat) (worstNode string, fits bool) {
	var worst *nodeStat
	for name, s := range nodeUsage {
		if worst == nil || s.usedCPU+s.usedMem/1_000_000 > worst.usedCPU+worst.usedMem/1_000_000 {
			worst = s
			worstNode = name
		}
	}
	if worst == nil {
		return "", true
	}

	var freeCPU, freeMem int64
	for name, s := range nodeUsage {
		if name == worstNode {
			continue
		}
		freeCPU += s.allocCPU - s.usedCPU
		freeMem += s.allocMem - s.usedMem
	}
	return worstNode, freeCPU >= worst.usedCPU && freeMem >= worst.usedMem
}

func pct(used, total int64) int {
	if total <= 0 {
		return 0
	}
	return int((used * 100) / total)
}

// pctQuantity converts two resource.Quantity values into a percentage.
// Returns 0 if hard is zero or unparseable.
func pctQuantity(used, hard resource.Quantity) int {
	h := hard.MilliValue()
	if h <= 0 {
		return 0
	}
	return int((used.MilliValue() * 100) / h)
}
