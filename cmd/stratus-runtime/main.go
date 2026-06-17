// stratus-runtime is the Go-based runtime for Stratus Cloud.
//
// It combines:
//   - A JIT (Just-In-Time) runner controller that watches Firestore for
//     pending runner requests and creates K8s Jobs
//   - A sandbox controller for Kata Container lifecycle management
//   - An HTTP server with health, readiness, Prometheus metrics, JIT runner
//     management, sandbox management, and GitHub webhook ingestion
//
// All components share a single binary with a Dockerfile for deployment.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"firebase.google.com/go/v4"
	"google.golang.org/api/option"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/StratusCloudLabs/stratus-runtime/internal/config"
	"github.com/StratusCloudLabs/stratus-runtime/internal/controller"
	"github.com/StratusCloudLabs/stratus-runtime/internal/metrics"
	"github.com/StratusCloudLabs/stratus-runtime/internal/server"
)

func main() {
	// Parse configuration.
	cfg := config.Load()

	// Set up structured logging.
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))
	logger := slog.Default()

	logger.Info("starting stratus-runtime",
		"version", cfg.Version,
		"port", cfg.Port,
		"metrics_port", cfg.MetricsPort,
	)

	// Validate configuration.
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Context for background workers.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Initialize GCP / Firestore ─────────────────────────────────────────
	var firestoreClient *firestore.Client

	initFirestore := func() {
		var opts []option.ClientOption
		if cfg.CredentialsFile != "" {
			opts = append(opts, option.WithCredentialsFile(cfg.CredentialsFile))
		}

		app, err := firebase.NewApp(ctx, &firebase.Config{
			ProjectID: cfg.ProjectID,
		}, opts...)
		if err != nil {
			logger.Warn("firebase init failed (non-fatal)", "error", err)
			return
		}

		firestoreClient, err = app.Firestore(ctx)
		if err != nil {
			logger.Warn("firestore init failed (non-fatal)", "error", err)
			firestoreClient = nil
		}
	}
	initFirestore()

	// ── Initialize Kubernetes client ───────────────────────────────────────
	var k8sClient kubernetes.Interface

	initK8s := func() {
		var k8sConfig *rest.Config
		var err error

		if cfg.KubeConfigPath != "" {
			k8sConfig, err = clientcmd.BuildConfigFromFlags("", cfg.KubeConfigPath)
		} else {
			// Try in-cluster config first, then default kubeconfig paths.
			k8sConfig, err = rest.InClusterConfig()
			if err != nil {
				k8sConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
					clientcmd.NewDefaultClientConfigLoadingRules(),
					&clientcmd.ConfigOverrides{},
				).ClientConfig()
			}
		}
		if err != nil {
			logger.Warn("k8s config init failed (non-fatal)", "error", err)
			return
		}

		k8sClient, err = kubernetes.NewForConfig(k8sConfig)
		if err != nil {
			logger.Warn("k8s client init failed (non-fatal)", "error", err)
			k8sClient = nil
		}
	}
	initK8s()

	// ── Initialize Controllers ─────────────────────────────────────────────

	// JIT controller.
	var jitCtrl *controller.JITController
	if firestoreClient != nil && k8sClient != nil {
		jitCtrl = controller.NewJITController(
			firestoreClient,
			k8sClient,
			cfg.RunnerNamespace,
			cfg.GHCRBase,
			cfg.GHCRSecret,
			cfg.InstallationID,
			cfg.RetryBaseInterval,
			cfg.RetryMaxInterval,
			logger,
		)

		// Start the JIT controller in the background.
		go jitCtrl.StartController(ctx,
			cfg.StartupRecoveryThreshold,
			30*time.Second, // retry interval
		)
		logger.Info("JIT controller started")
	} else {
		logger.Warn("JIT controller disabled (Firestore or K8s unavailable)")
	}

	// Sandbox controller.
	var sandboxCtrl *controller.SandboxController
	if k8sClient != nil {
		sandboxCtrl = controller.NewSandboxController(
			k8sClient,
			"",
			cfg.SandboxImage,
			logger,
		)
		logger.Info("sandbox controller initialized")
	} else {
		logger.Warn("sandbox controller disabled (K8s unavailable)")
	}

	// ── Metrics Push Loop ──────────────────────────────────────────────────
	if firestoreClient != nil && cfg.InstallationID != "" {
		hostname, _ := os.Hostname()
		pusher := metrics.NewClusterMetricsPusher(
			firestoreClient,
			k8sClient,
			cfg.RunnerNamespace,
			cfg.InstallationID,
			cfg.Version,
			hostname,
			logger,
		)
		go pusher.StartPushLoop(ctx, cfg.MetricsPushInterval)
	} else {
		logger.Warn("metrics push loop disabled (Firestore unavailable or INSTALLATION_ID not set)")
	}

	// ── Job Reaper ─────────────────────────────────────────────────────────
	if firestoreClient != nil && k8sClient != nil {
		reaper := controller.NewReaper(
			firestoreClient,
			k8sClient,
			cfg.RunnerNamespace,
			logger,
		)
		go reaper.StartReaperLoop(ctx, cfg.ReaperInterval, cfg.MinScheduledAge)
	} else {
		logger.Warn("job reaper disabled (Firestore or K8s unavailable)")
	}

	// ── Webhook Handler ────────────────────────────────────────────────────
	var webhookHandler *server.WebhookHandler
	if cfg.WebhookSecret != "" {
		webhookHandler = server.NewWebhookHandler(
			cfg.WebhookSecret,
			cfg.WebhookUpstreamURL(),
			logger,
		)
	} else {
		logger.Warn("webhook handler running without secret validation")
		webhookHandler = server.NewWebhookHandler("", cfg.WebhookUpstreamURL(), logger)
	}

	// ── HTTP Server ────────────────────────────────────────────────────────
	runtimeServer := server.NewRuntimeServer(server.RuntimeServerOptions{
		JITController:     jitCtrl,
		SandboxController: sandboxCtrl,
		WebhookHandler:    webhookHandler,
		Logger:            logger,
	})

	// Start main HTTP server in background.
	go func() {
		addr := fmt.Sprintf(":%d", cfg.Port)
		logger.Info("HTTP server starting", "addr", addr)
		if err := runtimeServer.Start(addr); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
			cancel()
		}
	}()

	// Start a separate metrics-only HTTP server on the metrics port.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	metricsServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler: metricsMux,
	}

	go func() {
		logger.Info("metrics server starting", "addr", metricsServer.Addr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()

	// ── Graceful Shutdown ──────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	logger.Info("received signal, shutting down", "signal", sig)
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	// Shutdown HTTP servers.
	if err := runtimeServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("runtime server shutdown error", "error", err)
	}
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("metrics server shutdown error", "error", err)
	}

	// Close Firestore client.
	if firestoreClient != nil {
		firestoreClient.Close()
	}

	logger.Info("stratus-runtime stopped")
}
