# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

**Upgrade Guardian** — a deterministic Kubernetes upgrade validation tool. It scans a live cluster and determines whether upgrading to a target Kubernetes minor version is safe. No LLMs in the detection path, ever.

Three interfaces (Headlamp plugin, CLI, REST API) all call the same Go backend on `:8090`.

## Commands

```bash
# Build
make build              # bin/upgrade-guardian + bin/upgrade-guardian-cli
make plugin-build       # compile Headlamp plugin → ~/.config/Headlamp/plugins/upgrade-guardian/

# Run locally
make dev                # backend with debug logging on :8090

# Test
make test               # go test -race -count=1 ./...
go test ./internal/checks/karpenter ./internal/checks/istio  # just the compatibility matrix tests

# Lint
make lint               # golangci-lint run ./...

# Update embedded tool databases (Pluto, Nova, kubeconform) — no rebuild needed for Pluto at runtime
make update-deps

# Release (cross-platform tarballs into dist/)
make release
VERSION=0.2.0 make release
```

## Architecture

```
cmd/server/main.go      → HTTP backend (:8090)
cmd/cli/main.go         → standalone CLI (calls backend via HTTP)
internal/
  checker/              → Checker interface + shared types (Finding, Report, CheckConfig)
  engine/               → runs all checkers concurrently; single registry in New()
  detector/             → auto-detects cluster type (eks / kubespray / upstream)
  api/                  → HTTP handlers; one handler struct wraps engine + rag
  checks/<name>/        → one package per checker (17 total)
  diff/                 → pre/post upgrade report diff
  versions/             → Pluto DB management (live update + module-cache fallback)
  rag/                  → RAG interface (NoopRAG is the only impl; SQLiteRAG is pending)
  npd/                  → node-problem-detector installer
  cli/                  → HTTP client + output formatters for the CLI binary
plugin/                 → React + TypeScript Headlamp plugin (MUI, built with build.mjs)
```

### Core data flow

1. `engine.Engine.Run()` calls `detector.Detect()` → sets `ClusterType` on `CheckConfig`
2. Each checker's `Supports(ct ClusterType)` gates whether it runs
3. All applicable checkers run in goroutines concurrently
4. Results are merged; matrix staleness findings are injected (see `engine/matrix_staleness.go`)
5. `Report.Blocker` is `true` if any `Finding.Blocker == true`

### Key types (`internal/checker/types.go`)

- `CheckConfig` — passed to every `Check()` call; contains `KubeClient`, `RestConfig`, `CurrentVersion`, `TargetVersion`, and optional `EKSConfig` / `KubesprayConfig`
- `Finding` — a single validated problem: `ID`, `Severity`, `Blocker`, `Title`, `Description`, `Remediation`, `DocsURL`, `Source`, optional `Resource`
- `CheckResult` — output of one checker: findings + meta key/value map + error + skipped flag
- `Report` — top-level response: all `CheckResult`s + `Blocker` bool + `ClusterType` + versions + timestamp

### REST API endpoints (`internal/api/handlers.go`)

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/check?from=X&to=Y[&context=C]` | Run all checkers |
| GET | `/api/v1/cluster[?context=C]` | Detect cluster type + server version |
| POST | `/api/v1/postcheck` | Re-run checkers and diff against pre-report |
| POST | `/api/v1/rag/query` | Translate findings (NoopRAG today) |
| POST | `/api/v1/npd/install` | Deploy node-problem-detector |
| GET | `/api/v1/versions[?target=X]` | Bundled tool versions |
| POST | `/api/v1/versions/update` | Refresh Pluto DB from GitHub at runtime |

Context is passed as a query param (`?context=<kubeconfig-context-name>`); EKS/Kubespray config via `X-Cluster-Name`, `X-AWS-Region`, `X-Kubespray-Inventory`, `X-Kubespray-GroupVars` headers.

## Adding a new checker

1. Create `internal/checks/<name>/checker.go` implementing `checker.Checker` (three methods: `Name()`, `Supports()`, `Check()`).
2. Register it in `internal/engine/engine.go` → `New()` → `checkers` slice.
3. Add label + meta formatters in `plugin/src/components/CheckCard.tsx`.
4. Update `docs/ARCHITECTURE.md`.
5. If the checker uses a hardcoded compatibility matrix, include `SOURCE OF TRUTH: <url>` and `LAST VERIFIED: YYYY-MM-DD` comments, and write a `TestMatrixIntegrity` test.

## Compatibility matrices (Karpenter, Istio, EKS add-ons, IRSA)

The matrices in `internal/checks/{karpenter,istio,eks-addons,irsa}/checker.go` are **hardcoded** with `LAST VERIFIED` dates. Refresh process:

1. Fetch upstream docs (linked in `SOURCE OF TRUTH` comments)
2. Edit `compatibilityMatrix` in the relevant checker
3. Update the `LAST VERIFIED` date
4. Run `go test ./internal/checks/<name>/`

Staleness warnings (>180 days since last verification) are auto-injected by `engine/matrix_staleness.go`.

## Pluto database updates

Pluto's deprecated-API database is **embedded at build time** via the Go module. Two ways to refresh:

- **Runtime (no rebuild):** `POST /api/v1/versions/update` fetches latest `versions.yaml` from GitHub and caches it locally.
- **Build time:** `make update-deps` → `go get github.com/fairwindsops/pluto/v5@latest` + rebuild.

The runtime path has a fallback to the module cache for airgapped clusters.

## Plugin development

The Headlamp plugin lives in `plugin/` and is standard React + TypeScript + MUI. Build:

```bash
make plugin-build   # build + copy to ~/.config/Headlamp/plugins/upgrade-guardian/
```

The plugin talks exclusively to the Go backend — no validation logic lives in the frontend.

## Cardinal constraints

- **No LLMs in the detection path.** `Check()` implementations must be fully deterministic.
- **Zero false positives over completeness.** Skip an uncertain check rather than emit a noisy finding.
- **Backend is the single source of truth.** UI, CLI, and direct API callers all hit the same endpoints.
- **Each failed checker is isolated** — a panic or error in one checker must not abort others (the engine handles this via goroutines + error field on `CheckResult`).
