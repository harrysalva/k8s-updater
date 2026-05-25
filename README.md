# Upgrade Guardian

**Deterministic Kubernetes upgrade validation.** Given a live cluster and a target k8s version, runs 13 checkers in parallel and tells you — with zero hallucinations — whether the upgrade is safe.

```
upgrade-guardian-cli check --from 1.34 --to 1.35

=== Upgrade Check: 1.34 → 1.35 ===
⚠ BLOCKERS FOUND — upgrade cannot proceed

Findings by severity:
  CRITICAL: 1
  HIGH: 17
  MEDIUM: 6
  INFO: 2

Checkers:
  ✗ workloads-readiness — 18 findings | pdb_blockers=1 single_replica=16
  ✗ deprecated-apis — 0 findings | resources_scanned=572
  ✓ webhooks-health — 0 findings | validating=1 mutating=1
  ...
```

## What it validates

Each checker answers ONE upgrade-blocking question. Findings are produced by tools like Pluto, Nova, kubeconform, or by direct API queries — never by an LLM.

1. **deprecated-apis** — Are any live API versions removed/deprecated in the target version? (Pluto + dynamic scan)
2. **helm-cves** — Do Helm chart `kubeVersion` constraints accept the target? (Nova)
3. **crd-schemas** — Are CRD schemas compatible with the target? (kubeconform)
4. **control-plane** — Are `/healthz`, certs, and the API server itself healthy?
5. **etcd-health** — Are etcd endpoints reachable, no NOSPACE/CORRUPT alarms?
6. **node-health** — Node conditions, NPD, and kubelet version skew (Kubernetes ≤ 2 minors policy)
7. **provider-compatibility** — Is the CNI / EKS add-on / Kubespray inventory compatible?
8. **workloads-readiness** — Do PDBs allow draining? Single-replica workloads? Pods already broken?
9. **webhooks-health** — Are admission webhook CAs valid? Services reachable?
10. **capacity-headroom** — Can the cluster absorb a node drain without leaving pods Pending?
11. **preflight-dryrun** — EKS Insights, kubeadm plan guidance, Kubespray inventory drift
12. **karpenter-compatibility** — Does the installed Karpenter support the target k8s? (matrix verified upstream)
13. **istio-compatibility** — Does the installed Istio support the target k8s? Is it still under upstream support?

## Three ways to use it

| Interface | When to use |
|---|---|
| **Headlamp plugin** | Operators who want a polished UI with summary tiles, finding details, one-click NPD install, post-upgrade diff |
| **CLI** (`upgrade-guardian-cli`) | CI/CD integration, automated pipelines, headless servers, scripting |
| **REST API** (`localhost:8090`) | Direct integration with other tooling, custom dashboards |

The backend is the source of truth — all three frontends call the same `/api/v1/*` endpoints.

## Install

Download the tarball for your OS/arch from `dist/` (after `make release`) and run the installer:

```bash
tar -xzf upgrade-guardian-<version>-<os>-<arch>.tar.gz
cd upgrade-guardian-<version>-<os>-<arch>
./scripts/install.sh
```

This puts both binaries in `/usr/local/bin/` and the Headlamp plugin in `~/.config/Headlamp/plugins/upgrade-guardian/`. A user-level systemd unit (Linux) or LaunchAgent (macOS) keeps the backend running.

Full instructions: [`docs/INSTALL.md`](docs/INSTALL.md).

## Quick start (from source)

```bash
make build                                          # builds bin/upgrade-guardian + bin/upgrade-guardian-cli
./bin/upgrade-guardian --kubeconfig ~/.kube/config &
./bin/upgrade-guardian-cli check --from 1.34 --to 1.35
```

## Documentation

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — system architecture, checker details, data model, API
- [`docs/INSTALL.md`](docs/INSTALL.md) — installation, RBAC requirements, troubleshooting
- [`docs/CLI.md`](docs/CLI.md) — CLI commands, output formats, CI/CD examples
- [`docs/adr/`](docs/adr/) — architecture decision records (why each tool was chosen)

## Cardinal principles

1. **No hallucinations.** Findings come from deterministic tools, never from LLMs.
2. **Zero false positives.** Better to skip an uncertain check than to cry wolf.
3. **Verified matrices.** Third-party compatibility matrices (Karpenter, Istio) cite an upstream URL and a `LAST VERIFIED` date.
4. **Refresh-only updates.** Out-of-date matrices get refreshed manually via WebFetch; no automated HTML scraping.

## Development

```bash
make build           # build server + CLI
make test            # run all unit tests
go test ./internal/checks/karpenter ./internal/checks/istio   # only matrix tests
make release         # cross-compile tarballs into dist/
make plugin-build    # build + deploy Headlamp plugin to ~/.config/Headlamp/plugins/
```

The Headlamp plugin uses Vite directly (`plugin/build.mjs`) because the official `headlamp-plugin` CLI is broken on Node v26.

## Status

| Component | Status |
|---|---|
| Backend | ✅ Production-ready, 13 checkers active |
| CLI | ✅ Production-ready |
| Headlamp plugin | ✅ Production-ready |
| Packaging | ✅ Multi-arch tarballs (linux/darwin × amd64/arm64) |
| Tests | ✅ Unit tests for matrix logic; integration tests pending |
| RAG (LLM explanations) | ⚠️ Interface defined; `NoopRAG` placeholder; `SQLiteRAG` pending |
| EKS Insights via `preflight-dryrun` | ✅ Implemented (requires AWS creds + `X-AWS-Region`) |
| kubeadm `upgrade plan` automation | ⚠️ Informational only (no SSH executor) |

## License

Internal tool — see internal repository for licensing.
# k8s-updater
