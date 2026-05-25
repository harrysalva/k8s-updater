# ADR-0001: Detección exclusivamente determinista — LLMs prohibidos para generar findings

| Campo | Valor |
|---|---|
| Estado | Aceptado |
| Fecha | 2026-05-23 |
| Autores | Equipo Upgrade Guardian |
| Sustituye | — |

---

## Contexto

Upgrade Guardian valida si un clúster Kubernetes está listo para un upgrade de versión. El sistema necesita emitir findings de severidad crítica — algunos de los cuales bloquean el upgrade si no se resuelven. En este dominio, un falso positivo tiene coste operativo alto: detiene una ventana de mantenimiento planificada y fuerza a los operadores a investigar un problema inexistente. Un falso negativo tiene coste aún mayor: el operador hace el upgrade y el clúster queda degradado.

Se evaluó si los LLMs podían participar en la fase de _detección_ (analizar el estado del clúster y decidir qué constituye un problema), argumentando que modelos como Qwen2.5-Coder-32B tienen conocimiento extenso sobre Kubernetes y podrían detectar patrones sutiles que las herramientas estáticas no cubren.

---

## Decisión

**Los LLMs tienen terminantemente prohibido generar, inferir o enriquecer findings.** Un finding solo puede ser producido por un checker determinista: una herramienta estática (Pluto, Nova, kubeconform) o una llamada directa a la API de Kubernetes con criterio de evaluación hardcodeado.

Los LLMs se permiten exclusivamente en la capa RAG para dos tareas:

1. **Traducir** un finding ya validado a lenguaje operacional amigable.
2. **Priorizar** (reordenar) findings dentro de un resultado ya cerrado.

En ningún caso puede la capa RAG agregar, modificar, suprimir ni ampliar el conjunto de findings.

---

## Consecuencias

### Positivas

**Cero falsos positivos fabricados por el modelo.** Cuando un checker no encuentra evidencia de un problema, no reporta nada. Los LLMs no pueden "intuir" que algo podría estar mal.

**Resultados reproducibles.** El mismo clúster con el mismo par `from/to` siempre produce el mismo conjunto de findings, independientemente de la temperatura del modelo, el estado del contexto o cambios en los pesos del LLM.

**Auditable.** Cada finding tiene `source` (herramienta que lo detectó) y `docs_url` (referencia oficial). El operador puede reproducir manualmente la detección.

**Sin dependencia de disponibilidad del LLM en el path crítico.** Si la capa RAG no está configurada (`NoopRAG`), el sistema sigue siendo completamente funcional — los 7 checkers se ejecutan igual. El LLM es un enhancer opcional, no un componente requerido.

**Compliance y auditabilidad.** En entornos regulados, la cadena de evidencia es trazable: `finding.source = "pluto"` + `finding.docs_url = "https://kubernetes.io/docs/reference/using-api/deprecation-guide/"`. No hay "el modelo dijo que...".

### Negativas

**Cobertura acotada.** Solo se detectan los problemas que los checkers deterministas saben buscar. Problemas sutiles de configuración que no entran en ninguna de las 7 categorías MVP no se detectan.

**Mantenimiento de la matriz de compatibilidad.** La tabla CNI/CSI/IAM en `provider/checker.go` debe actualizarse manualmente con cada release de Kubernetes, Calico, Cilium, etc.

**No hay aprendizaje de incidentes.** Si un operador documenta un problema real que no fue detectado, ese conocimiento no se incorpora automáticamente — requiere un checker explícito o una entrada de RAG.

---

## Alternativas consideradas

### A: LLM como detector primario + herramientas como verificación

El LLM analiza el clúster y propone findings; Pluto/Nova los validan. Se descartó porque la validación de Pluto no cubre el espacio de lo que el LLM podría proponer, dejando un gap de findings sin validar que igualmente llegarían al operador.

### B: LLM como detector de segunda opinión (solo para confirmar, no para bloquear)

El LLM podría agregar findings de severidad `info` no-blocker. Se descartó porque incluso findings informativos incorrectos generan ruido operacional y erosionan la confianza en la herramienta.

### C: LLM entrenado solo con datos de incidentes verificados (fine-tuning)

Un modelo fine-tuned sobre changelogs, CVEs y post-mortems verificados podría tener precisión más alta. Se descartó para el MVP por complejidad y porque no resuelve el problema raíz: la salida del modelo sigue siendo probabilística.

---

## Reglas derivadas de esta decisión

Estas reglas son invariantes del sistema y no pueden ser modificadas sin un nuevo ADR:

1. El tipo `Finding` solo puede ser construido dentro de un paquete bajo `internal/checks/`.
2. La capa `internal/rag/` no tiene acceso al tipo `checker.Finding` — solo recibe queries de texto y devuelve explicaciones de texto.
3. El endpoint `POST /api/v1/rag/query` no puede modificar ni ampliar el `Report` que ya fue generado por el engine.
4. Cada `Chunk` indexado en el RAG debe tener `provider`, `version_range` y `source_url` obligatorios. La ausencia de cualquiera de los tres rechaza el chunk en `IndexChunk()`.
5. La retrieval del RAG siempre aplica `WHERE provider = ?` antes de la búsqueda por similaridad — impide que documentación EKS contamine respuestas para clústeres Kubespray y viceversa.

---

## Referencias

- [Kubernetes API Deprecation Policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)
- [Pluto — API version finder](https://github.com/fairwindsops/pluto)
- [Nova — Helm outdated chart checker](https://github.com/fairwindsops/nova)
- [kubeconform — Kubernetes manifest validator](https://github.com/yannh/kubeconform)
