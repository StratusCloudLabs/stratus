// Package metrics provides Prometheus metric definitions and registration
// for the stratus-runtime components.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// JIT runner metrics.
var (
	DispatchQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratus_dispatch_queue_depth",
		Help: "Number of JIT runner requests queued waiting for cluster capacity",
	}, []string{"label", "arch"})

	DispatchQueueAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratus_dispatch_queue_age_seconds",
		Help: "Age in seconds of the oldest queued JIT runner request",
	}, []string{"arch"})

	DispatchCapacityCheckFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stratus_dispatch_capacity_check_failures_total",
		Help: "Total number of times a dispatch was deferred due to insufficient cluster capacity",
	}, []string{"label", "arch"})

	JobsCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stratus_jobs_created_total",
		Help: "Total number of K8s Jobs created",
	})

	JobsReapedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stratus_jobs_reaped_total",
		Help: "Total number of completed runner Jobs reaped",
	})

	JobsFailedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stratus_jobs_failed_total",
		Help: "Total number of K8s Jobs that failed to be created",
	})

	JobCreationDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratus_job_creation_duration_seconds",
		Help:    "Duration of K8s Job creation requests",
		Buckets: prometheus.DefBuckets,
	})

	ControllerClaimsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stratus_controller_claims_total",
		Help: "Total number of pendingRunner documents claimed",
	})
)

// Runtime server metrics.
var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stratus_http_requests_total",
		Help: "Total HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "stratus_http_request_duration_seconds",
		Help:    "Duration of HTTP requests",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	WebhooksReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stratus_webhooks_received_total",
		Help: "Total webhooks received by the runtime server",
	})

	WebhookSignatureErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stratus_webhook_signature_errors_total",
		Help: "Total webhook signature validation failures",
	})

	SandboxOperationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stratus_sandbox_operations_total",
		Help: "Total sandbox operations by action (create/delete/health)",
	}, []string{"action"})
)

// Controller health / state.
var (
	ControllerUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "stratus_controller_up",
		Help: "Controller is running (1) or not (0)",
	})

	ControllerJobClaimLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratus_controller_job_claim_duration_seconds",
		Help:    "Duration of the claimAndSchedule transaction",
		Buckets: prometheus.DefBuckets,
	})
)
