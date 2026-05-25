package checker

import "context"

// Checker is the single interface every validation module must implement.
// Rules:
//   - Check MUST be deterministic. No LLM calls here.
//   - Check MUST return zero findings rather than a false positive.
//   - Supports gates which cluster types this checker applies to.
type Checker interface {
	Name() string
	Supports(ct ClusterType) bool
	Check(ctx context.Context, cfg *CheckConfig) ([]Finding, map[string]string, error)
}
