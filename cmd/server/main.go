package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"upgrade-guardian/internal/api"
	"upgrade-guardian/internal/engine"
	"upgrade-guardian/internal/rag"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	// Note: controller-runtime (pulled in by Nova/Pluto) already registers --kubeconfig
	// in its init(). We read that flag's value after Parse() to avoid a redefinition panic.
	flag.Parse()

	setupLogger(*logLevel)

	kubeconfigPath := kubeconfigFlag()
	restCfg, err := buildKubeConfig(kubeconfigPath)
	if err != nil {
		slog.Error("failed to build kube config", "error", err)
		os.Exit(1)
	}

	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		slog.Error("failed to create kube client", "error", err)
		os.Exit(1)
	}

	eng := engine.New()
	ragBackend := &rag.NoopRAG{} // replace with rag.NewSQLiteRAG(...) once LLM is configured

	handler := api.NewHandler(eng, ragBackend, kubeClient, restCfg, kubeconfigPath)
	srv := api.New(*addr, handler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.Start(); err != nil {
			slog.Error("server exited", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
}

// kubeconfigFlag returns the kubeconfig path: the --kubeconfig flag registered by
// controller-runtime, falling back to the KUBECONFIG env var.
func kubeconfigFlag() string {
	if f := flag.Lookup("kubeconfig"); f != nil {
		if v := f.Value.String(); v != "" {
			return v
		}
	}
	return os.Getenv("KUBECONFIG")
}

func buildKubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func setupLogger(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}
