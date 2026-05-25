package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"upgrade-guardian/internal/checker"
)

// Context holds shared CLI state.
type Context struct {
	Client  *Client
	Format  string
	Verbose bool
}

// CmdCheck runs the upgrade readiness checks.
// Usage: upgrade-guardian-cli check --from 1.34 --to 1.35 [--context kind-cluster]
func CmdCheck(ctx *Context, args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	from := fs.String("from", "", "Current Kubernetes version (required)")
	to := fs.String("to", "", "Target Kubernetes version (required)")
	context := fs.String("context", "", "Kubernetes context name (optional)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `upgrade-guardian-cli check — Run upgrade readiness checks

Usage:
  upgrade-guardian-cli check --from <version> --to <version> [flags]

Flags:
  -from string     Current version (required)
  -to string       Target version (required)
  -context string  Kubernetes context (optional)
  -h               Show this help
`)
	}

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *from == "" || *to == "" {
		fs.Usage()
		os.Exit(1)
	}

	report, err := ctx.Client.RunChecks(*from, *to, *context)
	if err != nil {
		log.Fatal(err)
	}

	if err := PrintReport(ctx.Format, report, ctx.Verbose); err != nil {
		log.Fatal(err)
	}

	// Exit with code reflecting blocker status
	if report.Blocker {
		os.Exit(1)
	}
}

// CmdCluster shows cluster information.
// Usage: upgrade-guardian-cli cluster [--context kind-cluster]
func CmdCluster(ctx *Context, args []string) {
	fs := flag.NewFlagSet("cluster", flag.ExitOnError)
	context := fs.String("context", "", "Kubernetes context name (optional)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `upgrade-guardian-cli cluster — Show cluster information

Usage:
  upgrade-guardian-cli cluster [flags]

Flags:
  -context string  Kubernetes context (optional)
  -h               Show this help
`)
	}

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	info, err := ctx.Client.GetCluster(*context)
	if err != nil {
		log.Fatal(err)
	}

	switch ctx.Format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(info)
	default:
		fmt.Println("\nCluster Info:")
		for k, v := range info {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
}

// CmdVersions shows tool database coverage.
// Usage: upgrade-guardian-cli versions [--target 1.35]
func CmdVersions(ctx *Context, args []string) {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	target := fs.String("target", "", "Target version for coverage check (optional)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `upgrade-guardian-cli versions — Show tool database versions

Usage:
  upgrade-guardian-cli versions [flags]

Flags:
  -target string  Target version for coverage validation (optional)
  -h              Show this help
`)
	}

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	vr, err := ctx.Client.GetVersions(*target)
	if err != nil {
		log.Fatal(err)
	}

	if err := PrintVersions(ctx.Format, vr); err != nil {
		log.Fatal(err)
	}
}

// CmdPostCheck compares a pre-upgrade report with a fresh check.
// Usage: upgrade-guardian-cli postcheck --pre-report <file> --from 1.34 --to 1.35 [--context kind-cluster]
func CmdPostCheck(ctx *Context, args []string) {
	fs := flag.NewFlagSet("postcheck", flag.ExitOnError)
	preReportFile := fs.String("pre-report", "", "Path to pre-upgrade report JSON (required)")
	from := fs.String("from", "", "Current/pre-upgrade version (required)")
	to := fs.String("to", "", "Target version (required)")
	context := fs.String("context", "", "Kubernetes context (optional)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `upgrade-guardian-cli postcheck — Verify post-upgrade state

Usage:
  upgrade-guardian-cli postcheck --pre-report <file> --from <version> --to <version> [flags]

Flags:
  -pre-report string  Path to pre-upgrade report JSON (required)
  -from string        Version before upgrade (required)
  -to string          Version after upgrade (required)
  -context string     Kubernetes context (optional)
  -h                  Show this help
`)
	}

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *preReportFile == "" || *from == "" || *to == "" {
		fs.Usage()
		os.Exit(1)
	}

	// Load pre-report from file
	data, err := os.ReadFile(*preReportFile)
	if err != nil {
		log.Fatalf("Cannot read pre-report: %v", err)
	}

	var preReport checker.Report
	if err := json.Unmarshal(data, &preReport); err != nil {
		log.Fatalf("Cannot parse pre-report: %v", err)
	}

	result, err := ctx.Client.PostCheck(&preReport, *from, *to, *context)
	if err != nil {
		log.Fatal(err)
	}

	if err := PrintDiff(ctx.Format, result); err != nil {
		log.Fatal(err)
	}

	// Exit with code reflecting verification status
	if !result.Summary.Improved || result.Summary.NewBlockers > 0 {
		os.Exit(1)
	}
}
