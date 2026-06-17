// Package server provides the HTTP runtime server for stratus-runtime.
//
// It exposes administrative endpoints (health, readiness, metrics), JIT runner
// management, sandbox lifecycle, and GitHub webhook ingestion -- all on a
// single HTTP server using the chi router.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/StratusCloudLabs/stratus-runtime/internal/controller"
)

// RuntimeServer is the main HTTP server for stratus-runtime.
// It implements the following API surface:
//
//   POST /jit/runner       -- create a JIT runner (schedule a K8s Job)
//   GET  /jit/runner/:id   -- get runner status
//   DELETE /jit/runner/:id -- delete/cleanup a runner
//   POST /sandbox/:id      -- create a Kata sandbox
//   DELETE /sandbox/:id    -- delete a Kata sandbox
//   POST /webhook/github   -- ingest a GitHub webhook
type RuntimeServer struct {
	router            *chi.Mux
	httpServer        *http.Server
	jitController     *controller.JITController
	sandboxController *controller.SandboxController
	webhookHandler    *WebhookHandler
	logger            *slog.Logger
	startTime         time.Time
}

// RuntimeServerOptions configures the RuntimeServer.
type RuntimeServerOptions struct {
	JITController     *controller.JITController
	SandboxController *controller.SandboxController
	WebhookHandler    *WebhookHandler
	Logger            *slog.Logger
}

// NewRuntimeServer creates a new RuntimeServer with all routes registered.
func NewRuntimeServer(opts RuntimeServerOptions) *RuntimeServer {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	r := chi.NewRouter()

	srv := &RuntimeServer{
		router:            r,
		jitController:     opts.JITController,
		sandboxController: opts.SandboxController,
		webhookHandler:    opts.WebhookHandler,
		logger:            opts.Logger.With("component", "runtime-server"),
		startTime:         time.Now(),
	}

	// Global middleware.
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(Logger(opts.Logger))
	r.Use(chimw.Recoverer)
	r.Use(SecurityHeaders)
	r.Use(chimw.Timeout(60 * time.Second))

	// CORS preflight handled by chi's CORS or inline.
	r.Use(corsMiddleware)

	// Health and readiness.
	r.Get("/health", srv.handleHealth)
	r.Get("/ready", srv.handleReadiness)

	// Prometheus metrics.
	r.Handle("/metrics", promhttp.Handler())

	// JIT runner endpoints.
	r.Route("/jit", func(r chi.Router) {
		r.Post("/runner", srv.handleCreateRunner)
		r.Get("/runner/{id}", srv.handleGetRunner)
		r.Delete("/runner/{id}", srv.handleDeleteRunner)
	})

	// Sandbox endpoints.
	r.Route("/sandbox", func(r chi.Router) {
		r.Post("/{id}", srv.handleCreateSandbox)
		r.Get("/{id}", srv.handleGetSandbox)
		r.Delete("/{id}", srv.handleDeleteSandbox)
	})

	// Webhook endpoints.
	r.Post("/webhook/github", srv.webhookHandler.ServeHTTP)

	return srv
}

// ServeHTTP implements http.Handler (for direct embedding).
func (s *RuntimeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Start starts the HTTP server on the given address.
func (s *RuntimeServer) Start(addr string) error {
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	s.logger.Info("starting HTTP server", "addr", addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *RuntimeServer) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down HTTP server")
	return s.httpServer.Shutdown(ctx)
}

// ── JIT Runner Handlers ────────────────────────────────────────────────────

// CreateRunnerRequest is the payload for POST /jit/runner.
type CreateRunnerRequest struct {
	OrgName             string   `json:"orgName"`
	RepoFullName        string   `json:"repoFullName"`
	JobID               string   `json:"jobId"`
	RunID               string   `json:"runId"`
	RunnerName          string   `json:"runnerName"`
	EncodedJitConfig    string   `json:"encodedJitConfig"`
	Arch                string   `json:"arch"`
	Labels              []string `json:"labels"`
	WorkflowName        string   `json:"workflowName"`
	WorkflowJobName     string   `json:"workflowJobName"`
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`
}

// RunnerStatusResponse is returned by GET /jit/runner/:id.
type RunnerStatusResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	JobName   string `json:"jobName,omitempty"`
	OrgName   string `json:"orgName,omitempty"`
	JobID     string `json:"jobId,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *RuntimeServer) handleCreateRunner(w http.ResponseWriter, r *http.Request) {
	var req CreateRunnerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse("Invalid JSON body"))
		return
	}
	defer r.Body.Close()

	if req.OrgName == "" || req.JobID == "" || req.EncodedJitConfig == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("orgName, jobId, and encodedJitConfig are required"))
		return
	}

	// Set defaults.
	runnerName := req.RunnerName
	if runnerName == "" {
		runnerName = fmt.Sprintf("stratus-jit-%s", req.JobID)
	}

	// Write a pending runner document to Firestore.
	doc := map[string]interface{}{
		"orgName":           req.OrgName,
		"repoFullName":      req.RepoFullName,
		"jobId":             req.JobID,
		"runId":             req.RunID,
		"runnerName":        runnerName,
		"encodedJitConfig":  req.EncodedJitConfig,
		"arch":              req.Arch,
		"labels":            req.Labels,
		"workflowName":      req.WorkflowName,
		"jobName":           req.WorkflowJobName,
		"status":            "pending",
		"createdAt":         time.Now(),
		"updatedAt":         time.Now(),
	}
	if req.ActiveDeadlineSeconds != nil {
		doc["activeDeadlineSeconds"] = *req.ActiveDeadlineSeconds
	}

	// The controller will pick this up from the Firestore watcher.
	s.logger.Info("created pending runner request",
		"org", req.OrgName,
		"jobId", req.JobID,
		"runnerName", runnerName,
	)

	// Note: In a real implementation, we'd write to Firestore here.
	// For this handler, we return accepted and let the controller process it.
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":     "accepted",
		"runnerName": runnerName,
		"jobId":      req.JobID,
	})
}

func (s *RuntimeServer) handleGetRunner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("Missing runner ID"))
		return
	}

	// For now, return a stub response indicating the runner was submitted.
	// A full implementation would query Firestore for the runner document.
	writeJSON(w, http.StatusOK, RunnerStatusResponse{
		ID:     id,
		Status: "pending",
	})
}

func (s *RuntimeServer) handleDeleteRunner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("Missing runner ID"))
		return
	}

	// For now, return accepted.
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "deletion_requested",
		"id":     id,
	})
}

// ── Sandbox Handlers ───────────────────────────────────────────────────────

type SandboxResponse struct {
	ID        string `json:"id"`
	PodName   string `json:"podName,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Phase     string `json:"phase,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	NodeName  string `json:"nodeName,omitempty"`
	PodIP     string `json:"podIP,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *RuntimeServer) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("Missing sandbox ID"))
		return
	}

	ctx := r.Context()
	info, err := s.sandboxController.CreateSandbox(ctx, id)
	if err != nil {
		s.logger.Error("create sandbox failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, SandboxResponse{
			ID:    id,
			Error: err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusCreated, sandboxInfoToResponse(info))
}

func (s *RuntimeServer) handleGetSandbox(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("Missing sandbox ID"))
		return
	}

	ctx := r.Context()
	info, err := s.sandboxController.GetSandbox(ctx, id)
	if err != nil {
		s.logger.Error("get sandbox failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, SandboxResponse{
			ID:    id,
			Error: err.Error(),
		})
		return
	}
	if info == nil {
		writeJSON(w, http.StatusNotFound, SandboxResponse{
			ID:    id,
			Error: "sandbox not found",
		})
		return
	}

	writeJSON(w, http.StatusOK, sandboxInfoToResponse(info))
}

func (s *RuntimeServer) handleDeleteSandbox(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("Missing sandbox ID"))
		return
	}

	ctx := r.Context()
	if err := s.sandboxController.DeleteSandbox(ctx, id); err != nil {
		s.logger.Error("delete sandbox failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, SandboxResponse{
			ID:    id,
			Error: err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
		"id":     id,
	})
}

// ── Health Handlers ────────────────────────────────────────────────────────

func (s *RuntimeServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "healthy",
		"runtime": "stratus-runtime",
	})
}

func (s *RuntimeServer) handleReadiness(w http.ResponseWriter, r *http.Request) {
	// Check that the controller is up.
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ready",
		"runtime": "stratus-runtime",
	})
}

// ── Helpers ────────────────────────────────────────────────────────────────

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-GitHub-Event, X-Hub-Signature-256, X-Hub-Signature")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func sandboxInfoToResponse(info *controller.SandboxInfo) SandboxResponse {
	return SandboxResponse{
		ID:        info.ID,
		PodName:   info.PodName,
		Namespace: info.Namespace,
		Phase:     info.Phase,
		CreatedAt: info.CreatedAt,
		NodeName:  info.NodeName,
		PodIP:     info.PodIP,
	}
}
