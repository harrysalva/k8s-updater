// Package workloads validates that running workloads can survive a rolling
// node drain during an upgrade. Detects PodDisruptionBudgets that make drain
// impossible, single-replica workloads in user namespaces, missing readiness
// probes, and pods already in a broken state.
package workloads

import (
	"context"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"upgrade-guardian/internal/checker"
)

const Name = "workloads-readiness"

// systemNamespaces are excluded from single-replica warnings — these are
// expected to be operator/control-plane components whose HA is handled elsewhere.
var systemNamespaces = map[string]bool{
	"kube-system":        true,
	"kube-public":        true,
	"kube-node-lease":    true,
	"local-path-storage": true,
}

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	var findings []checker.Finding

	pdbsChecked, pdbBlockers := c.checkPDBs(ctx, cfg, &findings)
	deploysChecked, singleReplica := c.checkSingleReplicaDeployments(ctx, cfg, &findings)
	stsChecked, _ := c.checkSingleReplicaStatefulSets(ctx, cfg, &findings)
	missingProbes := c.checkMissingProbes(ctx, cfg, &findings)
	brokenPods := c.checkBrokenPods(ctx, cfg, &findings)

	meta := map[string]string{
		"pdbs_checked":        strconv.Itoa(pdbsChecked),
		"pdb_blockers":        strconv.Itoa(pdbBlockers),
		"deployments_checked": strconv.Itoa(deploysChecked),
		"statefulsets_checked": strconv.Itoa(stsChecked),
		"single_replica":      strconv.Itoa(singleReplica),
		"missing_probes":      strconv.Itoa(missingProbes),
		"broken_pods":         strconv.Itoa(brokenPods),
	}
	return findings, meta, nil
}

func (c *Checker) checkPDBs(ctx context.Context, cfg *checker.CheckConfig, findings *[]checker.Finding) (checked, blockers int) {
	pdbs, err := cfg.KubeClient.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0
	}
	checked = len(pdbs.Items)

	for _, pdb := range pdbs.Items {
		if blocker := analyzePDB(&pdb); blocker != nil {
			blockers++
			*findings = append(*findings, *blocker)
		}
	}
	return checked, blockers
}

// analyzePDB returns a critical blocker finding if the PDB makes drain impossible.
// A PDB with minAvailable >= expectedPods or maxUnavailable == 0 will block evictions
// indefinitely during a node drain.
func analyzePDB(pdb *policyv1.PodDisruptionBudget) *checker.Finding {
	// expectedPods is reported by the controller — falls back to currentHealthy.
	expected := pdb.Status.ExpectedPods
	if expected == 0 {
		expected = pdb.Status.CurrentHealthy
	}

	// maxUnavailable: 0 — explicit ban on disruption.
	if pdb.Spec.MaxUnavailable != nil && intstr.ValueOrDefault(pdb.Spec.MaxUnavailable, intstr.FromInt(-1)).IntValue() == 0 {
		return &checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       fmt.Sprintf("%s/%s: PDB blocks all evictions (maxUnavailable=0)", pdb.Namespace, pdb.Name),
			Description: "This PodDisruptionBudget forbids any pod eviction. Node drains during upgrade will hang indefinitely.",
			Remediation: "Set maxUnavailable >= 1 or use minAvailable < replicas on the PDB.",
			Resource: &checker.Resource{
				Kind: "PodDisruptionBudget", Name: pdb.Name, Namespace: pdb.Namespace, APIGroup: "policy/v1",
			},
			Source:  "workloads",
			DocsURL: "https://kubernetes.io/docs/tasks/run-application/configure-pdb/",
		}
	}

	// minAvailable >= expectedPods — controller can never disrupt any pod.
	if pdb.Spec.MinAvailable != nil && expected > 0 {
		minAvail := intOrPctValue(pdb.Spec.MinAvailable, int(expected))
		if minAvail >= int(expected) {
			return &checker.Finding{
				CheckerName: Name,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("%s/%s: PDB minAvailable=%d >= replicas=%d", pdb.Namespace, pdb.Name, minAvail, expected),
				Description: "minAvailable equals or exceeds the workload replica count. No pod can be evicted, blocking node drain.",
				Remediation: fmt.Sprintf("Reduce minAvailable to %d or lower, or scale the workload to >%d replicas.", expected-1, minAvail),
				Resource: &checker.Resource{
					Kind: "PodDisruptionBudget", Name: pdb.Name, Namespace: pdb.Namespace, APIGroup: "policy/v1",
				},
				Source:  "workloads",
				DocsURL: "https://kubernetes.io/docs/tasks/run-application/configure-pdb/",
			}
		}
	}
	return nil
}

// intOrPctValue resolves an IntOrString to an absolute integer given the total population.
func intOrPctValue(v *intstr.IntOrString, total int) int {
	if v.Type == intstr.Int {
		return v.IntValue()
	}
	pct, err := strconv.Atoi(stripPct(v.StrVal))
	if err != nil {
		return 0
	}
	return (total * pct) / 100
}

func stripPct(s string) string {
	if len(s) > 0 && s[len(s)-1] == '%' {
		return s[:len(s)-1]
	}
	return s
}

func (c *Checker) checkSingleReplicaDeployments(ctx context.Context, cfg *checker.CheckConfig, findings *[]checker.Finding) (checked, single int) {
	deploys, err := cfg.KubeClient.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0
	}
	checked = len(deploys.Items)
	for _, d := range deploys.Items {
		if systemNamespaces[d.Namespace] {
			continue
		}
		if d.Spec.Replicas != nil && *d.Spec.Replicas == 1 {
			single++
			*findings = append(*findings, singleReplicaFinding("Deployment", d.Namespace, d.Name))
		}
	}
	return checked, single
}

func (c *Checker) checkSingleReplicaStatefulSets(ctx context.Context, cfg *checker.CheckConfig, findings *[]checker.Finding) (checked, single int) {
	sts, err := cfg.KubeClient.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0
	}
	checked = len(sts.Items)
	for _, s := range sts.Items {
		if systemNamespaces[s.Namespace] {
			continue
		}
		if s.Spec.Replicas != nil && *s.Spec.Replicas == 1 {
			single++
			*findings = append(*findings, singleReplicaFinding("StatefulSet", s.Namespace, s.Name))
		}
	}
	return checked, single
}

func singleReplicaFinding(kind, ns, name string) checker.Finding {
	return checker.Finding{
		CheckerName: Name,
		Severity:    checker.SeverityHigh,
		Blocker:     false,
		Title:       fmt.Sprintf("%s/%s: %s has replicas=1", ns, name, kind),
		Description: "Single-replica workload outside a system namespace will be unavailable while its node is drained.",
		Remediation: "Scale to >= 2 replicas with a topology spread across nodes, or accept downtime explicitly.",
		Resource:    &checker.Resource{Kind: kind, Name: name, Namespace: ns, APIGroup: "apps/v1"},
		Source:      "workloads",
	}
}

func (c *Checker) checkMissingProbes(ctx context.Context, cfg *checker.CheckConfig, findings *[]checker.Finding) (missing int) {
	deploys, err := cfg.KubeClient.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0
	}
	for _, d := range deploys.Items {
		if systemNamespaces[d.Namespace] {
			continue
		}
		if d.Spec.Replicas == nil || *d.Spec.Replicas < 2 {
			continue
		}
		if podSpecMissingReadiness(&d.Spec.Template.Spec) {
			missing++
			*findings = append(*findings, checker.Finding{
				CheckerName: Name,
				Severity:    checker.SeverityMedium,
				Blocker:     false,
				Title:       fmt.Sprintf("%s/%s: Deployment lacks readinessProbe", d.Namespace, d.Name),
				Description: "Containers without readinessProbe are considered ready immediately. During rolling node drain, traffic may hit pods that haven't finished initializing.",
				Remediation: "Add readinessProbe (httpGet, tcpSocket, or exec) to each container in the pod template.",
				Resource:    &checker.Resource{Kind: "Deployment", Name: d.Name, Namespace: d.Namespace, APIGroup: "apps/v1"},
				Source:      "workloads",
				DocsURL:     "https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/",
			})
		}
	}
	return missing
}

func podSpecMissingReadiness(spec *corev1.PodSpec) bool {
	for _, ctr := range spec.Containers {
		if ctr.ReadinessProbe == nil {
			return true
		}
	}
	return false
}

func (c *Checker) checkBrokenPods(ctx context.Context, cfg *checker.CheckConfig, findings *[]checker.Finding) (broken int) {
	pods, err := cfg.KubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0
	}
	for _, p := range pods.Items {
		state := podBrokenState(&p)
		if state == "" {
			continue
		}
		broken++
		*findings = append(*findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityHigh,
			Blocker:     true,
			Title:       fmt.Sprintf("%s/%s: pod is %s", p.Namespace, p.Name, state),
			Description: fmt.Sprintf("Pod is in %s state before upgrade. Upgrades amplify existing scheduling/runtime issues — fix before proceeding.", state),
			Remediation: "Investigate with `kubectl describe pod` and resolve before starting the upgrade.",
			Resource:    &checker.Resource{Kind: "Pod", Name: p.Name, Namespace: p.Namespace, APIGroup: "v1"},
			Source:      "workloads",
		})
	}
	return broken
}

// podBrokenState returns a non-empty label for pods that are unhealthy.
// Pending pods with no scheduling decision, or pods with containers in CrashLoopBackOff, count as broken.
func podBrokenState(p *corev1.Pod) string {
	if p.Status.Phase == corev1.PodPending {
		// Skip pods that just started and are pulling images
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
				return "Pending(Unschedulable)"
			}
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			return "CrashLoopBackOff"
		}
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ImagePullBackOff" {
			return "ImagePullBackOff"
		}
	}
	return ""
}

// resourceQty is used by analyzePDB for percentage interpretation. Kept here
// to avoid import side effects in tests.
var _ = resource.Quantity{}
var _ = appsv1.Deployment{}
