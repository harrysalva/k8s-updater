package engine

import (
	"testing"
	"time"

	"upgrade-guardian/internal/checker"
)

func TestMatrixStaleness_NotStale(t *testing.T) {
	now := time.Now()
	findings := matrixStalenessFindings(now)
	if len(findings) != 0 {
		t.Errorf("freshly verified matrices should produce 0 staleness findings, got %d", len(findings))
	}
}

func TestMatrixStaleness_WarnAfter180Days(t *testing.T) {
	// Simulate 200 days since last verification.
	now := staticMatrices[0].lastVerified.Add(200 * 24 * time.Hour)
	findings := matrixStalenessFindings(now)
	if len(findings) == 0 {
		t.Fatal("expected staleness findings after 200 days, got none")
	}
	for _, f := range findings {
		if f.Severity != checker.SeverityMedium && f.Severity != checker.SeverityHigh {
			t.Errorf("expected medium or high severity, got %s", f.Severity)
		}
		if f.Blocker {
			t.Error("staleness findings must not be blockers")
		}
	}
}

func TestMatrixStaleness_HighAfter365Days(t *testing.T) {
	now := staticMatrices[0].lastVerified.Add(400 * 24 * time.Hour)
	findings := matrixStalenessFindings(now)
	var hasHigh bool
	for _, f := range findings {
		if f.Severity == checker.SeverityHigh {
			hasHigh = true
		}
	}
	if !hasHigh {
		t.Error("expected at least one high finding after 400 days")
	}
}
