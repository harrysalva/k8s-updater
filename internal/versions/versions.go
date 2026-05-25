// Package versions reports the embedded tool database versions and validates
// that they cover the requested upgrade target. Call CheckCoverage at startup
// and from the /api/v1/versions endpoint.
package versions

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	plutoversionsfile "github.com/fairwindsops/pluto/v5"
)

// ToolInfo describes a single bundled tool's version and database coverage.
type ToolInfo struct {
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	DBType      string     `json:"db_type"`            // "embedded" | "cached" | "runtime"
	MaxK8s      string     `json:"max_k8s"`            // highest k8s version in database
	CachedAt    *time.Time `json:"cached_at,omitempty"` // when the cache was last updated
	Description string     `json:"description"`
}

// CoverageWarning is returned when a tool's database may not cover the target version.
type CoverageWarning struct {
	Tool    string `json:"tool"`
	Message string `json:"message"`
}

// Report is the response body for GET /api/v1/versions.
type Report struct {
	Tools    []ToolInfo        `json:"tools"`
	Warnings []CoverageWarning `json:"warnings,omitempty"`
}

// Get returns the current tool report. targetVersion may be empty (no coverage check).
func Get(targetVersion string) *Report {
	// Use cached database if available, otherwise fall back to embedded.
	content := PlutoContent(plutoversionsfile.Content())
	plutoMax := computeMaxK8s(content)

	cachedExists, cachedAt := PlutoCacheInfo()
	dbType := "embedded"
	var cachedAtPtr *time.Time
	if cachedExists {
		dbType = "cached"
		cachedAtPtr = &cachedAt
	}

	tools := []ToolInfo{
		{
			Name:        "pluto",
			Version:     "v5.24.0",
			DBType:      dbType,
			MaxK8s:      plutoMax,
			CachedAt:    cachedAtPtr,
			Description: "Deprecated/removed Kubernetes API detection. Database can be updated at runtime without rebuilding the binary.",
		},
		{
			Name:        "nova",
			Version:     "v0.0.0-20260427",
			DBType:      "runtime",
			MaxK8s:      "n/a",
			Description: "Helm chart kubeVersion compatibility. Reads constraints declared in each chart at scan time — no separate database.",
		},
		{
			Name:        "kubeconform",
			Version:     "v0.7.0",
			DBType:      "runtime",
			MaxK8s:      "current",
			Description: "CRD schema validation. Downloads schemas from kubernetesjsonschema.dev at scan time. Third-party CRD schemas (Dapr, operators) may be absent.",
		},
	}

	var warnings []CoverageWarning

	if targetVersion != "" {
		targetMinor := parseMinor(targetVersion)
		plutoMinor  := parseMinor(plutoMax)

		if plutoMinor > 0 && targetMinor > plutoMinor {
			warnings = append(warnings, CoverageWarning{
				Tool: "pluto",
				Message: fmt.Sprintf(
					"Pluto database covers up to k8s %s but target is %s. Some deprecated/removed APIs for %s may not be detected. Run `go get github.com/fairwindsops/pluto/v5@latest && go mod tidy` to update.",
					plutoMax, targetVersion, targetVersion,
				),
			})
		}
	}

	return &Report{Tools: tools, Warnings: warnings}
}

func parseMinor(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(parts[1])
	return n
}
