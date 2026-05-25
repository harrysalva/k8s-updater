package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Server wraps the HTTP server lifecycle.
type Server struct {
	http *http.Server
}

// New creates a Server with all routes registered.
func New(addr string, h *Handler) *Server {
	mux := http.NewServeMux()
	registerRoutes(mux, h)

	return &Server{
		http: &http.Server{
			Addr:         addr,
			Handler:      corsMiddleware(mux),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second, // checks can take a while
			IdleTimeout:  60 * time.Second,
		},
	}
}

func registerRoutes(mux *http.ServeMux, h *Handler) {
	// Check runs (long-running, up to 2 min).
	mux.HandleFunc("GET /api/v1/check", h.RunChecks)

	// Re-run checks and diff against a prior report (post-upgrade verification).
	mux.HandleFunc("POST /api/v1/postcheck", h.PostCheck)

	// Cluster info (fast).
	mux.HandleFunc("GET /api/v1/cluster", h.GetCluster)

	// RAG explanation for a specific finding.
	mux.HandleFunc("POST /api/v1/rag/query", h.RAGQuery)

	// Install node-problem-detector into kube-system (idempotent).
	mux.HandleFunc("POST /api/v1/npd/install", h.InstallNPD)

	// Tool database versions and coverage report.
	mux.HandleFunc("GET /api/v1/versions", h.GetVersions)
	// Download latest Pluto versions.yaml from GitHub.
	mux.HandleFunc("POST /api/v1/versions/update", h.UpdateVersions)

	// Liveness probe for the backend pod.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func (s *Server) Start() error {
	slog.Info("upgrade-guardian backend listening", "addr", s.http.Addr)
	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// corsMiddleware allows the Headlamp frontend (different port) to call the backend.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
