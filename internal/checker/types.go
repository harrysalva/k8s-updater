package checker

import (
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type ClusterType string

const (
	ClusterTypeEKS       ClusterType = "eks"
	ClusterTypeKubespray ClusterType = "kubespray"
	ClusterTypeUpstream  ClusterType = "upstream"
	ClusterTypeUnknown   ClusterType = "unknown"
)

type Severity string

const (
	// SeverityCritical blocks the upgrade — do not proceed.
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityInfo     Severity = "info"
)

type Resource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	APIGroup  string `json:"api_group,omitempty"`
}

// Finding is a single validated problem discovered by a deterministic checker.
// LLMs may translate/prioritize these but never generate them.
type Finding struct {
	ID          string      `json:"id"`
	CheckerName string      `json:"checker_name"`
	ClusterType ClusterType `json:"cluster_type"`
	Severity    Severity    `json:"severity"`
	// Blocker=true means this finding MUST be resolved before upgrading.
	Blocker     bool      `json:"blocker"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Remediation string    `json:"remediation"`
	Resource    *Resource `json:"resource,omitempty"`
	// Source identifies which underlying tool produced this finding.
	Source  string `json:"source"`
	DocsURL string `json:"docs_url,omitempty"`
}

// KubesprayConfig holds paths to Kubespray inventory files.
// Component versions are always read from these files, never from kubectl.
type KubesprayConfig struct {
	InventoryPath string // path to inventory.ini or hosts.yaml
	GroupVarsPath string // path to group_vars/ directory
}

// EKSConfig holds EKS-specific identifiers for AWS API calls.
type EKSConfig struct {
	ClusterName string
	Region      string
	AccountID   string
}

// CheckConfig is passed to every Checker.Check call.
type CheckConfig struct {
	ClusterType    ClusterType
	CurrentVersion string
	TargetVersion  string
	KubeClient     kubernetes.Interface
	RestConfig     *rest.Config
	// Set only for Kubespray clusters.
	KubesprayConfig *KubesprayConfig
	// Set only for EKS clusters.
	EKSConfig *EKSConfig
}

// CheckResult is the output of a single Checker run.
type CheckResult struct {
	CheckerName string            `json:"checker_name"`
	Findings    []Finding         `json:"findings"`
	Meta        map[string]string `json:"meta,omitempty"`
	Error       string            `json:"error,omitempty"`
	Skipped     bool              `json:"skipped,omitempty"`
	SkipReason  string            `json:"skip_reason,omitempty"`
}

// Report aggregates all CheckResults for a target version upgrade.
type Report struct {
	ClusterType    ClusterType   `json:"cluster_type"`
	CurrentVersion string        `json:"current_version"`
	TargetVersion  string        `json:"target_version"`
	Results        []CheckResult `json:"results"`
	Timestamp      time.Time     `json:"timestamp"`
	// Blocker=true if any finding in any result is a blocker.
	Blocker bool `json:"blocker"`
}
