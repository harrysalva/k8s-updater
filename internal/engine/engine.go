// Package engine assembles all checkers and runs them concurrently.
// Import this package from cmd/server — it owns the checker registry.
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"upgrade-guardian/internal/checker"
	"upgrade-guardian/internal/checks/controlplane"
	"upgrade-guardian/internal/checks/crd"
	"upgrade-guardian/internal/checks/deprecated"
	"upgrade-guardian/internal/checks/capacity"
	"upgrade-guardian/internal/checks/etcd"
	"upgrade-guardian/internal/checks/helm"
	"upgrade-guardian/internal/checks/istio"
	"upgrade-guardian/internal/checks/karpenter"
	"upgrade-guardian/internal/checks/nodes"
	"upgrade-guardian/internal/checks/preflight"
	"upgrade-guardian/internal/checks/provider"
	vpccni "upgrade-guardian/internal/checks/vpc-cni"
	"upgrade-guardian/internal/checks/webhooks"
	"upgrade-guardian/internal/checks/workloads"
	"upgrade-guardian/internal/detector"
)

// Engine runs all registered checkers concurrently and returns a Report.
type Engine struct {
	checkers []checker.Checker
}

// New returns an Engine with all registered checkers.
func New() *Engine {
	return &Engine{
		checkers: []checker.Checker{
			deprecated.New(),
			helm.New(),
			crd.New(),
			controlplane.New(),
			etcd.New(),
			nodes.New(),
			provider.New(),
			workloads.New(),
			webhooks.New(),
			capacity.New(),
			preflight.New(),
			karpenter.New(),
			istio.New(),
			vpccni.New(),
		},
	}
}

// Run detects the cluster type then executes every applicable checker concurrently.
func (e *Engine) Run(ctx context.Context, cfg *checker.CheckConfig) (*checker.Report, error) {
	ct, err := detector.Detect(ctx, cfg.KubeClient)
	if err != nil {
		return nil, fmt.Errorf("cluster detection: %w", err)
	}
	cfg.ClusterType = ct

	results := make([]checker.CheckResult, 0, len(e.checkers))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	for _, c := range e.checkers {
		if !c.Supports(ct) {
			results = append(results, checker.CheckResult{
				CheckerName: c.Name(),
				Skipped:     true,
				SkipReason:  fmt.Sprintf("not applicable for %s clusters", ct),
			})
			continue
		}

		wg.Add(1)
		go func(c checker.Checker) {
			defer wg.Done()

			findings, meta, err := c.Check(ctx, cfg)
			result := checker.CheckResult{
				CheckerName: c.Name(),
				Findings:    findings,
				Meta:        meta,
			}
			if err != nil {
				result.Error = err.Error()
			}

			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(c)
	}

	wg.Wait()

	// Inject staleness findings into the matching checker result.
	stale := matrixStalenessFindings(time.Now())
	for _, sf := range stale {
		for i, r := range results {
			if r.CheckerName == sf.CheckerName {
				results[i].Findings = append(results[i].Findings, sf)
				break
			}
		}
	}

	report := &checker.Report{
		ClusterType:    ct,
		CurrentVersion: cfg.CurrentVersion,
		TargetVersion:  cfg.TargetVersion,
		Results:        results,
		Timestamp:      time.Now().UTC(),
	}
	report.Blocker = hasBlocker(results)

	return report, nil
}

func hasBlocker(results []checker.CheckResult) bool {
	for _, r := range results {
		for _, f := range r.Findings {
			if f.Blocker {
				return true
			}
		}
	}
	return false
}
