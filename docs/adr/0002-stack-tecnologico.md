# ADR-0002: Stack tecnológico — herramientas específicas por dominio

| Campo | Valor |
|---|---|
| Estado | Aceptado |
| Fecha | 2026-05-23 (revisado 2026-05-25 para añadir checkers 8-13 y empaquetado) |
| Autores | Equipo Upgrade Guardian |
| Depende de | ADR-0001 (detección determinista) |

---

## Contexto

Upgrade Guardian ejecuta 13 categorías de validación contra un clúster Kubernetes live. Las primeras 7 son MVP (decididas en la versión original de este ADR); las 6 siguientes se agregaron en la revisión 2026-05-25 al detectar gaps de cobertura tras experiencias reales de upgrades. Para cada categoría existe más de una opción técnica. Las decisiones de stack son "innegociables" en el sentido de que deben justificarse con criterios deterministas y no pueden cambiarse a herramientas de best-practices que generen opiniones subjetivas.

El sistema debe funcionar como **librería Go nativa**, no como orquestador de CLIs externos. Invocar `kubectl`, `pluto`, `nova` o `kubeadm` como subprocesos introduciría dependencias de PATH, versiones de binario no controladas, y haría imposible el testing unitario con clústeres Kind.

---

## Decisiones por dominio

### APIs Deprecated — Pluto v5

**Alternativas evaluadas**: Kubent, API server audit logs, scan manual de manifiestos.

**Por qué Pluto**: es la única herramienta con paquete Go importable (`github.com/fairwindsops/pluto/v5/pkg/`) que mantiene una base de datos de APIs deprecated/removed por versión de Kubernetes. Kubent solo expone CLI. Los audit logs requieren retención y no son síncronos. El scan de manifiestos no cubre recursos en `etcd` que no están en Git.

**Implementación**: se usa la base de datos de Pluto (`versions.yaml`, ~730 entradas) pero **no** su mecanismo de discovery. En lugar de `GetApiResources()` (que solo lee objetos con la anotación `kubectl.kubernetes.io/last-applied-configuration`), el checker usa `discovery.ServerPreferredResources()` + `dynamic.Interface.List()` para iterar todos los objetos del clúster y leer su `apiVersion` directamente. Esto garantiza que recursos desplegados via Helm, operadores u otros métodos sin `kubectl apply` también son escaneados.

**Cobertura de la base**: la base de Pluto es embebida en el binario en tiempo de compilación. El endpoint `GET /api/v1/versions` calcula el `max_k8s` cubierto y emite un warning si el target excede esa cobertura. Ejecutar `make update-deps` antes de cada ciclo de upgrades.

**Limitación conocida**: Pluto registra un `--kubeconfig` flag en `init()` a través de `controller-runtime`. Mitigación: no redefinir ese flag en `main.go`; leerlo post-`flag.Parse()` con `flag.Lookup("kubeconfig")`.

---

### Helm/CVEs — Nova (fairwindsops)

**Alternativas evaluadas**: `helm list` + consulta manual a Artifact Hub, Trivy, Grype.

**Por qué Nova**: es la única librería Go que enumera releases Helm del clúster live y los compara contra versiones disponibles upstream, con soporte para `KubeVersion` constraint del chart. Trivy y Grype operan sobre imágenes de contenedor, no sobre releases Helm.

**Limitación conocida**: Nova es pre-release en el módulo Go (no publica sufijos `/v3`). Se importa como `github.com/fairwindsops/nova` con tag de fecha. Si el módulo cambia de naming en el futuro, actualizar `go.mod`.

---

### CRDs — kubeconform

**Alternativas evaluadas**: `kubectl --dry-run=server`, kubeval (deprecated), cel-admission-library.

**Por qué kubeconform**: valida recursos contra schemas JSON oficiales de kubernetes/kubernetes y puede recibir bytes arbitrarios sin necesidad de un clúster activo (modo offline). Se puede construir un `validator.Validator` con inyección de opciones. kubeval está abandonado. `kubectl --dry-run=server` no da información sobre _por qué_ falla un schema.

**Limitación conocida**: CRDs de terceros (Dapr, Argo, Cert-Manager) no tienen schema en el registry por defecto de kubeconform → resultado `Error`, no `Invalid`. Se mapea correctamente a `medium` no-blocker, no a `critical`.

---

### Control Plane — HTTP directo + crypto/tls

**Alternativas evaluadas**: kubeadm preflight (librería), EKS Insights API.

**Por qué HTTP directo**: `kubeadm` no expone sus preflight checks como paquete Go público importable sin efectos secundarios. Hacerlo requeriría fork o reflección. Las endpoints `/healthz`, `/readyz`, `/livez` del API server son parte del contrato oficial de Kubernetes y suficientes para el MVP.

**EKS Insights**: pendiente de implementar. La API `DescribeAddonVersions` del SDK EKS es la fuente autoritativa para add-ons gestionados. El checker actual devuelve `not yet implemented` para el path EKS.

---

### etcd — etcd client v3

**Alternativas evaluadas**: `etcdutl` como subproceso, `etcdctl` como subproceso, lectura de métricas Prometheus.

**Por qué cliente v3 nativo**: `go.etcd.io/etcd/client/v3` es el cliente oficial. `cli.Status()` y `cli.AlarmList()` dan información precisa sobre salud y alarmas activas sin dependencias de PATH. Las métricas Prometheus requieren que el endpoint esté expuesto y no garantizan presencia de alarmas.

**Limitación conocida**: los certificados TLS estándar están en `/etc/kubernetes/pki/etcd/` (kubeadm). En Kind o clústeres remotos, los certs no son accesibles → degradación grácil con finding `medium` no-blocker + comando `etcdctl` manual.

**Skipped para EKS**: el etcd de EKS es gestionado por AWS y no es accesible. `Supports(ClusterTypeEKS)` devuelve `false`.

---

### Nodos — NodeConditions + NPD + kubelet version skew

**Alternativas evaluadas**: node-problem-detector como DaemonSet obligatorio, Prometheus node-exporter.

**Por qué NodeConditions**: las condiciones de Node Problem Detector se reflejan como `NodeCondition` adicionales en el objeto `Node` cuando NPD está desplegado. El checker puede leerlas vía `CoreV1().Nodes().List()` sin importar si NPD está presente o no — si las condiciones no existen, simplemente no hay findings. No se requiere NPD como dependencia del deployment.

**Kubelet version skew**: el checker valida que el kubelet de cada nodo esté dentro de la política oficial de Kubernetes (máximo 2 minor versions de diferencia respecto al API server target). Un skew mayor es blocker. La versión del kubelet se lee de `node.Status.NodeInfo.KubeletVersion`.

**Instalación one-click de NPD**: si NPD no está desplegado, el finding incluye un botón en la UI que llama a `POST /api/v1/npd/install` para desplegarlo vía DaemonSet y re-ejecuta los checks automáticamente.

---

### Provider (CNI/CSI/IAM) — parsers + SDKs específicos

**Alternativas evaluadas**: compatibility matrix genérica, Helm chart metadata, etiquetas de imagen.

**Por qué parsers específicos**:
- **CNI**: detectado por DaemonSet name en `kube-system` (enfoque robusto ante distintos métodos de instalación). La matriz de compatibilidad se mantiene manualmente a partir de los changelogs oficiales de Calico, Cilium, Flannel.
- **EKS add-ons**: `aws-sdk-go-v2/service/eks.ListAddons` + `DescribeAddonVersions` es la única fuente autoritativa para versiones de VPC CNI, kube-proxy, CoreDNS gestionados. No se asume comportamiento upstream.
- **Kubespray**: las versiones de componentes se leen de `group_vars/all/all.yml` y `k8s_cluster/k8s-cluster.yml`. Nunca de `kubectl` — el inventario es la fuente de verdad para Kubespray.
- **Weave Net**: EOL desde 2023. Cualquier presencia se reporta como `critical/blocker` independientemente de la versión.

---

### RAG — SQLite-vec + bge-m3 + Qwen2.5-Coder-32B

**Alternativas evaluadas**: OpenAI API, Chroma, Pinecone, PostgreSQL + pgvector.

**Por qué local**:
1. Los clústeres objetivo pueden ser on-premise sin acceso a internet.
2. Los documentos indexados son RRHH internos (changelogs, runbooks, post-mortems) que no pueden enviarse a APIs externas.
3. SQLite con la extensión `sqlite-vec` elimina dependencias de infraestructura externa para el vector store.

**Por qué Qwen2.5-Coder-32B**: entrenado específicamente en código y documentación técnica. Su contexto de 32K tokens permite ingerir changelogs completos. Se sirve localmente vía Ollama.

**Estado actual**: implementado como `NoopRAG` (interfaz definida, implementación pendiente). El servidor expone el endpoint `POST /api/v1/rag/query` listo para cuando se configure el backend LLM.

---

### Workloads — client-go directo (sin librerías externas)

**Alternativas evaluadas**: kube-score (ya excluido), kube-no-trouble, OPA/Gatekeeper.

**Por qué client-go directo**:
- Las cuatro validaciones (PDB blockers, single-replica, missing probes, broken pods) son lógica de negocio simple sobre estructuras k8s nativas. No requieren librerías especializadas.
- kube-score solapa con Polaris (excluido — best practices subjetivas).
- OPA/Gatekeeper requiere policies del usuario; queremos validación lista para usar.

**Validaciones implementadas**:
1. **PDB `maxUnavailable: 0`** o **`minAvailable >= expectedPods`** → blocker (drain imposible).
2. **Deployment/StatefulSet con `replicas: 1`** en namespaces no-system → high.
3. **Containers sin `readinessProbe`** en workloads de más de 2 réplicas → medium.
4. **Pods en `Pending(Unschedulable)`, `CrashLoopBackOff`, `ImagePullBackOff`** → high+blocker.

---

### Webhooks — admissionregistration/v1 + crypto/x509

**Alternativas evaluadas**: client-go con `WebhookConfigurationLister`, tooling como kube-bench.

**Por qué admissionregistration/v1 + x509 manual**:
- `ValidatingWebhookConfigurations().List()` y `MutatingWebhookConfigurations().List()` exponen todo lo necesario.
- Para CA expiry: `pem.Decode` + `x509.ParseCertificate` de la CA bundle. Soporta tanto PEM como DER.
- Para reachability: lookup de `Service` + verificación de `Endpoints` (no se intenta TCP dial porque el backend pod normalmente no tiene network access equivalent al apiserver).

**Por qué blockear solo cuando `failurePolicy: Fail`**: webhooks con `failurePolicy: Ignore` que están caídos no rompen el upgrade — solo se ignoran sus reglas.

---

### Capacity — simulación de drain con requests sumados

**Alternativas evaluadas**: cluster-autoscaler simulator (descheduler), kube-capacity, metrics-server.

**Por qué simulación propia**:
- `metrics-server` reporta **uso real**, no `requests`. Para validar si un drain genera pods Pending, lo que importa son los `requests` (que el scheduler usa para decidir).
- cluster-autoscaler simulator es una librería interna de Karpenter/CAS, no consumible.
- Algoritmo simple: para el nodo más cargado por `requests`, simular su drain y verificar si la suma de sus `requests` cabe en `allocatable - requests` de los nodos restantes.

**Limitaciones aceptadas**: no considera taints/tolerations, node affinity, ni topology spread constraints. Simulación conservadora (peor caso de suma de requests). Single-node clusters → skip explícito.

---

### Preflight — AWS SDK v2 (EKS Insights) + parsers Kubespray

**Alternativas evaluadas**: ejecutar `kubeadm upgrade plan` vía SSH, ejecutar `ansible-playbook --check`, integración directa con eksctl.

**Por qué EKS Insights API**:
- AWS publica `ListInsights` específicamente para validación pre-upgrade. Es la única fuente autoritativa para problemas EKS-specific (IAM, addon versions, etc.).
- No requiere shell ni acceso al cluster control plane.

**Por qué no SSH a control plane para kubeadm**:
- El backend pod típicamente no tiene credenciales SSH ni network access al control plane.
- `kubeadm upgrade plan` requiere root y kubeadm config local.
- En su lugar: finding informativo con el comando exacto a ejecutar manualmente.

**Por qué Kubespray solo valida inventory**:
- Ejecutar `ansible-playbook --check` requiere Ansible instalado + credenciales SSH al inventario.
- La validación pragmática: comparar `kube_version` en `group_vars/` con el target. Si difieren, blocker.

---

### Third-party compatibility matrices — estáticas verificadas

**Alternativas evaluadas**: API endpoints upstream (no existen), parsing automático de docs HTML, hardcoded sin verificación.

**Por qué matrices estáticas verificadas manualmente**:
- Karpenter (`karpenter.sh/docs/upgrading/compatibility/`) y Istio (`istio.io/.../supported-releases/`) publican sus matrices solo como tablas HTML. No hay endpoint JSON/YAML estable.
- Parsing HTML es frágil — el HTML cambia con cada redesign de la documentación.
- Las matrices se actualizan ~1-2 veces por trimestre. La carga operativa es trivial.

**Protocolo de actualización**:
1. WebFetch sobre la URL upstream con prompt específico (preservado en chat history).
2. Editar el `compatibilityMatrix` en `internal/checks/<tool>/checker.go`.
3. Bumpear el comment `LAST VERIFIED: YYYY-MM-DD`.
4. Correr `go test ./internal/checks/<tool>/...`.

**Por qué un checker por tool (Karpenter + Istio separados)**:
- Single responsibility: cada uno con su matriz, lógica de detección, y findings específicos.
- Skipping cuando la herramienta no está instalada es trivial.
- Add-ons futuros (cert-manager, external-dns, ingress-nginx) siguen el mismo patrón.

---

### Empaquetado — multi-arch tarballs + install.sh

**Alternativas evaluadas**: Docker image + Helm chart, Headlamp plugin marketplace, deb/rpm packages.

**Por qué tarballs**:
- Cero dependencias en la máquina target (binarios statically-linked).
- Multi-arch nativo con cross-compilación de Go (4 plataformas, ~30s build total).
- `install.sh` maneja systemd (Linux) y launchd (macOS) automáticamente.

**Por qué no Docker/Helm primero**:
- El operador típico ejecuta esto en su laptop o en una jump box, no necesariamente in-cluster.
- Docker + Helm queda como opción futura (descrita en INSTALL.md) si se requiere multi-tenant.

---

### CLI — Go binario separado que envuelve la API

**Alternativas evaluadas**: subcomando del backend (`upgrade-guardian check ...`), shell script con `curl`, cliente embebido en plugin.

**Por qué binario standalone**:
- Separación clara de responsabilidades: el server hace I/O contra el cluster; el CLI hace I/O contra el server.
- Permite invocarse desde CI/CD sin exponer kubeconfig del operador (el server puede ejecutarse en otra máquina/contenedor).
- Output configurable (table/JSON/CSV) sin contaminar la lógica del server.
- Exit codes apropiados para pipelines (`0` = OK, `1` = blockers/falla post-upgrade).

---

## Herramientas explícitamente excluidas del MVP

| Herramienta | Categoría | Razón de exclusión |
|---|---|---|
| Polaris | Best practices | Genera opiniones subjetivas, no bloqueantes de upgrade |
| Popeye | Best practices | Ídem |
| Kubent | APIs deprecated | Solo CLI, no librería Go |
| Trivy / Grype | CVEs | Opera sobre imágenes, no sobre releases Helm |
| OPA/Gatekeeper | Policy | Requiere configuración específica del usuario |
| Falco | Runtime security | Fuera del scope de upgrade validation |

---

## Referencias

- [Pluto releases](https://github.com/fairwindsops/pluto/releases)
- [Nova repository](https://github.com/fairwindsops/nova)
- [kubeconform schema registry](https://github.com/yannh/kubernetes-json-schema)
- [etcd client v3 docs](https://pkg.go.dev/go.etcd.io/etcd/client/v3)
- [AWS SDK for Go v2 — EKS](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/eks)
- [Karpenter compatibility matrix](https://karpenter.sh/docs/upgrading/compatibility/) — source of truth para checker 12
- [Istio supported releases](https://istio.io/latest/docs/releases/supported-releases/) — source of truth para checker 13
- [Kubernetes version skew policy](https://kubernetes.io/releases/version-skew-policy/) — base para `node-health` kubelet skew detection
- [PodDisruptionBudget docs](https://kubernetes.io/docs/concepts/workloads/pods/disruptions/) — base para `workloads-readiness`
- [sqlite-vec extension](https://github.com/asg017/sqlite-vec)
