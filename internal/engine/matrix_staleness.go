package engine

import (
	"fmt"
	"time"

	"upgrade-guardian/internal/checker"
	karpenterpkg "upgrade-guardian/internal/checks/karpenter"
	istiopkg "upgrade-guardian/internal/checks/istio"
	vpccnipkg "upgrade-guardian/internal/checks/vpc-cni"
)

// matrixEntry describes a static compatibility matrix and when it was last verified.
type matrixEntry struct {
	checkerName  string
	lastVerified time.Time
	docsURL      string
}

var staticMatrices = []matrixEntry{
	{
		checkerName:  karpenterpkg.Name,
		lastVerified: karpenterpkg.MatrixLastVerified,
		docsURL:      "https://karpenter.sh/docs/upgrading/compatibility/",
	},
	{
		checkerName:  istiopkg.Name,
		lastVerified: istiopkg.MatrixLastVerified,
		docsURL:      "https://istio.io/latest/docs/releases/supported-releases/",
	},
	{
		checkerName:  vpccnipkg.Name,
		lastVerified: vpccnipkg.MatrixLastVerified,
		docsURL:      "https://docs.aws.amazon.com/eks/latest/userguide/managing-vpc-cni.html",
	},
}

// matrixStalenessFindings emits findings for any static matrix that is stale.
// Callers inject `now` so this is testable without wall-clock dependency.
func matrixStalenessFindings(now time.Time) []checker.Finding {
	const warnDays = 180
	const critDays = 365

	var findings []checker.Finding
	for _, m := range staticMatrices {
		age := now.Sub(m.lastVerified)
		ageDays := int(age.Hours() / 24)
		if ageDays < warnDays {
			continue
		}

		sev := checker.SeverityMedium
		if ageDays >= critDays {
			sev = checker.SeverityHigh
		}

		findings = append(findings, checker.Finding{
			CheckerName: m.checkerName,
			Severity:    sev,
			Blocker:     false,
			Title:       fmt.Sprintf("Compatibility matrix for %s is %d days old", m.checkerName, ageDays),
			Description: fmt.Sprintf("The static compatibility matrix was last verified on %s (%d days ago). It may be out of date — new Kubernetes or add-on versions may not be covered.", m.lastVerified.Format("2006-01-02"), ageDays),
			Remediation: fmt.Sprintf("WebFetch the upstream source and update the matrix in internal/checks/%s/checker.go. Bump MatrixLastVerified to today.", m.checkerName),
			Source:      "matrix-staleness",
			DocsURL:     m.docsURL,
		})
	}
	return findings
}
