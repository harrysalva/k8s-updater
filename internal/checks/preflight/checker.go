// Package preflight runs the actual upgrade tooling in dry-run mode to surface
// issues that only the upgrade engine itself knows about (control plane health,
// version compatibility, addon versions, IAM/CNI gaps for EKS).
//
// Implementation per cluster type:
//
//	EKS       → aws-sdk-go-v2 ListInsights / DescribeUpdate (no shell needed).
//	Upstream  → `kubeadm upgrade plan` via SSH (planned, currently informational).
//	Kubespray → inventory sanity check against the cluster.
package preflight

import (
	"context"
	"strconv"

	"upgrade-guardian/internal/checker"
)

const Name = "preflight-dryrun"

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

// Supports: only run for cluster types we can actually dry-run today.
// Unknown is excluded because we don't know which tool to invoke.
func (c *Checker) Supports(ct checker.ClusterType) bool {
	switch ct {
	case checker.ClusterTypeEKS, checker.ClusterTypeUpstream, checker.ClusterTypeKubespray:
		return true
	}
	return false
}

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	switch cfg.ClusterType {
	case checker.ClusterTypeEKS:
		return checkEKSInsights(ctx, cfg)
	case checker.ClusterTypeUpstream:
		return checkKubeadm(ctx, cfg)
	case checker.ClusterTypeKubespray:
		return checkKubespray(ctx, cfg)
	}
	return nil, map[string]string{"platform": string(cfg.ClusterType)}, nil
}

// metaWithCounts is a helper to build the meta map consistently across platforms.
func metaWithCounts(platform string, checked, errors, warnings int) map[string]string {
	return map[string]string{
		"platform":        platform,
		"insights_checked": strconv.Itoa(checked),
		"errors":          strconv.Itoa(errors),
		"warnings":        strconv.Itoa(warnings),
	}
}
