<div align="center">

# Upgrade Guardian

**Validación determinista de upgrades de Kubernetes — sin alucinaciones, sin sorpresas.**

[![Go Version](https://img.shields.io/badge/go-1.26-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/license-internal-orange)](#license)
[![Checkers](https://img.shields.io/badge/checkers-13-green)](#checkers-disponibles)
[![Platforms](https://img.shields.io/badge/platforms-linux%20%7C%20macOS-lightgrey)](#install)

</div>

---

Apunta Upgrade Guardian a tu clúster live, dile a qué versión querés subir, y te dice **si es seguro** — basándose en escaneos reales del clúster, no en heurísticas ni LLMs.

```
$ upgrade-guardian-cli check --from 1.34 --to 1.35

=== Upgrade Check: 1.34 → 1.35 ===
⚠ BLOCKERS FOUND — upgrade cannot proceed

Findings by severity:
  CRITICAL: 1
  HIGH:     17
  MEDIUM:   6
  INFO:     2

Checkers:
  ✗ workloads-readiness  — 18 findings | pdb_blockers=1 single_replica=16
  ✓ deprecated-apis      — 0  findings | resources_scanned=572 kinds=62
  ✓ webhooks-health      — 0  findings | validating=1 mutating=1 ca_expiring_soon=0
  ✓ karpenter-compat     — 0  findings | installed=false
  ✓ istio-compat         — 0  findings | installed=false
  ✗ crd-schemas          — 5  findings | crds_validated=5
  ✓ capacity-headroom    — 0  findings | skip_reason=single-node cluster
  ...
```

## Tabla de contenidos

- [El problema](#el-problema)
- [Cómo funciona](#cómo-funciona)
- [Checkers disponibles](#checkers-disponibles)
- [Tres formas de usarlo](#tres-formas-de-usarlo)
- [Install](#install)
- [Quick start](#quick-start)
- [Casos de uso típicos](#casos-de-uso-típicos)
- [Cómo se mantiene preciso](#cómo-se-mantiene-preciso)
- [Comparación con alternativas](#comparación-con-alternativas)
- [Arquitectura](#arquitectura)
- [Desarrollo](#desarrollo)
- [Roadmap](#roadmap)
- [Principios cardinales](#principios-cardinales)
- [Documentación](#documentación)

---

## El problema

Hacer upgrade de Kubernetes es **el upgrade más peligroso** que un operador hace regularmente. No por la complejidad del control plane, sino por las **interacciones invisibles**:

- Un PDB con `maxUnavailable: 0` que congela el drain del primer nodo y nadie se da cuenta hasta las 2 AM.
- Una API deprecada en un objeto que llegó al clúster vía Helm, que `kubectl convert` no detecta porque la annotation `last-applied-configuration` no existe.
- Una versión de Karpenter o Istio que parece compatible "porque siempre funcionó" pero rompe la siguiente minor.
- Un webhook con CA expirada que bloquea `kube-system` recreations post-upgrade.
- Drain sin headroom de CPU/memoria que deja pods Pending indefinidamente.

Las herramientas tradicionales (`pluto`, `kube-no-trouble`) cubren una fracción del problema. Lo que faltaba era algo que:

1. **Escanee el clúster vivo, no manifests estáticos.**
2. **Combine herramientas deterministas existentes** (Pluto, Nova, kubeconform, AWS SDK) bajo una sola API.
3. **Mantenga matrices verificadas** contra fuentes oficiales upstream para third-party (Karpenter, Istio).
4. **Devuelva un veredicto único** ("blocker" o "safe") accionable desde una UI, CLI, o pipeline.

Eso es Upgrade Guardian.

---

## Cómo funciona

```
┌──────────────────────┐
│  Tu kubeconfig       │
└──────────┬───────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────┐
│  Backend Go (:8090)                                      │
│  ┌─────────────────────────────────────────────────┐   │
│  │  13 checkers en paralelo                         │   │
│  │  ── Pluto (deprecated APIs)                      │   │
│  │  ── Nova (Helm kubeVersion)                      │   │
│  │  ── kubeconform (CRD schemas)                    │   │
│  │  ── crypto/tls (control plane certs)             │   │
│  │  ── etcd client v3                               │   │
│  │  ── client-go (nodes, PDBs, webhooks, capacity)  │   │
│  │  ── AWS SDK v2 (EKS Insights)                    │   │
│  │  ── matrices verificadas (Karpenter, Istio)      │   │
│  └─────────────────────────────────────────────────┘   │
└──────────┬──────────────────────────────────────────────┘
           │
           ▼
┌──────────────────────────────────────────────────────────┐
│  Report { blocker, results[], meta }                      │
└──────────┬───────────────────────────────────────────────┘
           │
   ┌───────┴───────┬─────────────┐
   ▼               ▼             ▼
Headlamp UI     CLI tool      REST API
(operadores)    (CI/CD)       (custom)
```

Cada checker es **independiente** y corre en una goroutine. Un checker fallido no detiene a los demás. El tipo de clúster (EKS / Kubespray / upstream) se detecta automáticamente y los checkers se filtran por aplicabilidad.

---

## Checkers disponibles

Trece checkers organizados en cinco categorías:

### Compatibilidad de APIs (3)

| Checker | Pregunta | Herramienta |
|---|---|---|
| `deprecated-apis` | ¿Hay objetos live con APIs removidas/deprecated en target? | Pluto + dynamic scan |
| `helm-cves` | ¿Algún chart Helm declara `kubeVersion` incompatible? | Nova |
| `crd-schemas` | ¿Algún schema CRD rompe en target? | kubeconform |

### Salud del clúster (4)

| Checker | Pregunta | Herramienta |
|---|---|---|
| `control-plane` | ¿API server / certs / componentes están sanos? | HTTPS + `crypto/tls` |
| `etcd-health` | ¿etcd alcanzable, sin alarmas NOSPACE/CORRUPT? | etcd client v3 |
| `node-health` | ¿Conditions OK + kubelet skew ≤ 2 minors? | client-go + NPD |
| `provider-compatibility` | ¿CNI / EKS add-ons / Kubespray inventory compatibles? | DaemonSet detect + AWS SDK |

### Resiliencia de workloads (2)

| Checker | Pregunta | Herramienta |
|---|---|---|
| `workloads-readiness` | ¿Los PDBs permiten drain? ¿Replicas / probes / pods rotos? | client-go |
| `webhooks-health` | ¿Admission webhooks tienen CA válida y son alcanzables? | `crypto/x509` + Endpoints |

### Capacidad y operación (2)

| Checker | Pregunta | Herramienta |
|---|---|---|
| `capacity-headroom` | ¿Hay capacidad para drenar 1 nodo sin Pending pods? | Simulación con requests |
| `preflight-dryrun` | EKS Insights / `kubeadm upgrade plan` guidance / Kubespray inventory | AWS SDK + parsers |

### Add-ons críticos (2)

| Checker | Pregunta | Herramienta |
|---|---|---|
| `karpenter-compatibility` | ¿Karpenter instalado soporta target k8s? | Matrix verificada (LAST VERIFIED: 2026-05-25) |
| `istio-compatibility` | ¿Istio soporta target + sigue en upstream support? | Matrix verificada |

Cada finding incluye **título, descripción, remediation con comando concreto, link a docs oficiales, severidad, blocker flag, y recurso afectado**.

---

## Tres formas de usarlo

| Interface | Cuándo usarlo | Ejemplo |
|---|---|---|
| 🖥️ **Headlamp plugin** | UI rica para operadores; summary tiles, post-upgrade diff, one-click NPD install | Click "Run validation" en el sidebar de Headlamp |
| ⚙️ **CLI** | CI/CD, automation, scripting, headless servers | `upgrade-guardian-cli check --from 1.34 --to 1.35` |
| 🔌 **REST API** | Integraciones custom (Slack bots, dashboards, ChatOps) | `curl :8090/api/v1/check?from=1.34&to=1.35` |

El backend es la única fuente de verdad. Los tres frontends llaman a la misma API `/api/v1/*`.

---

## Install

### Binario pre-compilado (recomendado)

```bash
# Descargar el tarball para tu plataforma
curl -L https://github.com/harrysalva/k8s-updater/releases/latest/download/upgrade-guardian-linux-amd64.tar.gz | tar -xz
cd upgrade-guardian-*
./scripts/install.sh
```

Esto instala:
- `/usr/local/bin/upgrade-guardian` (server)
- `/usr/local/bin/upgrade-guardian-cli` (CLI)
- `~/.config/Headlamp/plugins/upgrade-guardian/` (plugin Headlamp)
- systemd user unit (Linux) o LaunchAgent (macOS) para auto-start

### Desde source

```bash
git clone git@github.com:harrysalva/k8s-updater.git
cd k8s-updater
make build
./bin/upgrade-guardian --kubeconfig ~/.kube/config &
./bin/upgrade-guardian-cli check --from 1.34 --to 1.35
```

Instrucciones detalladas + RBAC mínimo: [`docs/INSTALL.md`](docs/INSTALL.md).

---

## Quick start

### 1. Iniciar el backend

```bash
upgrade-guardian --kubeconfig ~/.kube/config
# Backend escucha en localhost:8090
```

### 2. Validar tu upgrade

```bash
# Verificación rápida
upgrade-guardian-cli check --from 1.34 --to 1.35

# Con detalle de cada finding
upgrade-guardian-cli -v check --from 1.34 --to 1.35

# Exportar a JSON para análisis posterior
upgrade-guardian-cli -format json check --from 1.34 --to 1.35 > report.json
```

### 3. Después del upgrade — verificar que mejoró

```bash
# Guardar report pre-upgrade
upgrade-guardian-cli -format json check --from 1.34 --to 1.35 > pre.json

# ... hacer el upgrade ...

# Comparar con el estado post
upgrade-guardian-cli postcheck --pre-report pre.json --from 1.34 --to 1.35

# Output:
# ✓ VERIFIED — Upgrade successful
#   18 resolved | 0 new | 8 unchanged
```

### 4. Desde la UI

Abrir Headlamp → sidebar **"Upgrade Guardian"** → seleccionar versiones → click **"Run validation"**.

---

## Casos de uso típicos

### CI/CD — bloquear merges que romperían el próximo upgrade

```yaml
# .github/workflows/upgrade-readiness.yml
- name: Validate upgrade readiness
  run: |
    upgrade-guardian &
    sleep 2
    upgrade-guardian-cli check --from 1.34 --to 1.35
    # Exit code 1 si hay blockers → falla el job
```

### Reportes periódicos para SRE

```bash
# cron diario, manda CSV por email
upgrade-guardian-cli -format csv check --from $CURRENT --to $TARGET \
  | mail -s "Daily upgrade readiness" sre@company.com
```

### Validación pre-window de mantenimiento

```bash
for cluster in prod-us prod-eu prod-asia; do
  echo "=== $cluster ==="
  upgrade-guardian-cli check --from 1.34 --to 1.35 --context $cluster
done | tee maintenance-window-report.txt
```

### Integración con Slack/ChatOps

```bash
# Webhook que postea blockers a Slack
curl -s http://localhost:8090/api/v1/check?from=1.34&to=1.35 | \
  jq '.results[].findings[] | select(.blocker == true)' | \
  send-to-slack
```

---

## Cómo se mantiene preciso

Upgrade Guardian se enfrenta a dos tipos de "drift" — la documentación oficial cambia con el tiempo. La estrategia:

### Bases de datos (Pluto, Nova, kubeconform)

| Base | Cómo se actualiza | Frecuencia recomendada |
|---|---|---|
| Pluto (APIs deprecated) | `POST /api/v1/versions/update` (live, sin rebuild) o `make update-deps` (con rebuild) | Antes de cada minor de k8s |
| Nova (Helm) | Lee `kubeVersion` del chart en runtime | Automático |
| kubeconform (CRDs) | Descarga schemas en runtime | Automático |

El endpoint de update tiene **fallback al Go module cache** si no hay internet, así que clústeres airgapped siguen funcionando.

### Matrices de third-party (Karpenter, Istio)

Las matrices están **hardcoded** en código con un comentario `LAST VERIFIED: YYYY-MM-DD` y URL upstream. Refresh manual:

```bash
# 1. Hacer WebFetch (en Claude) sobre las URLs upstream
#    https://karpenter.sh/docs/upgrading/compatibility/
#    https://istio.io/latest/docs/releases/supported-releases/

# 2. Editar internal/checks/{karpenter,istio}/checker.go → compatibilityMatrix
# 3. Bumpear LAST VERIFIED
# 4. Verificar
go test ./internal/checks/karpenter ./internal/checks/istio
```

**Por qué manual y no scraping automático**: el HTML de las páginas upstream cambia con cada redesign; un parser HTML se rompe en silencio. Refresh manual cada ~6 meses es trivial y verificable.

---

## Comparación con alternativas

| Tool | APIs deprecated | Helm | CRDs | Control plane | Nodes | PDBs | Webhooks | Capacity | Karpenter | Istio | EKS Insights |
|---|---|---|---|---|---|---|---|---|---|---|---|
| **Upgrade Guardian** | ✅ live scan | ✅ Nova | ✅ kubeconform | ✅ | ✅ +skew | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `pluto` (standalone) | ✅ static only | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| `kube-no-trouble` | ✅ live | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| `kubent` | ✅ live | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| `popeye` / `polaris` | ❌ | ❌ | ❌ | partial | ✅ | ✅ | partial | ❌ | ❌ | ❌ | ❌ |
| `eksctl upgrade --dry-run` | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Headlamp UI nativa | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |

Las herramientas existentes cubren slices del problema. Upgrade Guardian no las reemplaza sino que las **orquesta** (Pluto y Nova están embebidos directamente como librerías Go).

---

## Arquitectura

```
k8s-updater/
├── cmd/
│   ├── server/main.go              # Backend HTTP (:8090)
│   └── cli/main.go                 # CLI standalone
├── internal/
│   ├── checker/                    # Interface + tipos compartidos
│   ├── engine/                     # Orquestador concurrente de checkers
│   ├── detector/                   # Detección de tipo de clúster
│   ├── api/                        # REST endpoints
│   ├── diff/                       # Pre/post upgrade diff
│   ├── versions/                   # Pluto cache + module-cache fallback
│   ├── npd/                        # node-problem-detector installer
│   ├── rag/                        # LLM interface (NoopRAG por ahora)
│   ├── cli/                        # Cliente HTTP + formatters
│   └── checks/                     # Los 13 checkers (uno por subdirectorio)
├── plugin/                         # Headlamp plugin (React + TypeScript + MUI)
├── scripts/                        # release.sh, install.sh, systemd, launchd
├── docs/                           # ARCHITECTURE, INSTALL, CLI, ADRs
└── Makefile                        # build, test, release, plugin-build, update-deps
```

Detalles completos: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

---

## Desarrollo

```bash
# Build
make build                  # bin/upgrade-guardian + bin/upgrade-guardian-cli
make plugin-build           # plugin Headlamp → ~/.config/Headlamp/plugins/

# Tests
make test                   # toda la suite
go test ./internal/checks/karpenter ./internal/checks/istio   # solo matrices

# Release
make release                # genera dist/upgrade-guardian-*-<os>-<arch>.tar.gz
VERSION=0.2.0 make release  # con versión explícita

# Actualizar bases de datos de herramientas
make update-deps            # go get latest + rebuild

# Run local
make dev                    # backend con log-level debug
```

### Agregar un nuevo checker

1. Crear `internal/checks/<nombre>/checker.go` que implemente `checker.Checker`.
2. Registrar en `internal/engine/engine.go`.
3. Agregar label + meta formatters en `plugin/src/components/CheckCard.tsx`.
4. Documentar en `docs/ARCHITECTURE.md`.
5. Si usa matriz upstream: incluir comentario `SOURCE OF TRUTH:` + `LAST VERIFIED:`.

### Agregar matriz de compatibilidad (cert-manager, ingress-nginx, etc.)

Mismo patrón que `karpenter` o `istio`. Plantilla:

```go
var compatibilityMatrix = map[string]versionRange{
    "X.Y": {minK8sMinor, maxK8sMinor},
    // ...
}
```

Y un test que valide integridad de la matriz:

```go
func TestMatrixIntegrity(t *testing.T) {
    for series, rng := range compatibilityMatrix {
        if rng.minMinor > rng.maxMinor { t.Errorf(...) }
    }
}
```

---

## Roadmap

| Prioridad | Item | Estado |
|---|---|---|
| Alta | RAG real con SQLite-vec + bge-m3 + Qwen2.5-Coder-32B | Pendiente — interface lista, `NoopRAG` placeholder |
| Alta | SSH executor para `kubeadm upgrade plan` real | Pendiente — hoy solo informativo |
| Media | Matrices para cert-manager, external-dns, ingress-nginx | Pendiente |
| Media | GitHub Actions release pipeline (auto-publish a Releases on tag) | Pendiente |
| Media | Tests de integración con clúster Kind ephemeral | Pendiente |
| Baja | Docker image + Helm chart para deploy in-cluster | Pendiente — actualmente solo binario standalone |
| Baja | Slack webhook integration nativa | Pendiente |

PRs bienvenidos.

---

## Principios cardinales

1. **No alucinaciones.** Los findings vienen de herramientas deterministas, jamás de LLMs.
2. **Cero falsos positivos.** Mejor omitir un check dudoso que generar ruido.
3. **Matrices verificadas.** Las matrices de third-party citan URL upstream + `LAST VERIFIED`. Sin scraping automático.
4. **Backend = fuente de verdad.** UI, CLI y curl llaman a la misma API. No hay lógica de validación en el frontend.
5. **Detección live.** Escaneo de objetos vivos del clúster, no de manifests Git.
6. **Refrescable sin rebuild.** Pluto DB se actualiza en runtime con fallback al module cache.

Más detalle en [`docs/adr/`](docs/adr/).

---

## Documentación

| Archivo | Contenido |
|---|---|
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Sistema completo, modelo de datos, API REST, cada checker en detalle |
| [`docs/INSTALL.md`](docs/INSTALL.md) | Instalación paso a paso, RBAC mínimo YAML, troubleshooting |
| [`docs/CLI.md`](docs/CLI.md) | Comandos CLI, formatos de output, ejemplos CI/CD |
| [`docs/adr/0001`](docs/adr/0001-deterministic-only-detection.md) | Por qué no usar LLMs para detección |
| [`docs/adr/0002`](docs/adr/0002-stack-tecnologico.md) | Decisión de stack por dominio (Pluto, Nova, kubeconform, etc.) |

---

## Status del proyecto

| Componente | Estado |
|---|---|
| Backend (13 checkers) | ✅ Production-ready |
| CLI standalone | ✅ Production-ready |
| Headlamp plugin | ✅ Production-ready |
| Multi-arch packaging | ✅ linux/darwin × amd64/arm64 |
| Unit tests (matrices) | ✅ 10/10 passing |
| RAG real (LLM explanations) | ⚠️ Interface lista, `SQLiteRAG` pendiente |
| EKS Insights via preflight | ✅ Implementado |
| kubeadm upgrade plan via SSH | ⚠️ Solo informativo |
| Integration tests con Kind | ⚠️ Pendiente |

---

## License

Internal tool — uso restringido al equipo. Ver repositorio interno para términos completos.
