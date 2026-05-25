package main

import (
	"flag"
	"fmt"
	"os"

	"upgrade-guardian/internal/cli"
)

func main() {
	// Global flags first (before subcommand)
	globalFS := flag.NewFlagSet("", flag.ContinueOnError)
	serverAddr := globalFS.String("server", "http://localhost:8090", "API server address")
	format := globalFS.String("format", "table", "Output format: table, json, csv")
	verbose := globalFS.Bool("v", false, "Verbose output")

	globalFS.Usage = func() {} // suppress default usage for global flags
	globalFS.Parse(os.Args[1:])

	// Get remaining args (which should start with subcommand)
	remainingArgs := globalFS.Args()
	if len(remainingArgs) == 0 {
		printHelp()
		os.Exit(0)
	}

	cmd := remainingArgs[0]
	args := remainingArgs[1:]

	client := cli.NewClient(*serverAddr)
	ctx := &cli.Context{
		Client:  client,
		Format:  *format,
		Verbose: *verbose,
	}

	switch cmd {
	case "check":
		cli.CmdCheck(ctx, args)
	case "cluster":
		cli.CmdCluster(ctx, args)
	case "versions":
		cli.CmdVersions(ctx, args)
	case "postcheck":
		cli.CmdPostCheck(ctx, args)
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Fprintf(os.Stderr, `upgrade-guardian-cli — Kubernetes upgrade validation from the CLI

Usage:
  upgrade-guardian-cli [flags] <command> [command-flags]

Commands:
  check      Run upgrade readiness checks
  cluster    Show cluster info
  versions   Show tool database coverage
  postcheck  Verify post-upgrade (compare pre-report vs current state)
  help       Show this help

Global flags:
  -server string   API server address (default "http://localhost:8090")
  -format string   Output format: table, json, csv (default "table")
  -v               Verbose output

Examples:
  upgrade-guardian-cli check --from 1.34 --to 1.35
  upgrade-guardian-cli check --from 1.34 --to 1.35 --context kind-cluster -v
  upgrade-guardian-cli cluster --context kind-cluster
  upgrade-guardian-cli versions --target 1.35
  upgrade-guardian-cli postcheck --pre-report=/tmp/pre.json --from 1.34 --to 1.35

For command-specific help:
  upgrade-guardian-cli <command> -h
`)
}
