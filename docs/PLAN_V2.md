# Upgrade Guardian — Plan de Implementación V2

> Objetivo: pasar de ~65% de cobertura real a ~95% para upgrades EKS/k8s de mínimo impacto.
> Esfuerzo estimado total: 60-80 horas. Marcar cada ítem con `[x]` al completarlo.

---

## Fase 1 — Fundamentos (prioridad CRÍTICA, ~12h)

### 1.1 Infraestructura de tests de integración con Kind (~6h)

- [ ] Crear `tests/integration/` con `main_test.go` y `TestMain` que levanta un cluster Kind
- [ ] Añadir helper `tests/integration/fixtures/` con manifiestos YAML para escenarios:
  - [ ] `deprecated-api.yaml` — recurso con `networking.k8s.io/v1beta1 Ingress`
  - [ ] `pdb-blocking.yaml` — PDB con `maxUnavailable: 0`
  - [ ] `single-replica.yaml` — Deployment con `replicas: 1`
  - [ ] `missing-probes.yaml` — Deployment >2 réplicas sin readinessProbe
  - [ ] `crashloop.yaml` — Pod en CrashLoopBackOff
  - [ ] `webhook-expired-ca.yaml` — ValidatingWebhookConfiguration con CA caducada
  - [ ] `webhook-missing-service.yaml` — Webhook apuntando a Service inexistente
  - [ ] `overloaded-node.yaml` — DaemonSet que llena nodo de requests
  - [ ] `helm-release.yaml` — Helm release desactualizado (via `helm install` en setup)
  - [ ] `crd-schema.yaml` — CRD con schema JSON inválido en target version
  - [ ] `node-not-ready.yaml` — Patch en node status para simular NotReady
  - [ ] `etcd-alarm.yaml` — No aplica para Kind; test de degraded graceful
  - [ ] `kubelet-skew.yaml` — No aplica directamente; test de versión mock
  - [ ] `healthy-cluster.yaml` — Cluster limpio (zero findings esperados)
  - [ ] `all-blockers.yaml` — Combina todos los blocking scenarios
- [ ] Crear `tests/integration/suite_test.go` con tabla de casos y assert por checker
- [ ] Añadir `make test-integration` en `Makefile` con `go test ./tests/integration/... -tags=integration`
- [ ] Añadir flag `-short` para saltarse integration tests en CI rápido
- [ ] Documentar en `docs/TESTING.md` cómo levantar el entorno Kind localmente
- [ ] **Criterio de aceptación**: `make test-integration` pasa en CI con Kind preinstalado

### 1.2 Mensajes honestos y cobertura explícita (~1h)

- [ ] Cambiar texto del botón en UI: "Run upgrade check" → "Run pre-upgrade scan (13 checkers)"
- [ ] Cambiar `verdict` text: "Safe to upgrade" → "No blockers detected"
- [ ] Añadir chip/badge en UI con "Coverage: 13/~25 known risk categories"
- [ ] Crear `docs/COVERAGE.md` con tabla de qué se detecta, qué no, y por qué
  - Columnas: `Categoría | Detectado | Checker | Limitación conocida`
  - Filas para las ~25 categorías de riesgo reales en upgrades EKS
- [ ] Añadir link a `COVERAGE.md` en el footer de la UI y en el README
- [ ] **Criterio de aceptación**: ninguna string en la UI promete "safe to upgrade" sin matices

### 1.3 Warning de matriz obsoleta (~1h)

- [ ] Añadir constante `MatrixLastVerified time.Time` en cada checker con matriz estática:
  - [ ] `internal/checks/karpenter/checker.go`
  - [ ] `internal/checks/istio/checker.go`
  - [ ] (futuros) cert-manager, ingress-nginx, ALB controller, ArgoCD, Prometheus Operator
- [ ] Añadir lógica en `engine.go`: si `time.Since(MatrixLastVerified) > 180*24*time.Hour` → finding `medium` con mensaje "Compatibility matrix for X is N days old — verify against upstream"
- [ ] Si `> 365*24*time.Hour` → finding `high`
- [ ] Exponer `matrix_age_days` en el JSON de cada checker afectado
- [ ] **Criterio de aceptación**: test unitario que inyecta fecha antigua y verifica finding generado

### 1.4 Versionado de API (`/api/v2`) (~2h)

- [ ] Crear `internal/api/v2/handlers.go` con mismos endpoints pero bajo `/api/v2/`
- [ ] Cambiar `Content-Type` response a `application/vnd.upgrade-guardian.v2+json`
- [ ] Mantener `/api/v1/` funcional y sin breaking changes (backwards compat)
- [ ] Añadir header `X-API-Version: 2` en respuestas v2
- [ ] Actualizar plugin para usar `/api/v2/` con fallback a v1 si 404
- [ ] Actualizar CLI para usar `/api/v2/` con flag `--api-version 1|2`
- [ ] **Criterio de aceptación**: ambos endpoints responden; CLI funciona con `--api-version 1`

### 1.5 Cache compartida de listings (~2h)

- [ ] Crear `internal/cache/lister.go` con `SharedLister` que recibe `client-go` y cachea por (GVR, namespace, `ListOptions.LabelSelector`)
- [ ] TTL: 30 segundos por entrada; invalidar al inicio de cada run
- [ ] Pasar `SharedLister` a todos los checkers que actualmente hacen `List()` propio
- [ ] Checkers que se benefician: nodes, workloads, webhooks, capacity, karpenter, istio, provider
- [ ] Medir reducción de llamadas API con contador en `SharedLister.Stats()`
- [ ] **Criterio de aceptación**: para cluster de 500 nodos, tiempo total <60s (vs >120s sin cache)

---

## Fase 2 — EKS específico (prioridad ALTA, ~14h)

### 2.1 Checker: VPC CNI version + prefix delegation (~3h)

Archivo: `internal/checks/vpc-cni/checker.go`

- [ ] Detectar versión de `aws-node` DaemonSet en `kube-system`
- [ ] Obtener versión EKS target del header `X-Target-Version`
- [ ] Consultar `DescribeAddonVersions(addonName="vpc-cni", kubernetesVersion=target)` via AWS SDK
- [ ] Comparar versión instalada vs `defaultVersion` del addon en EKS
- [ ] Si `installed < minimum_for_target` → `critical/blocker` con link a upgrade docs
- [ ] Detectar si `ENABLE_PREFIX_DELEGATION=true` en `aws-node` ConfigMap
- [ ] Si prefix delegation activo + versión <1.11 → `critical/blocker` (incompatible)
- [ ] Añadir a engine como checker 14 (solo ejecuta si `ClusterType == EKS`)
- [ ] Tests unitarios con mock de AWS SDK
- [ ] **Criterio de aceptación**: detecta VPC CNI outdated en cluster EKS real

### 2.2 Checker: Subnet IP availability (~3h)

Archivo: `internal/checks/subnet-ips/checker.go`

- [ ] Leer node annotations `vpc.amazonaws.com/node-subnet-id` para obtener subnets en uso
- [ ] Llamar `ec2.DescribeSubnets(SubnetIds=[...])` via AWS SDK v2
- [ ] Por subnet: calcular `AvailableIpAddressCount` vs `TotalIpCount` (de CIDR block)
- [ ] Si alguna subnet tiene `AvailableIpAddressCount < 10%` → `high` finding
- [ ] Si `< 5%` → `critical/blocker` (upgrade de nodo no podrá lanzar ENIs nuevos)
- [ ] Incluir nombre de subnet, AZ, y CIDR en finding details
- [ ] Si prefix delegation activo: multiplicar por factor 16 (cada IP hostea 16 pods)
- [ ] Tests con mock de EC2 client
- [ ] **Criterio de aceptación**: funciona sin acceso real a AWS usando mocks

### 2.3 Checker: IRSA / OIDC provider validation (~3h)

Archivo: `internal/checks/irsa/checker.go`

- [ ] Leer todos los ServiceAccounts con annotation `eks.amazonaws.com/role-arn`
- [ ] Para cada SA: obtener el role ARN y llamar `iam.GetRole(roleName)` via AWS SDK
- [ ] Extraer trust policy y verificar que el OIDC provider URL del cluster esté presente
- [ ] Obtener OIDC provider del cluster via `eks.DescribeCluster` → `OIDCIssuerURL`
- [ ] Si trust policy no referencia el OIDC correcto → `high` finding por SA
- [ ] Verificar que el OIDC provider esté registrado en IAM (`iam.ListOpenIDConnectProviders`)
- [ ] Si OIDC provider no registrado → `critical/blocker`
- [ ] Verificar que el thumbprint del OIDC provider no haya expirado (cert TLS del issuer)
- [ ] Tests con mock de IAM/EKS clients
- [ ] **Criterio de aceptación**: detecta SA con role ARN apuntando a OIDC incorrecto

### 2.4 Checker: EKS managed add-on versions (~2h)

Archivo: `internal/checks/eks-addons/checker.go`  
(Refactor de la parte pendiente del checker `preflight/eks.go`)

- [ ] Listar add-ons managed via `eks.ListAddons(clusterName)`
- [ ] Para cada add-on: `eks.DescribeAddon` → versión actual
- [ ] Para cada add-on: `eks.DescribeAddonVersions` → versiones disponibles para el target k8s
- [ ] Si versión actual no es compatible con target → `critical/blocker`
- [ ] Si versión actual es compatible pero hay versión más nueva → `medium` informativo
- [ ] Add-ons a cubrir: `vpc-cni`, `coredns`, `kube-proxy`, `aws-ebs-csi-driver`, `aws-efs-csi-driver`
- [ ] Tests con mock de EKS client
- [ ] **Criterio de aceptación**: detecta add-on incompatible con target version

### 2.5 Security Groups for Pods check (~2h)

Archivo: `internal/checks/sgp/checker.go`

- [ ] Leer pods con annotation `vpc.amazonaws.com/pod-eni` (SGfP activo)
- [ ] Verificar que `ENABLE_POD_ENI=true` en `amazon-vpc-cni` ConfigMap
- [ ] Verificar que los nodos del pod tienen la etiqueta `vpc.amazonaws.com/has-trunk-attached: true`
- [ ] Si pods con SGfP pero trunk no attached → `high` finding (pods perderán conectividad)
- [ ] Si versión VPC CNI < 1.7.7 y SGfP en uso → `critical/blocker`
- [ ] **Criterio de aceptación**: detecta misconfiguration de SGfP en cluster mock

### 2.6 Node drain order / PDB interaction check (~1h)

Integración en checker existente `workloads/checker.go`:

- [ ] Para cada PDB bloqueante, verificar cuántos nodos tienen pods de ese PDB
- [ ] Si un PDB tiene pods en >1 nodo y `minAvailable` es alto → añadir contexto al finding
- [ ] Incluir "drain order suggestion" en meta: drenar nodos con menor densidad de pods protegidos primero
- [ ] **Criterio de aceptación**: finding de PDB incluye lista de nodos afectados

---

## Fase 3 — Ecosistema de add-ons (prioridad MEDIA, ~14h)

### 3.1 Checker: cert-manager compatibility (~3h)

Archivo: `internal/checks/cert-manager/checker.go`

- [ ] Detectar cert-manager: Deployment `cert-manager` en `cert-manager` namespace
- [ ] Parsear imagen para extraer versión (e.g., `v1.14.0`)
- [ ] Matriz de compatibilidad (verificar en https://cert-manager.io/docs/releases/):
  ```go
  var compatibilityMatrix = map[string]versionRange{
      "1.12": {25, 28}, "1.13": {25, 29},
      "1.14": {25, 30}, "1.15": {25, 31},
      "1.16": {26, 32},
  }
  var supportedReleases = map[string]bool{"1.14": true, "1.15": true, "1.16": true}
  ```
- [ ] `LAST_VERIFIED: 2026-05-25` — actualizar trimestralmente
- [ ] Detectar si hay `Certificate` resources con `issuerRef` a ClusterIssuers que no existen
- [ ] Tests: matrix integrity, parseImageTag, recommendedUpgrade
- [ ] **Criterio de aceptación**: detecta cert-manager incompatible y EOL

### 3.2 Checker: ingress-nginx compatibility (~3h)

Archivo: `internal/checks/ingress-nginx/checker.go`

- [ ] Detectar: Deployment `ingress-nginx-controller` en `ingress-nginx`
- [ ] Matriz (verificar en https://github.com/kubernetes/ingress-nginx#supported-versions-table):
  ```go
  var compatibilityMatrix = map[string]versionRange{
      "1.9": {27, 30}, "1.10": {28, 31}, "1.11": {29, 32},
      "1.12": {30, 33},
  }
  ```
- [ ] Verificar que `ValidatingWebhookConfiguration` de ingress-nginx tiene CA válida
- [ ] Si webhook de ingress-nginx está caído + `failurePolicy: Fail` → heredar finding del webhooks checker (no duplicar)
- [ ] Tests: matrix integrity, parseImageTag, recommendedUpgrade
- [ ] **Criterio de aceptación**: detecta ingress-nginx incompatible y warning de webhook

### 3.3 Checker: AWS Load Balancer Controller compatibility (~2h)

Archivo: `internal/checks/alb-controller/checker.go`

- [ ] Detectar: Deployment `aws-load-balancer-controller` en `kube-system`
- [ ] Matriz (verificar en https://kubernetes-sigs.github.io/aws-load-balancer-controller/v2.8/deploy/installation/#supported-kubernetes-versions):
  ```go
  var compatibilityMatrix = map[string]versionRange{
      "2.6": {25, 29}, "2.7": {26, 30}, "2.8": {27, 31},
  }
  ```
- [ ] Verificar IAM role annotation en ServiceAccount del controller
- [ ] Tests: matrix integrity
- [ ] **Criterio de aceptación**: detecta ALB controller incompatible

### 3.4 Checker: ArgoCD / Flux compatibility (~2h)

Archivo: `internal/checks/gitops/checker.go`

- [ ] Detectar ArgoCD: Deployment `argocd-server` en `argocd`
- [ ] Detectar Flux: Deployment `helm-controller` en `flux-system`
- [ ] Matrices:
  ```go
  // ArgoCD: verificar en https://argo-cd.readthedocs.io/en/stable/operator-manual/upgrading/
  var argoCDMatrix = map[string]versionRange{
      "2.9": {25, 29}, "2.10": {26, 30}, "2.11": {27, 31}, "2.12": {28, 32},
  }
  // Flux: generalmente compatible con k8s N-2
  var fluxMatrix = map[string]versionRange{
      "2.1": {25, 29}, "2.2": {26, 30}, "2.3": {27, 31},
  }
  ```
- [ ] Tests: matrix integrity para ambas herramientas
- [ ] **Criterio de aceptación**: detecta ArgoCD/Flux incompatibles

### 3.5 Checker: Prometheus Operator / kube-prometheus-stack (~2h)

Archivo: `internal/checks/monitoring/checker.go`

- [ ] Detectar: Deployment `prometheus-operator` o `kube-prometheus-stack-operator`
- [ ] Matriz (verificar en https://github.com/prometheus-operator/prometheus-operator/blob/main/COMPATIBILITY.md):
  ```go
  var promOperatorMatrix = map[string]versionRange{
      "0.69": {25, 29}, "0.70": {26, 30}, "0.71": {27, 31},
      "0.72": {28, 32}, "0.73": {29, 33},
  }
  ```
- [ ] Verificar que `ServiceMonitor` CRD está presente (prereq para el operator)
- [ ] Tests: matrix integrity
- [ ] **Criterio de aceptación**: detecta Prometheus Operator incompatible

### 3.6 UI: labels y formatMetaEntry para nuevos checkers (checkers 14-19) (~2h)

En `plugin/src/components/CheckCard.tsx`:

- [ ] Añadir a `CHECKER_LABEL`:
  ```typescript
  'vpc-cni-version': 'VPC CNI Version',
  'subnet-ip-availability': 'Subnet IP Availability',
  'irsa-oidc': 'IRSA / OIDC Provider',
  'eks-addons': 'EKS Managed Add-ons',
  'cert-manager-compatibility': 'cert-manager',
  'ingress-nginx-compatibility': 'ingress-nginx',
  'alb-controller-compatibility': 'AWS LB Controller',
  'gitops-compatibility': 'GitOps (ArgoCD/Flux)',
  'monitoring-compatibility': 'Prometheus Operator',
  ```
- [ ] Añadir casos en `formatMetaEntry()` para todos los meta keys nuevos
- [ ] Rebuild plugin y verificar en Headlamp
- [ ] **Criterio de aceptación**: nuevos checkers aparecen con label correcto en UI

---

## Fase 4 — Operacional (prioridad MEDIA-ALTA, ~10h)

### 4.1 Checker: etcd defragmentation status (~1h)

Integración en `internal/checks/etcd/checker.go`:

- [ ] Obtener `db_size` y `db_size_in_use` via `cli.Status()` (ya disponible)
- [ ] Calcular fragmentation ratio: `(db_size - db_size_in_use) / db_size`
- [ ] Si ratio > 30% → `medium` finding con comando `etcdctl defrag` sugerido
- [ ] Si ratio > 50% → `high` finding (posible out-of-space durante upgrade)
- [ ] Añadir `db_size_mb`, `db_size_in_use_mb`, `frag_pct` al meta del finding
- [ ] **Criterio de aceptación**: test unitario con mock que simula ratio 40%

### 4.2 Checker: CSI driver version compatibility (~3h)

Archivo: `internal/checks/csi/checker.go`

- [ ] Detectar CSI drivers instalados como StatefulSet/DaemonSet:
  - `aws-ebs-csi-driver` en `kube-system`
  - `aws-efs-csi-driver` en `kube-system`
  - `rook-ceph-operator` en `rook-ceph`
  - `longhorn-manager` en `longhorn-system`
- [ ] Matrices de compatibilidad por driver:
  ```go
  var ebsCSIMatrix = map[string]versionRange{
      "1.25": {25, 29}, "1.26": {26, 30}, "1.27": {27, 31},
      "1.28": {28, 32}, "1.29": {29, 33},
  }
  ```
- [ ] Verificar StorageClasses referencian provisioners que aún existen tras upgrade
- [ ] Verificar PVCs en `Pending` o `Failed` (previo a upgrade = blocker)
- [ ] Tests: matrix integrity, detecta driver incompatible
- [ ] **Criterio de aceptación**: detecta aws-ebs-csi-driver desactualizado

### 4.3 Checker: storage readiness (~2h)

Archivo: `internal/checks/storage/checker.go`

- [ ] Listar todos los PVCs: si alguno en `Pending` state → `high` finding
- [ ] Listar todos los PVs: si alguno en `Failed` state → `high` finding
- [ ] Para PVCs en `Bound`: verificar que PV referenciado existe y está `Available` o `Bound`
- [ ] Si StorageClass tiene `volumeBindingMode: WaitForFirstConsumer` y hay pods Pending → correlacionar con capacity checker
- [ ] Verificar que VolumeAttachments no estén stuck (`.spec.attacher` != `metadata.deletionTimestamp`)
- [ ] **Criterio de aceptación**: detecta PVC stuck y PV failed

### 4.4 Flag `--last-mile` para validación estricta post-upgrade (~2h)

- [ ] Añadir flag `--last-mile bool` al backend (CLI y API)
- [ ] Cuando `last_mile=true`, elevar findings `medium` → `high` y `high` → `critical` en:
  - workloads-readiness
  - webhooks-health
  - capacity-headroom
  - storage-readiness
- [ ] Cuando `last_mile=true`, añadir checks adicionales:
  - Verificar que todos los pods están en `Running` state (no solo workloads seleccionados)
  - Verificar que todos los nodes están `Ready` (no solo degraded)
  - Verificar que `ComponentStatuses` están healthy (scheduler, controller-manager)
- [ ] Añadir `--last-mile` flag al CLI (`upgrade-guardian-cli check --last-mile`)
- [ ] Añadir toggle en UI: "Strict mode (last-mile)"
- [ ] **Criterio de aceptación**: con `--last-mile`, findings medium se vuelven critical

### 4.5 Verificación de CoreDNS y kube-proxy skew (~2h)

Integración en `internal/checks/controlplane/checker.go`:

- [ ] Leer Deployment `coredns` en `kube-system`, comparar imagen tag con versión recomendada para target
- [ ] Leer DaemonSet `kube-proxy` en `kube-system`, comparar imagen tag
- [ ] Fuente: `registry.k8s.io/coredns/coredns:v1.11.1` para k8s 1.28, etc.
- [ ] Mantener tabla estática `coreDNSVersions` y `kubeProxyVersions` por minor k8s
- [ ] Si skew > 2 minor versions → `critical/blocker`
- [ ] **Criterio de aceptación**: test que detecta coredns 2 versiones por detrás

---

## Fase 5 — Historial y drift detection (prioridad BAJA-MEDIA, ~10h)

### 5.1 SQLite history store (~3h)

Archivo: `internal/history/store.go`

- [ ] Schema:
  ```sql
  CREATE TABLE runs (
    id INTEGER PRIMARY KEY,
    run_id TEXT UNIQUE,
    timestamp INTEGER,
    cluster TEXT,
    target_version TEXT,
    blocker INTEGER,
    findings_json TEXT
  );
  CREATE TABLE findings (
    id INTEGER PRIMARY KEY,
    run_id TEXT,
    fingerprint TEXT,
    checker TEXT,
    severity TEXT,
    title TEXT,
    resource_kind TEXT,
    resource_ns TEXT,
    resource_name TEXT,
    first_seen INTEGER,
    last_seen INTEGER
  );
  ```
- [ ] Implementar `Store.SaveRun(report)` — inserta en `runs` y `findings`
- [ ] Implementar `Store.ListRuns(cluster, limit)` — lista corridas recientes
- [ ] Implementar `Store.GetRun(runID)` — obtiene report completo
- [ ] Ruta del DB: `~/.local/share/upgrade-guardian/history.db` (Linux) / `~/Library/Application Support/upgrade-guardian/history.db` (macOS)
- [ ] Handler `GET /api/v2/history` — lista runs con paginación
- [ ] Handler `GET /api/v2/history/:runID` — devuelve report de run pasado
- [ ] **Criterio de aceptación**: segunda corrida lee historial de primera

### 5.2 Drift detection (~3h)

Archivo: `internal/history/drift.go`

- [ ] Función `DetectDrift(current *checker.Report, store *Store, cluster string) []DriftFinding`
- [ ] `DriftFinding`: `{Fingerprint, FirstSeen, LastSeen, OccurrenceCount, Status}`
- [ ] `Status` enum: `new` (no visto antes), `recurring` (N veces), `resolved` (estaba, ya no), `worsened` (severity aumentó)
- [ ] Handler `GET /api/v2/drift` — devuelve DriftFindings para cluster actual
- [ ] UI: sección "Drift" en panel lateral con badge rojo si hay `worsened`
- [ ] CLI: `upgrade-guardian-cli drift --cluster my-cluster`
- [ ] **Criterio de aceptación**: ejecutar 2 veces con mismo cluster, drift detecta recurring

### 5.3 eksctl dry-run integration (~2h)

Archivo: `internal/checks/preflight/eksctl.go`

- [ ] Si `eksctl` está en PATH: ejecutar `eksctl upgrade cluster --name X --version Y --dry-run 2>&1`
- [ ] Parsear output: líneas con `[!]` o `error` → `high` findings; líneas con `[ℹ]` → `low` informativo
- [ ] Si `eksctl` no está en PATH: finding informativo con instrucciones de instalación
- [ ] Timeout de 30s en la ejecución
- [ ] Output crudo en `meta.eksctl_output` truncado a 2KB
- [ ] **Criterio de aceptación**: detecta cuando eksctl reporta problemas en dry-run

### 5.4 Run comparison endpoint (~2h)

- [ ] Handler `POST /api/v2/compare` — recibe dos `run_id` y devuelve diff (reutilizar `internal/diff`)
- [ ] CLI: `upgrade-guardian-cli compare --run1 <id> --run2 <id>`
- [ ] Output: misma tabla de diff que `postcheck` pero entre corridas históricas
- [ ] UI: selector de "Compare with run from:" con dropdown de runs históricos
- [ ] **Criterio de aceptación**: `compare` entre run pre y post upgrade muestra findings resueltos

---

## Fase 6 — Simulación de carga (opcional, ~8h)

> Solo implementar si hay demanda explícita. No es bloqueante para las otras fases.

### 6.1 Benchmark de latencia de API server (~3h)

- [ ] Endpoint `POST /api/v2/loadtest` — lanza N goroutines haciendo requests simultáneos
- [ ] Medir p50/p95/p99 de latencia de `List` calls durante el test
- [ ] Si p95 > 500ms → `medium` finding (API server bajo presión)
- [ ] Si p95 > 2000ms → `high` (posible throttling durante drain)
- [ ] Límite de seguridad: max 100 goroutines, max 30s, max 1000 requests

### 6.2 etcd request throttling detection (~2h)

- [ ] Leer métricas Prometheus del API server (si `--enable-profiling` está activo)
- [ ] Métrica: `apiserver_request_duration_seconds_bucket` con `verb=LIST`
- [ ] Si p99 > 1s → warning
- [ ] Fallback: usar tiempo real de `List` calls del engine como proxy

### 6.3 Node pressure simulation (~3h)

- [ ] Calcular: si hago drain de cada nodo secuencialmente, ¿en algún punto el cluster queda con >90% CPU/memory?
- [ ] Extender checker `capacity` con simulación multi-drain
- [ ] Reporting: "draining node X would cause 93% memory utilization on remaining nodes"

---

## Cross-cutting tasks

### Infraestructura

- [ ] Añadir `internal/engine/categories.go` — agrupar checkers en categorías para el reporte:
  - `Core K8s` (deprecated-apis, crd-schemas, control-plane, etcd, nodes)
  - `Workloads` (workloads-readiness, webhooks-health, capacity-headroom)
  - `Storage` (csi-drivers, storage-readiness)
  - `Networking` (vpc-cni-version, subnet-ip-availability, ingress-nginx)
  - `IAM / Auth` (irsa-oidc, eks-addons)
  - `Add-ons` (helm-cves, karpenter, istio, cert-manager, alb-controller, gitops, monitoring)
  - `Pre-flight` (preflight-dryrun)
- [ ] Actualizar `internal/engine/engine.go` para pasar categoría a cada checker
- [ ] UI: agrupar CheckCards por categoría con header colapsable

### Documentación

- [ ] Crear `docs/COVERAGE.md` (ver 1.2) — ¿qué detecta y qué no?
- [ ] Crear `docs/TESTING.md` — cómo ejecutar tests de integración con Kind
- [ ] Actualizar `docs/ARCHITECTURE.md` — añadir fases 2-5, nuevos checkers
- [ ] Actualizar `docs/adr/0002-stack-tecnologico.md` — decisiones de fase 2-5
- [ ] Actualizar `README.md` — tabla de checkers con filas 14-19, roadmap actualizado

### Release

- [ ] Bump versión en `Makefile` a `v0.2.0` al completar Fase 1+2
- [ ] Bump a `v0.3.0` al completar Fase 3+4
- [ ] Generar nueva release en GitHub para cada versión
- [ ] Actualizar `docs/INSTALL.md` con nuevos RBAC requirements (EC2, EKS, IAM describe)

---

## Orden de ejecución recomendado

```
Semana 1:  1.1 (Kind tests) → 1.2 (mensajes honestos) → 1.5 (cache)
Semana 2:  2.1 (VPC CNI) → 2.2 (Subnet IPs) → 4.1 (etcd defrag) → 1.3 (matrix warning)
Semana 3:  2.3 (IRSA) → 2.4 (EKS addons) → 4.2 (CSI) → 4.3 (storage)
Semana 4:  3.1-3.5 (add-on matrices) → 3.6 (UI updates)
Semana 5:  5.1 (SQLite history) → 5.2 (drift) → 1.4 (API v2) → 4.4 (--last-mile)
           5.3 (eksctl) → 5.4 (compare) → docs finales
```

### Quick wins para esta semana (si el tiempo es limitado)

1. **2.1 VPC CNI checker** (~3h) — el fallo #1 en upgrades EKS
2. **4.1 etcd defrag** (~1h) — integración mínima en checker existente
3. **1.2 mensajes honestos** (~1h) — cambio de texto, impacto inmediato en confianza
4. **1.3 matrix warning** (~1h) — previene matrices obsoletas silenciosas

---

## Decisiones tomadas / pendientes

| Decisión | Estado | Notas |
|---|---|---|
| RAG (SQLite-vec + Qwen) | Diferido | NoopRAG en su lugar; implementar después de Fase 5 |
| SSH executor para kubeadm | Diferido | Finding informativo con comando manual |
| Docker/Helm packaging | Diferido | Tarballs son suficientes para laptop/jumpbox |
| Multi-cluster orchestration | Excluido | Scope creep; el plugin de Headlamp maneja contextos |
| Auto-remediation | Excluido permanente | Demasiado arriesgado; solo lectura + recomendaciones |
| Load testing (Fase 6) | Opcional | Solo si hay demanda explícita post-Fase 4 |

---

_Última actualización: 2026-05-25_  
_Progreso: 0/~80 tareas completadas_
