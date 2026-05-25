package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"upgrade-guardian/internal/checker"
	"upgrade-guardian/internal/detector"
	"upgrade-guardian/internal/diff"
	"upgrade-guardian/internal/engine"
	"upgrade-guardian/internal/npd"
	"upgrade-guardian/internal/rag"
	"upgrade-guardian/internal/versions"
)

// Handler holds the shared dependencies for all HTTP handlers.
type Handler struct {
	engine         *engine.Engine
	rag            rag.RAG
	defaultClient  kubernetes.Interface
	defaultConfig  *rest.Config
	kubeconfigPath string // used to build per-context clients
}

// NewHandler wires the engine and RAG backend into the handler.
func NewHandler(eng *engine.Engine, r rag.RAG, kc kubernetes.Interface, rc *rest.Config, kubeconfigPath string) *Handler {
	return &Handler{
		engine:         eng,
		rag:            r,
		defaultClient:  kc,
		defaultConfig:  rc,
		kubeconfigPath: kubeconfigPath,
	}
}

// clientForContext returns a kube client and rest config for the given context name.
// If context is empty it returns the default client built at startup.
func (h *Handler) clientForContext(context string) (kubernetes.Interface, *rest.Config, error) {
	if context == "" {
		return h.defaultClient, h.defaultConfig, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if h.kubeconfigPath != "" {
		loadingRules.ExplicitPath = h.kubeconfigPath
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: context},
	).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("build config for context %q: %w", context, err)
	}

	kc, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create client for context %q: %w", context, err)
	}
	return kc, cfg, nil
}

// RunChecks runs all applicable checkers for the requested version upgrade.
// GET /api/v1/check?from=1.29&to=1.30&context=kind-my-cluster
func (h *Handler) RunChecks(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "query params 'from' and 'to' are required")
		return
	}

	kubeClient, restConfig, err := h.clientForContext(r.URL.Query().Get("context"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg := &checker.CheckConfig{
		CurrentVersion: from,
		TargetVersion:  to,
		KubeClient:     kubeClient,
		RestConfig:     restConfig,
	}

	// Propagate EKS / Kubespray config from request headers if provided.
	if cluster := r.Header.Get("X-Cluster-Name"); cluster != "" {
		cfg.EKSConfig = &checker.EKSConfig{
			ClusterName: cluster,
			Region:      r.Header.Get("X-AWS-Region"),
		}
	}
	if inv := r.Header.Get("X-Kubespray-Inventory"); inv != "" {
		cfg.KubesprayConfig = &checker.KubesprayConfig{
			InventoryPath: inv,
			GroupVarsPath: r.Header.Get("X-Kubespray-GroupVars"),
		}
	}

	report, err := h.engine.Run(r.Context(), cfg)
	if err != nil {
		slog.Error("engine.Run failed", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, report)
}

// GetCluster returns the detected cluster type and server version.
// GET /api/v1/cluster?context=kind-my-cluster
func (h *Handler) GetCluster(w http.ResponseWriter, r *http.Request) {
	kubeClient, _, err := h.clientForContext(r.URL.Query().Get("context"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ct, err := detector.Detect(r.Context(), kubeClient)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	sv, err := kubeClient.Discovery().ServerVersion()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"cluster_type": string(ct),
		"version":      sv.GitVersion,
		"platform":     sv.Platform,
	})
}

// RAGQuery translates a validated finding into operator-friendly language.
// POST /api/v1/rag/query
// Body: { "query": "...", "provider": "eks", "version_range": "1.29-1.30" }
func (h *Handler) RAGQuery(w http.ResponseWriter, r *http.Request) {
	var req rag.QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "'provider' is required (eks|upstream|kubespray)")
		return
	}

	resp, err := h.rag.Query(r.Context(), &req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// InstallNPD deploys node-problem-detector into kube-system if not already present.
// POST /api/v1/npd/install?context=kind-my-cluster
func (h *Handler) InstallNPD(w http.ResponseWriter, r *http.Request) {
	kubeClient, _, err := h.clientForContext(r.URL.Query().Get("context"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := npd.Install(r.Context(), kubeClient)
	if err != nil {
		slog.Error("npd.Install failed", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GetVersions reports bundled tool versions and database coverage.
// GET /api/v1/versions?target=1.35
func (h *Handler) GetVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, versions.Get(r.URL.Query().Get("target")))
}

// UpdateVersions downloads the latest Pluto versions.yaml from GitHub and caches it.
// POST /api/v1/versions/update
func (h *Handler) UpdateVersions(w http.ResponseWriter, r *http.Request) {
	result, err := versions.UpdatePluto()
	if err != nil {
		slog.Error("versions.UpdatePluto failed", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// PostCheck re-runs all checkers and diffs the result against a previous report
// supplied in the request body. The body shape is:
//
//	{ "pre_report": { ...Report... }, "from": "1.34", "to": "1.35", "context": "kind-..." }
//
// Response is a diff.Result. Useful to confirm an upgrade was successful.
// POST /api/v1/postcheck
func (h *Handler) PostCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PreReport *checker.Report `json:"pre_report"`
		From      string          `json:"from"`
		To        string          `json:"to"`
		Context   string          `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.PreReport == nil {
		writeError(w, http.StatusBadRequest, "'pre_report' is required")
		return
	}
	if body.From == "" || body.To == "" {
		writeError(w, http.StatusBadRequest, "'from' and 'to' are required")
		return
	}

	kubeClient, restConfig, err := h.clientForContext(body.Context)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg := &checker.CheckConfig{
		CurrentVersion: body.From,
		TargetVersion:  body.To,
		KubeClient:     kubeClient,
		RestConfig:     restConfig,
	}
	post, err := h.engine.Run(r.Context(), cfg)
	if err != nil {
		slog.Error("engine.Run (postcheck) failed", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, diff.Compute(body.PreReport, post))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
