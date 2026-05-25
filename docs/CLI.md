# upgrade-guardian-cli — Command-Line Interface

A standalone CLI tool to validate Kubernetes upgrades without needing the Headlamp UI. Perfect for:

- Automated CI/CD validation in upgrade pipelines
- Integration with other tooling
- Quick validation from SSH or cron jobs
- Scripting and report generation

## Installation

The CLI is included in each release tarball at `bin/upgrade-guardian-cli`. Install it alongside the backend:

```bash
tar -xzf upgrade-guardian-<version>-<os>-<arch>.tar.gz
cd upgrade-guardian-<version>-<os>-<arch>
./scripts/install.sh
```

This copies both `upgrade-guardian` (backend) and `upgrade-guardian-cli` to `/usr/local/bin/`.

## Quickstart

```bash
# Backend must be running on :8090
upgrade-guardian-cli check --from 1.34 --to 1.35

# Show cluster info
upgrade-guardian-cli cluster

# Show tool database coverage
upgrade-guardian-cli versions

# Verify post-upgrade
upgrade-guardian-cli postcheck --pre-report=/tmp/pre.json --from 1.34 --to 1.35
```

## Commands

### check — Run upgrade readiness checks

```bash
upgrade-guardian-cli check --from <version> --to <version> [flags]
```

**Flags:**
- `--from VERSION` — Current Kubernetes version (required)
- `--to VERSION` — Target Kubernetes version (required)
- `--context NAME` — Kubernetes context (optional; defaults to current)

**Output:** Colored table with summary + detailed findings (if `--v`)

**Exit code:** 0 if safe (no blockers), 1 if blockers found

**Example:**
```bash
upgrade-guardian-cli check --from 1.34 --to 1.35
upgrade-guardian-cli check --from 1.34 --to 1.35 --context prod-cluster -v
```

### cluster — Show cluster information

```bash
upgrade-guardian-cli cluster [flags]
```

**Flags:**
- `--context NAME` — Kubernetes context (optional)

**Output:** Cluster type, version, platform

**Example:**
```bash
upgrade-guardian-cli cluster
upgrade-guardian-cli cluster --context staging
```

### versions — Show tool database coverage

```bash
upgrade-guardian-cli versions [flags]
```

**Flags:**
- `--target VERSION` — Target version (optional; checks coverage)

**Output:** Each tool (Pluto, Nova, Kubeconform) with max k8s version and cache status

**Example:**
```bash
upgrade-guardian-cli versions
upgrade-guardian-cli versions --target 1.35
```

### postcheck — Verify post-upgrade state

Compare a pre-upgrade report with a fresh check after the upgrade completes.

```bash
upgrade-guardian-cli postcheck --pre-report <file> --from <version> --to <version> [flags]
```

**Flags:**
- `--pre-report FILE` — Path to pre-upgrade report JSON (required)
- `--from VERSION` — Version before upgrade (required)
- `--to VERSION` — Version after upgrade (required)
- `--context NAME` — Kubernetes context (optional)

**Output:** Delta (resolved/new/unchanged findings), verdict (improved/failed)

**Exit code:** 0 if improved + no new blockers, 1 otherwise

**Example:**
```bash
# Save pre-upgrade report
upgrade-guardian-cli -format json check --from 1.34 --to 1.35 > /tmp/pre.json

# ... upgrade the cluster ...

# Verify
upgrade-guardian-cli postcheck --pre-report /tmp/pre.json --from 1.34 --to 1.35
```

## Global Flags

All commands support these flags (placed before the subcommand):

```bash
upgrade-guardian-cli [flags] <command> [command-flags]
```

- `-server URL` — Backend server address (default: `http://localhost:8090`)
- `-format FORMAT` — Output format: `table`, `json`, `csv` (default: `table`)
- `-v` — Verbose output (shows detailed finding descriptions)

## Output Formats

### Table (default)

Human-readable, colored output with summaries and details:

```
=== Upgrade Check: 1.34 → 1.35 ===
Cluster type: upstream | Timestamp: 2026-05-24 20:56:00

⚠ BLOCKERS FOUND — upgrade cannot proceed

Findings by severity:
  CRITICAL: 1
  HIGH: 17
  MEDIUM: 6
  INFO: 2

Checkers:
  ✗ workloads-readiness — 18 findings | deployments_checked=19 pdb_blockers=1 ...
  ✗ deprecated-apis — 0 findings | resources_scanned=572 ...
  ...
```

### JSON

Complete machine-readable report (all fields):

```bash
upgrade-guardian-cli -format json check --from 1.34 --to 1.35 | jq .
```

Useful for scripting, CI integration, or storage.

### CSV

Tabular format for spreadsheets:

```bash
upgrade-guardian-cli -format csv check --from 1.34 --to 1.35 > findings.csv
```

Columns: `checker`, `severity`, `blocker`, `title`, `description`, `remediation`

## Exit Codes

- **0** — Success (no blockers for `check`; improved for `postcheck`)
- **1** — Failure (blockers found or new blockers post-upgrade)
- **2** — Invalid usage (missing required flags)

## Examples

### CI/CD Integration

```bash
#!/bin/bash
set -e

# Start backend if not already running
upgrade-guardian &
sleep 2

# Pre-upgrade validation
echo "→ Pre-upgrade checks (1.34 → 1.35)..."
upgrade-guardian-cli check --from 1.34 --to 1.35 -v || {
  echo "Blockers found. Aborting upgrade."
  exit 1
}

# Save pre-report for later
upgrade-guardian-cli -format json check --from 1.34 --to 1.35 > /tmp/pre.json

# ... perform upgrade ...

# Post-upgrade verification
echo "→ Post-upgrade verification..."
upgrade-guardian-cli postcheck --pre-report /tmp/pre.json --from 1.34 --to 1.35

echo "✓ Upgrade validated successfully"
```

### Generate upgrade report

```bash
# Create a detailed report in both formats
upgrade-guardian-cli -format table check --from 1.34 --to 1.35 -v > upgrade_report.txt
upgrade-guardian-cli -format json check --from 1.34 --to 1.35 > upgrade_report.json
upgrade-guardian-cli -format csv check --from 1.34 --to 1.35 > upgrade_report.csv
```

### Monitor multiple clusters

```bash
for cluster in prod staging dev; do
  echo "=== $cluster ==="
  upgrade-guardian-cli check --from 1.34 --to 1.35 --context $cluster
done
```

## Troubleshooting

| Issue | Solution |
|---|---|
| `Cannot reach backend` | Start the backend: `upgrade-guardian --kubeconfig ~/.kube/config` |
| `flag provided but not defined` | Global flags go BEFORE the subcommand: `upgrade-guardian-cli -v check --from 1.34` |
| JSON parse errors | Use `-format json` to get the full Report structure for debugging |
| Exit code always 0 | Use `echo $?` to check the exit code |
| Colors not showing | Some terminals/pipes disable ANSI codes. Use `-format json` or `-format csv` instead |

## Backend Compatibility

The CLI requires a compatible upgrade-guardian backend running on the same machine (or reachable via `-server`). The backend and CLI are always released together and are compatible.

If upgrading the CLI to a newer version, also upgrade the backend:

```bash
# Download latest release
curl -L https://github.com/your-org/upgrade-guardian/releases/download/v0.2.0/upgrade-guardian-0.2.0-linux-amd64.tar.gz | tar xz
cd upgrade-guardian-0.2.0-linux-amd64
./scripts/install.sh --no-systemd  # keep systemd unit as-is
systemctl --user restart upgrade-guardian
```
