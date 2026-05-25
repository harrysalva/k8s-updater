package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"upgrade-guardian/internal/checker"
	"upgrade-guardian/internal/diff"
	"upgrade-guardian/internal/versions"
)

// ANSI color codes for terminal output.
const (
	colorReset   = "\033[0m"
	colorBold    = "\033[1m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorGray    = "\033[90m"
)

// PrintReport formats and prints a Report based on the CLI format.
func PrintReport(format string, report *checker.Report, verbose bool) error {
	switch format {
	case "json":
		return printReportJSON(report)
	case "csv":
		return printReportCSV(report)
	default:
		return printReportTable(report, verbose)
	}
}

// printReportTable formats the report as a human-readable table.
func printReportTable(report *checker.Report, verbose bool) error {
	fmt.Printf("\n%s=== Upgrade Check: %s → %s ===%s\n",
		colorBold, report.CurrentVersion, report.TargetVersion, colorReset)
	fmt.Printf("Cluster type: %s | Timestamp: %s\n", report.ClusterType, report.Timestamp.Format("2006-01-02 15:04:05"))

	if report.Blocker {
		fmt.Printf("\n%s⚠ BLOCKERS FOUND — upgrade cannot proceed%s\n", colorRed, colorReset)
	} else {
		fmt.Printf("\n%s✓ No blockers — safe to upgrade%s\n", colorGreen, colorReset)
	}

	// Summary by severity
	bySev := map[string]int{}
	for _, res := range report.Results {
		for _, f := range res.Findings {
			bySev[string(f.Severity)]++
		}
	}

	fmt.Println("\nFindings by severity:")
	severityOrder := []string{"critical", "high", "medium", "info"}
	for _, sev := range severityOrder {
		count := bySev[sev]
		if count == 0 {
			continue
		}
		c := severityColor(sev)
		fmt.Printf("  %s%s%s: %d\n", c, strings.ToUpper(sev), colorReset, count)
	}

	// Per-checker summary
	fmt.Println("\nCheckers:")
	for _, res := range report.Results {
		icon := "✓"
		if len(res.Findings) > 0 {
			icon = "✗"
		}
		if res.Error != "" {
			icon = "!"
		}
		fmt.Printf("  %s %s — %d findings", icon, res.CheckerName, len(res.Findings))
		if res.Error != "" {
			fmt.Printf(" (error: %s)", res.Error)
		} else if res.Skipped {
			fmt.Printf(" (skipped: %s)", res.SkipReason)
		} else if len(res.Meta) > 0 {
			fmt.Printf(" | ")
			metaStr := []string{}
			for k, v := range res.Meta {
				if v != "" {
					metaStr = append(metaStr, fmt.Sprintf("%s=%s", k, v))
				}
			}
			fmt.Print(strings.Join(metaStr, " "))
		}
		fmt.Println()
	}

	// Detailed findings if verbose
	if verbose {
		fmt.Println("\nFindings details:")
		for _, res := range report.Results {
			if len(res.Findings) == 0 {
				continue
			}
			fmt.Printf("\n%s%s%s:\n", colorBold, res.CheckerName, colorReset)
			for _, f := range res.Findings {
				fmt.Printf("  %s[%s]%s %s\n",
					severityColor(string(f.Severity)), strings.ToUpper(string(f.Severity)), colorReset,
					f.Title)
				if f.Blocker {
					fmt.Printf("    %s⚠ BLOCKER%s\n", colorRed, colorReset)
				}
				fmt.Printf("    %s\n", f.Description)
				if f.Remediation != "" {
					fmt.Printf("    Remediation: %s\n", f.Remediation)
				}
				fmt.Println()
			}
		}
	}

	return nil
}

func printReportJSON(report *checker.Report) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func printReportCSV(report *checker.Report) error {
	w := csv.NewWriter(os.Stdout)
	w.Write([]string{"checker", "severity", "blocker", "title", "description", "remediation"})

	for _, res := range report.Results {
		for _, f := range res.Findings {
			w.Write([]string{
				res.CheckerName,
				string(f.Severity),
				strconv.FormatBool(f.Blocker),
				f.Title,
				f.Description,
				f.Remediation,
			})
		}
	}
	w.Flush()
	return w.Error()
}

// PrintDiff formats and prints a DiffResult.
func PrintDiff(format string, result *diff.Result) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	case "csv":
		return printDiffCSV(result)
	default:
		return printDiffTable(result)
	}
}

func printDiffTable(result *diff.Result) error {
	fmt.Printf("\n%s=== Post-Upgrade Verification ===%s\n", colorBold, colorReset)

	s := result.Summary
	if s.Improved {
		fmt.Printf("%s✓ VERIFIED%s — Upgrade successful\n", colorGreen, colorReset)
	} else if s.NewBlockers > 0 {
		fmt.Printf("%s✗ NEW BLOCKERS%s — %d issues appeared\n", colorRed, colorReset, s.NewBlockers)
	} else if s.UnchangedBlockers > 0 {
		fmt.Printf("%s⚠ UNCHANGED BLOCKERS%s — %d still present\n", colorYellow, colorReset, s.UnchangedBlockers)
	} else {
		fmt.Printf("%s○ NO CHANGES%s\n", colorGray, colorReset)
	}

	fmt.Printf("\nDelta:\n")
	fmt.Printf("  %s%d resolved%s | %s%d new%s | %d unchanged\n",
		colorGreen, s.ResolvedTotal, colorReset,
		colorRed, s.NewTotal, colorReset,
		len(result.Unchanged))

	if s.NewBlockers > 0 {
		fmt.Printf("\n%sNew blockers:%s\n", colorRed, colorReset)
		for _, f := range result.New {
			if f.Blocker {
				fmt.Printf("  + %s\n", f.Title)
			}
		}
	}

	if s.ResolvedTotal > 0 {
		fmt.Printf("\n%sResolved:%s\n", colorGreen, colorReset)
		for _, f := range result.Resolved {
			fmt.Printf("  − %s\n", f.Title)
		}
	}

	return nil
}

func printDiffCSV(result *diff.Result) error {
	w := csv.NewWriter(os.Stdout)
	w.Write([]string{"status", "checker", "title", "severity", "blocker"})

	for _, f := range result.New {
		w.Write([]string{"new", f.CheckerName, f.Title, string(f.Severity), strconv.FormatBool(f.Blocker)})
	}
	for _, f := range result.Resolved {
		w.Write([]string{"resolved", f.CheckerName, f.Title, string(f.Severity), strconv.FormatBool(f.Blocker)})
	}

	w.Flush()
	return w.Error()
}

// PrintVersions formats and prints a VersionsReport.
func PrintVersions(format string, vr *versions.Report) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(vr)
	case "csv":
		return printVersionsCSV(vr)
	default:
		return printVersionsTable(vr)
	}
}

func printVersionsTable(vr *versions.Report) error {
	fmt.Printf("\n%sTool Database Coverage:%s\n", colorBold, colorReset)

	for _, t := range vr.Tools {
		dbColor := colorGray
		if t.DBType == "cached" {
			dbColor = colorGreen
		} else if t.DBType == "embedded" {
			dbColor = colorBlue
		}

		fmt.Printf("  %s [%s%s%s] — up to k8s %s\n",
			t.Name,
			dbColor, t.DBType, colorReset,
			t.MaxK8s)

		if t.CachedAt != nil {
			fmt.Printf("      cached: %s\n", t.CachedAt)
		}
	}

	if len(vr.Warnings) > 0 {
		fmt.Printf("\n%sWarnings:%s\n", colorYellow, colorReset)
		for _, w := range vr.Warnings {
			fmt.Printf("  ⚠ %s: %s\n", w.Tool, w.Message)
		}
	}

	return nil
}

func printVersionsCSV(vr *versions.Report) error {
	w := csv.NewWriter(os.Stdout)
	w.Write([]string{"tool", "version", "db_type", "max_k8s", "cached_at"})

	for _, t := range vr.Tools {
		cachedAt := ""
		if t.CachedAt != nil {
			cachedAt = t.CachedAt.String()
		}
		w.Write([]string{t.Name, t.Version, t.DBType, t.MaxK8s, cachedAt})
	}

	w.Flush()
	return w.Error()
}

func severityColor(sev string) string {
	switch sev {
	case "critical":
		return colorRed
	case "high":
		return colorYellow
	case "medium":
		return colorBlue
	default:
		return colorGray
	}
}
