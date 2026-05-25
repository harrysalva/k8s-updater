# Upgrade Guardian — Architecture

## Propósito

Upgrade Guardian es una herramienta de validación determinista para upgrades de Kubernetes. Dado un clúster live y un par de versiones (`from` → `to`), ejecuta 13 checkers en paralelo, cada uno respaldado por herramientas de análisis estático (Pluto, Nova, kubeconform), llamadas directas a la API de Kubernetes, o matrices de compatibilidad verificadas contra documentación upstream. Devuelve un informe que indica si el upgrade está bloqueado o es seguro.

No es un producto comercial. Es una **herramienta interna de supervivencia operativa**: su único objetivo es prevenir desastres durante upgrades y garantizar la paz mental del operador.

**Principio cardinal**: ningún componente del sistema inventa hallazgos. Los LLMs solo traducen y priorizan findings ya validados por checkers deterministas.

---

## Mandatos del sistema (innegociables)

Estos mandatos derivan del diseño original y no pueden ser modificados sin un ADR explícito:

1. **Detección 100% determinista**: NUNCA usar LLMs para detectar problemas. Solo para traducir/priorizar hallazgos ya validados. → [ADR-0001](adr/0001-deterministic-only-detection.md)
2. **Scope acotado**: NUNCA añadir Polaris, Popeye, Kubent ni herramientas de best practices. Solo bloqueantes de upgrade. → [ADR-0002](adr/0002-stack-tecnologico.md)
3. **RAG con fuentes verificadas**: Solo `docs.aws.amazon.com/eks`, `kubernetes.io`, release notes oficiales de CNI/CSI. Prohibido blogs, Medium, Reddit, issues sin triage.
4. **Metadata RAG obligatoria**: cada chunk DEBE tener `provider`, `version_range`, `source_url`. El retrieval SIEMPRE filtra por `provider` antes de buscar.
5. **Kubespray via inventory**: las versiones se leen de `group_vars/`, NUNCA de `kubectl`. Los fixes son vía Ansible.
6. **EKS via AWS SDK**: usar `aws-sdk-go-v2/service/eks` para add-ons gestionados. No asumir comportamiento upstream.
7. **Badge visual de clúster**: la UI SIEMPRE muestra el tipo de clúster (EKS/Upstream/Kubespray) junto a cada hallazgo.
8. **Cero falsos positivos**: mejor omitir un check dudoso que generar ruido.
9. **Matrices verificadas**: las matrices de compatibilidad de third-party (Karpenter, Istio) llevan `LAST VERIFIED` con fecha y URL upstream. Se refrescan manualmente vía WebFetch.

---

## Estado actual — 13 checkers

| # | Pregunta | Checker | Estado |
|---|---|---|---|
| 1 | ¿Detecta APIs removidas/deprecated? | `deprecated-apis` (Pluto) | ✅ |
| 2 | ¿Detecta charts Helm incompatibles? | `helm-cves` (Nova) | ✅ |
| 3 | ¿Valida esquemas CRD contra versión objetivo? | `crd-schemas` (kubeconform) | ✅ |
| 4 | ¿Valida salud control plane y certs? | `control-plane` | ✅ |
| 5 | ¿Verifica integridad etcd? | `etcd-health` | ✅ (skip en EKS) |
| 6 | ¿Confirma nodos sin problemas + kubelet skew? | `node-health` (NPD) | ✅ |
| 7 | ¿Valida compatibilidad CNI/IAM del provider? | `provider-compatibility` | ✅ |
| 8 | ¿PDBs y workloads sobreviven un drain? | `workloads-readiness` | ✅ |
| 9 | ¿Webhooks tienen CA válida y son alcanzables? | `webhooks-health` | ✅ |
| 10 | ¿Hay headroom de CPU/memoria para drain? | `capacity-headroom` | ✅ |
| 11 | ¿Dry-run de la operación de upgrade pasa? | `preflight-dryrun` | ✅ (EKS via Insights API, kubeadm/Kubespray informativo) |
| 12 | ¿Karpenter soporta el target k8s? | `karpenter-compatibility` | ✅ |
| 13 | ¿Istio soporta el target k8s? | `istio-compatibility` | ✅ |

---

## Stack

| Capa | Tecnología |
|---|---|
| Backend | Go 1.26, `net/http` con mux patterns Go 1.22 |
| Kubernetes client | k8s.io/client-go v0.35.4 |
| API deprecated | Pluto v5.24.0 (base embebida + cache runtime + fallback al Go module cache) |
| Helm compatibility | Nova (fairwindsops) |
| Schema validation | kubeconform v0.7.0 (schemas en runtime desde internet) |
| etcd health | etcd client v3.6.11 |
| EKS add-ons + Insights | AWS SDK v2 (`config` + `service/eks`) |
| Matrices Karpenter/Istio | Estáticas verificadas contra upstream docs |
| RAG (futuro) | SQLite + sqlite-vec + bge-m3 + Qwen2.5-Coder-32B |
| Frontend | React 18 + TypeScript + MUI v5 |
| Plugin host | Headlamp 0.42.0 |
| Build plugin | Vite 6 via `plugin/build.mjs` |
| CLI | Go binario standalone que envuelve la API del backend |

---

## Estructura de directorios

```
k8s-updater/
├── cmd/
│   ├── server/main.go              # Backend HTTP server
│   └── cli/main.go                 # CLI tool (upgrade-guardian-cli)
├── internal/
│   ├── checker/
│   │   ├── interface.go            # Checker interface
│   │   └── types.go                # ClusterType, Severity, Finding, CheckConfig, CheckResult, Report
│   ├── detector/detector.go        # Detecta tipo de clúster (EKS / Kubespray / upstream)
│   ├── engine/engine.go            # Orquesta los 13 checkers en goroutines concurrentes
│   ├── api/
│   │   ├── server.go               # net/http server con CORS middleware
│   │   └── handlers.go             # RunChecks, GetCluster, PostCheck, GetVersions, UpdateVersions, RAGQuery, InstallNPD
│   ├── diff/diff.go                # Diff pre/post upgrade reports
│   ├── versions/
│   │   ├── versions.go             # Reporta versiones de herramientas y cobertura de bases
│   │   └── updater.go              # Refresca Pluto desde GitHub o module cache fallback
│   ├── npd/installer.go            # Instala node-problem-detector
│   ├── cli/                        # CLI: client.go, format.go, commands.go
│   ├── rag/rag.go                  # Interface RAG + NoopRAG (SQLiteRAG pendiente)
│   └── checks/
│       ├── deprecated/checker.go   # Pluto + dynamic client (escanea todos los objetos live)
│       ├── helm/checker.go         # Nova: releases incompatibles
│       ├── crd/checker.go          # kubeconform
│       ├── controlplane/checker.go # /healthz + ServerVersion + cert expiry
│       ├── etcd/checker.go         # etcd health + alarmas
│       ├── nodes/checker.go        # NodeConditions + NPD + kubelet skew
│       ├── provider/checker.go     # CNI/IAM compat
│       ├── workloads/checker.go    # PDBs, single-replica, broken pods, missing probes
│       ├── webhooks/checker.go     # Admission webhook CA, reachability, timeouts
│       ├── capacity/checker.go     # Drain simulation, headroom, ResourceQuota
│       ├── preflight/              # EKS Insights, kubeadm guidance, Kubespray inventory
│       │   ├── checker.go
│       │   ├── eks.go
│       │   ├── kubeadm.go
│       │   └── kubespray.go
│       ├── karpenter/checker.go    # Compatibility matrix (verified)
│       └── istio/checker.go        # Compatibility matrix + upstream support status
├── plugin/
│   ├── src/
│   │   ├── index.tsx               # registerSidebarEntry + registerRoute
│   │   ├── api/client.ts           # HTTP client (Report, DiffResult, VersionsReport)
│   │   ├── types.ts                # Report, Finding, CheckResult con Meta
│   │   └── components/
│   │       ├── Dashboard.tsx       # Version picker + summary tiles + resultados
│   │       ├── CheckCard.tsx       # Card con meta chips + grid de findings
│   │       ├── ClusterBadge.tsx    # Chip por tipo de clúster
│   │       ├── DatabaseStatus.tsx  # Estado de Pluto + botón "Update databases"
│   │       └── PostUpgradeDiff.tsx # Verificación post-upgrade con diff visual
│   ├── build.mjs                   # Build directo con Vite
│   └── package.json
├── scripts/
│   ├── setup.sh                    # go get de dependencias
│   ├── release.sh                  # Cross-compile para linux/darwin × amd64/arm64
│   ├── install.sh                  # Instalador local (binarios + plugin + systemd/launchd)
│   ├── systemd/upgrade-guardian.service
│   └── launchd/com.upgrade-guardian.plist
├── Makefile                        # build, test, lint, release, plugin-build, update-deps
└── docs/
    ├── ARCHITECTURE.md             # Este archivo
    ├── INSTALL.md                  # Guía de instalación
    ├── CLI.md                      # Documentación del CLI
    └── adr/
        ├── 0001-deterministic-only-detection.md
        └── 0002-stack-tecnologico.md
```

---

## Flujo de ejecución

```
Browser (Headlamp :4466)               CLI (upgrade-guardian-cli)
    │                                       │
    │  GET  /api/v1/cluster?context=...     │
    │  GET  /api/v1/check?from=&to=&context=│
    │  POST /api/v1/postcheck (diff)        │
    │  GET  /api/v1/versions?target=        │
    │  POST /api/v1/versions/update         │
    ▼                                       ▼
Go HTTP Server (:8090)
    │
    ├── detector.Detect()
    │   └── ClusterType: eks | kubespray | upstream | unknown
    │
    └── engine.Run(cfg)
        │
        ├─[goroutine]─ deprecated.Check()
        ├─[goroutine]─ helm.Check()
        ├─[goroutine]─ crd.Check()
        ├─[goroutine]─ controlplane.Check()
        ├─[goroutine]─ etcd.Check()
        ├─[goroutine]─ nodes.Check()
        ├─[goroutine]─ provider.Check()
        ├─[goroutine]─ workloads.Check()
        ├─[goroutine]─ webhooks.Check()
        ├─[goroutine]─ capacity.Check()
        ├─[goroutine]─ preflight.Check()
        ├─[goroutine]─ karpenter.Check()
        └─[goroutine]─ istio.Check()
                │
                ▼
           Report { blocker: bool, results: []CheckResult }
                │     (Meta map[string]string por checker)
                ▼
    Plugin React (Headlamp) o CLI
```

---

## Detección de tipo de clúster

| Señal | ClusterType |
|---|---|
| Label `eks.amazonaws.com/nodegroup` presente | `eks` |
| ConfigMap `kube-system/aws-auth` existe | `eks` |
| Label `kubespray.kubernetes.io/managed` | `kubespray` |
| DaemonSet `kubespray-nodelocaldns` existe | `kubespray` |
| Ninguna señal | `upstream` |

---

## Checkers

Todos implementan:

```go
type Checker interface {
    Name()     string
    Supports(ct ClusterType) bool
    Check(ctx context.Context, cfg *CheckConfig) ([]Finding, map[string]string, error)
}
```

El tercer valor de retorno (`map[string]string`) es la **metadata de ejecución** mostrada en la UI como chips informativos.

### 1. `deprecated-apis`

**Librería**: `github.com/fairwindsops/pluto/v5`

Escanea **todos** los objetos del clúster con `dynamic.Interface` + `discovery.ServerPreferredResources()`. Lee `apiVersion` del objeto live (no de annotations). Compara contra base Pluto.

**Carga híbrida**: Pluto usa `versionsmgr.PlutoContent(plutoversionsfile.Content())` que devuelve la base cacheada si existe, sino la embebida.

**Meta**: `resources_scanned`, `kinds_checked`, `namespaces_checked`, `removed`, `deprecated`.

### 2. `helm-cves`

**Librería**: `github.com/fairwindsops/nova`

Inyecta `kubernetes.Interface` en Nova directamente. Verifica `kubeVersion` de cada chart con `Masterminds/semver`.

**Meta**: `releases_scanned`, `with_constraints`, `incompatible`.

### 3. `crd-schemas`

**Librería**: `github.com/yannh/kubeconform`

Lista CRDs vía `dynamic.Interface` y valida contra schemas online. **Medium** cuando el schema no está disponible (típico de Dapr, Argo); **Critical** cuando es inválido confirmado.

**Meta**: `crds_validated`.

### 4. `control-plane`

`/healthz`, `/readyz`, `/livez` con `crypto/tls`. `Discovery().ServerVersion()`. `tls.Dial` para cert expiry con CN.

**Meta**: `api_version`, `endpoints_checked`.

### 5. `etcd-health`

**Librería**: `go.etcd.io/etcd/client/v3`

Lee endpoints del pod estático `kube-apiserver`. Carga certs de `/etc/kubernetes/pki/etcd/`. Si no existen (Kind), finding medium con comando manual. **Skipped** en EKS.

**Meta**: `endpoints_checked` o `status: tls_unavailable`.

### 6. `node-health`

Itera nodos y evalúa:
1. Kubelet conditions: `DiskPressure`, `NetworkUnavailable` (blocker), `MemoryPressure`, `PIDPressure`.
2. NPD conditions: `KernelDeadlock`, `ReadonlyFilesystem`, etc.
3. `NodeReady`: NotReady es blocker.
4. **Kubelet version skew**: > 2 minors del target → blocker (política oficial).

Si NPD no está desplegado, finding medium con botón one-click de instalación.

**Meta**: `nodes_checked`, `control_plane_nodes`, `worker_nodes`.

### 7. `provider-compatibility`

Tres rutas según clúster:

- **Upstream**: detecta CNI por DaemonSet name. Weave Net (EOL 2023) siempre blocker.
- **EKS**: `ListAddons` + `DescribeAddonVersions`.
- **Kubespray**: parsea `group_vars/`.

**Meta**: `cni` o `addons_checked`.

### 8. `workloads-readiness`

Valida que los workloads sobrevivan un node drain:
- **PDB blockers**: `maxUnavailable: 0` o `minAvailable >= replicas` → critical+blocker.
- **Single-replica**: deployments/statefulsets en namespaces no-system con `replicas: 1` → high.
- **Missing probes**: deployments >= 2 réplicas sin `readinessProbe` → medium.
- **Broken pods**: pods en `Pending(Unschedulable)`, `CrashLoopBackOff`, `ImagePullBackOff` → high+blocker.

**Meta**: `pdbs_checked`, `pdb_blockers`, `deployments_checked`, `statefulsets_checked`, `single_replica`, `missing_probes`, `broken_pods`.

### 9. `webhooks-health`

Audita Validating y Mutating webhooks:
- **CA expirada o < 7 días** → critical+blocker. **CA < 30 días** → high.
- **Service unreachable** (sin endpoints ready): blocker si `failurePolicy: Fail`, sino high.
- **timeoutSeconds > 10** → medium.

**Meta**: `validating`, `mutating`, `ca_expiring_soon`, `unreachable`.

### 10. `capacity-headroom`

Simula un drain del nodo más cargado:
- Si su carga no cabe en los nodos restantes → critical+blocker.
- Headroom global < 20% CPU o memoria → high.
- ResourceQuota saturada (> 90%) → medium.
- Nodo individual > 85% committed → medium.

Single-node clusters → skip explícito.

**Meta**: `nodes`, `cluster_cpu_headroom`, `cluster_mem_headroom`, `worst_node_drain_fits`, `saturated_quotas`.

### 11. `preflight-dryrun`

Ejecuta la herramienta de upgrade real en modo dry-run, según plataforma:

- **EKS**: AWS SDK `ListInsights` con `cfg.EKSConfig`. Error/Warning insights → critical/high.
- **Upstream**: finding informativo con el comando `kubeadm upgrade plan <target>` a ejecutar manualmente (no hay SSH desde el backend).
- **Kubespray**: parsea `group_vars/` buscando `kube_version`; flag si no coincide con el target.

**Meta**: `platform`, `insights_checked`, `errors`, `warnings`.

### 12. `karpenter-compatibility`

Detecta `Deployment karpenter` en namespaces `karpenter`/`kube-system`. Extrae versión del image tag. Aplica matriz verificada contra https://karpenter.sh/docs/upgrading/compatibility/ (LAST VERIFIED: 2026-05-25):

| Karpenter | Max k8s |
|---|---|
| 0.34 | 1.29 |
| 0.37 | 1.30 |
| 1.0  | 1.31 |
| 1.2  | 1.32 |
| 1.5  | 1.33 |
| 1.6  | 1.34 |
| 1.9  | 1.35 |

- `target > maxK8s` → critical+blocker.
- `target == maxK8s` → info (planear upgrade Karpenter antes del próximo minor).

**Meta**: `installed`, `namespace`, `version`, `supported_range`, `recommended_upgrade`.

### 13. `istio-compatibility`

Detecta `Deployment istiod` en `istio-system`. Aplica matriz verificada contra https://istio.io/latest/docs/releases/supported-releases/ (LAST VERIFIED: 2026-05-25):

| Istio | k8s range | Status |
|---|---|---|
| 1.27 | 1.24 - 1.33 | EOL April 2026 |
| 1.28 | 1.25 - 1.34 | supported |
| 1.29 | 1.26 - 1.35 | supported |
| 1.30 | 1.27 - 1.36 | supported |

Doble validación: compatibilidad con target k8s + status de upstream support (EOL → high finding adicional, independiente del k8s match).

**Meta**: `installed`, `version`, `supported_range`, `upstream_supported`, `recommended_upgrade`.

---

## Modelo de datos

### Finding

```go
type Finding struct {
    ID          string
    CheckerName string
    ClusterType ClusterType
    Severity    Severity     // critical | high | medium | info
    Blocker     bool         // true = MUST resolverse antes de upgrade
    Title       string
    Description string
    Remediation string
    Resource    *Resource
    Source      string       // herramienta detectora
    DocsURL     string
}
```

### CheckResult

```go
type CheckResult struct {
    CheckerName string
    Findings    []Finding
    Meta        map[string]string  // métricas de ejecución
    Error       string
    Skipped     bool
    SkipReason  string
}
```

### Diff (post-upgrade)

```go
type Result struct {
    Pre       *Report
    Post      *Report
    Resolved  []Finding   // pre, ausentes en post
    New       []Finding   // ausentes en pre, en post
    Unchanged []Finding   // en ambos
    Summary   Summary     // contadores agregados
}
```

Matching de findings: `(CheckerName, Title, Resource.Kind+Namespace+Name)`.

---

## API REST

```
GET  /api/v1/cluster?context=<ctx>
     → { cluster_type, version, platform }

GET  /api/v1/check?from=1.34&to=1.35&context=<ctx>
     Headers opcionales:
       X-Cluster-Name, X-AWS-Region          (EKS)
       X-Kubespray-Inventory, X-Kubespray-GroupVars
     → Report { current_version, target_version, blocker, timestamp, results }

POST /api/v1/postcheck
     Body: { pre_report, from, to, context }
     → DiffResult { pre, post, resolved, new, unchanged, summary }

GET  /api/v1/versions?target=<version>
     → { tools, warnings }

POST /api/v1/versions/update
     Refresca Pluto. Intenta GitHub; fallback al Go module cache.
     → UpdateResult { tool, source: github|module-cache, updated_at, max_k8s, message }

POST /api/v1/rag/query
     Body: { query, provider, version_range }
     → { explanation, sources[] }

POST /api/v1/npd/install?context=<ctx>
     → { already_installed, message }

GET  /healthz
     → HTTP 200
```

CORS habilitado para Headlamp.

---

## Gestión de versiones de herramientas

Tres niveles de base de datos por checker:

| Tool | Tipo | Refresh |
|---|---|---|
| **Pluto** | Embebida + cache runtime + module-cache fallback | `POST /api/v1/versions/update` (live), `make update-deps` (rebuild) |
| **Nova** | Runtime | Sin base propia (lee `kubeVersion` del chart) |
| **kubeconform** | Runtime (schemas desde internet) | Automática |
| **Karpenter matrix** | Estática en código | Manual: WebFetch + edit + bump `LAST VERIFIED` |
| **Istio matrix** | Estática en código | Manual: WebFetch + edit + bump `LAST VERIFIED` |

### Pluto cache: lookup order

1. `~/.cache/upgrade-guardian/pluto-versions.yaml` (descargado vía botón "Update databases")
2. Binario embebido (`plutoversionsfile.Content()`)

### Pluto update fallback

`POST /api/v1/versions/update` intenta:
1. `GET https://raw.githubusercontent.com/FairwindsOps/pluto/main/versions.yaml`
2. Si falla (red bloqueada, 404): lee de `$GOMODCACHE/github.com/fairwindsops/pluto/v5@<ver>/versions.yaml`
3. Escribe el resultado al cache file. UI muestra "offline — used bundled copy" si vino del module cache.

### Refrescar matrices Karpenter/Istio

Cuando salga una nueva versión upstream:

```
WebFetch https://karpenter.sh/docs/upgrading/compatibility/
WebFetch https://istio.io/latest/docs/releases/supported-releases/
```

Editar `compatibilityMatrix` en los respectivos `checker.go`, bumpear `LAST VERIFIED`, correr `go test ./internal/checks/karpenter ./internal/checks/istio`.

---

## CLI

`upgrade-guardian-cli` es un binario standalone que envuelve la API. Permite validar upgrades sin necesidad del plugin Headlamp.

```bash
upgrade-guardian-cli [flags] <command> [command-flags]
```

Comandos:
- `check --from 1.34 --to 1.35 [--context X]`
- `cluster [--context X]`
- `versions [--target X]`
- `postcheck --pre-report=<file> --from --to`

Flags globales: `-server`, `-format table|json|csv`, `-v`.

Exit codes: 0 OK, 1 blockers/falla post-upgrade.

Detalles en [`CLI.md`](CLI.md).

---

## Plugin Headlamp — UI

### Dashboard

- **Sección "Upgrade path"**: selector de versiones + botón "Run validation".
- **Database status**: muestra cobertura de Pluto + botón "Update databases".
- **Summary tiles**: contadores por estado tras el scan.
- **Post-upgrade verification**: botón "Verify post-upgrade" + diff visual.
- **Resultados**: `CheckCard`s ordenados por severidad.

### CheckCard

- Header: indicador de estado + nombre + meta chips + badge.
- Findings: grid de 2 columnas (severity pill + detalle).
- NPD missing: botón one-click de instalación con re-run automático.

### Build & deploy

```bash
cd plugin && node build.mjs
cp dist/main.js ~/.config/Headlamp/plugins/upgrade-guardian/main.js
```

El CLI `headlamp-plugin` está roto en Node v26 — usar `build.mjs` con Vite directo.

---

## Empaquetado y distribución

```bash
make release                    # genera dist/upgrade-guardian-*-<os>-<arch>.tar.gz
VERSION=0.2.0 make release      # tag explícito
```

Cada tarball contiene:
- `bin/upgrade-guardian` (server)
- `bin/upgrade-guardian-cli` (CLI)
- `plugin/main.js` + `package.json` (plugin Headlamp)
- `scripts/install.sh` (instalador)
- `scripts/systemd/`, `scripts/launchd/` (service units)
- `docs/`

Plataformas: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`. Binarios statically-linked (`CGO_ENABLED=0`) y stripped (`-s -w`).

Instalación local: `./scripts/install.sh` (sudo prompt para `/usr/local/bin`).

Detalles en [`INSTALL.md`](INSTALL.md).

---

## RAG (pendiente)

La interfaz `RAG` está definida. `NoopRAG` devuelve `"(RAG not configured)"`. La implementación `SQLiteRAG` debe:

1. `go-sqlite3` + extensión `sqlite-vec` para búsqueda vectorial.
2. Embeddings: Ollama `POST /api/embed` con `bge-m3`.
3. LLM: Ollama `POST /api/generate` con `Qwen2.5-Coder-32B-Instruct`.
4. **Invariante**: toda query filtra `WHERE provider = ?` antes de cosine similarity.
5. Fuentes: `docs.aws.amazon.com/eks`, `kubernetes.io`, release notes de CNI/CSI. Blogs prohibidos.

---

## Tests

Cobertura unitaria actual:
- `internal/checks/karpenter/checker_test.go` — 5 tests (matriz, parsing, recommendation)
- `internal/checks/istio/checker_test.go` — 5 tests (matriz, EOL handling, recommendation)

```bash
go test ./...                   # toda la suite
go test ./internal/checks/...   # solo checkers
```

---

## Quirks técnicos conocidos

| Problema | Causa | Mitigación |
|---|---|---|
| Flag `--kubeconfig` duplicado | `controller-runtime` lo registra en `init()` | No redefinir; leer con `flag.Lookup("kubeconfig")` post-Parse |
| `headlamp-plugin build` roto en Node v26 | Conflicto ESM/CJS en yargs | Usar `plugin/build.mjs` con Vite directo |
| etcd certs no accesibles en Kind | Certs en el nodo, no en el host | Finding medium no-blocker |
| Nova sin sufijo `/v3` | No publica versión mayor vía Go modules | Importar como `github.com/fairwindsops/nova` |
| Pluto registra flag global | `fairwindsops/pluto/v5` registra `--kubeconfig` | Mismo workaround que controller-runtime |
| `POST /api/v1/versions/update` HTTP 404 desde GitHub | No hay internet o el path cambió | Fallback automático al Go module cache; UI muestra "offline — used bundled copy" |
| Matrices Karpenter/Istio se desactualizan | Upstream libera versiones; las matrices son estáticas | Refresh manual semestral con WebFetch (instrucciones en headers de los archivos) |
