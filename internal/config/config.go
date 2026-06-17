// Package config provides runtime configuration for stratus-runtime.
//
// Configuration is sourced from environment variables with sensible defaults
// for local development and production deployment.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the top-level configuration object for stratus-runtime.
// All fields have env-friendly defaults where possible, and zero values
// signal that a subsystem should be disabled or use its own defaults.
type Config struct {
	// Server configuration
	Port         int
	MetricsPort  int
	ShutdownTimeout time.Duration

	// GCP / Firestore
	ProjectID        string
	FirestoreDB      string
	CredentialsFile  string

	// Kubernetes
	KubeConfigPath string
	RunnerNamespace string
	GHCRBase       string
	GHCRSecret     string

	// Webhook
	WebhookSecret      string
	WebhookUpstream    string

	// JIT controller
	InstallationID          string
	MetricsPushInterval     time.Duration
	ReaperInterval          time.Duration
	MinScheduledAge         time.Duration
	RetryBaseInterval       time.Duration
	RetryMaxInterval        time.Duration
	StartupRecoveryThreshold time.Duration

	// Sandbox
	SandboxImage string

	// Auth
	AuthJWTSecret  string
	JWTIssuer      string
	JWTAudience    string

	// Version / release
	Version string
}

// Load builds a Config from environment variables.
func Load() *Config {
	return &Config{
		Port:                    envInt("PORT", 8080),
		MetricsPort:             envInt("METRICS_PORT", 9091),
		ShutdownTimeout:         envDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
		ProjectID:               envStr("GOOGLE_CLOUD_PROJECT", ""),
		FirestoreDB:             envStr("FIRESTORE_DB", "(default)"),
		CredentialsFile:         envStr("GOOGLE_APPLICATION_CREDENTIALS", ""),
		KubeConfigPath:          envStr("KUBECONFIG", ""),
		RunnerNamespace:         envStr("RUNNER_NAMESPACE", "arc-runners"),
		GHCRBase:                envStr("GHCR_BASE", "ghcr.io/stratuscloudlabs"),
		GHCRSecret:              envStr("GHCR_SECRET", "ghcr-secret"),
		WebhookSecret:           envStr("GITHUB_WEBHOOK_SECRET", ""),
		WebhookUpstream:         envStr("WEBHOOK_UPSTREAM_URL", "http://localhost:8080/webhooks/github"),
		InstallationID:          envStr("STRATUS_INSTALLATION_ID", ""),
		MetricsPushInterval:     envDuration("METRICS_PUSH_INTERVAL", 30*time.Second),
		ReaperInterval:          envDuration("REAPER_INTERVAL", 30*time.Second),
		MinScheduledAge:         envDuration("MIN_SCHEDULED_AGE", 2*time.Minute),
		RetryBaseInterval:       envDuration("RETRY_BASE_INTERVAL", 30*time.Second),
		RetryMaxInterval:        envDuration("RETRY_MAX_INTERVAL", 5*time.Minute),
		StartupRecoveryThreshold: envDuration("STARTUP_RECOVERY_THRESHOLD", 2*time.Minute),
		SandboxImage:            envStr("SANDBOX_IMAGE", "ghcr.io/stratuscloudlabs/sandbox-kata:latest"),
		AuthJWTSecret:           envStr("STRATUS_AUTH_JWT_SECRET", ""),
		JWTIssuer:               envStr("STRATUS_AUTH_JWT_ISSUER", "stratus"),
		JWTAudience:             envStr("STRATUS_AUTH_JWT_AUDIENCE", "stratus-runtime"),
		Version:                 envStr("STRATUS_VERSION", "dev"),
	}
}

// Validate checks that required configuration is present and reasonable.
func (c *Config) Validate() error {
	var errs []string

	if c.ProjectID == "" {
		errs = append(errs, "GOOGLE_CLOUD_PROJECT must be set")
	}
	if c.WebhookSecret == "" {
		errs = append(errs, "GITHUB_WEBHOOK_SECRET must be set")
	}
	if c.Port <= 0 || c.Port > 65535 {
		errs = append(errs, "PORT must be a valid port number")
	}
	if c.MetricsPort <= 0 || c.MetricsPort > 65535 {
		errs = append(errs, "METRICS_PORT must be a valid port number")
	}
	if c.Port == c.MetricsPort {
		errs = append(errs, "PORT and METRICS_PORT must differ")
	}
	if c.RunnerNamespace == "" {
		errs = append(errs, "RUNNER_NAMESPACE must not be empty")
	}
	if c.GHCRBase != "" {
		if _, err := url.Parse(c.GHCRBase); err != nil {
			errs = append(errs, fmt.Sprintf("GHCR_BASE is not a valid URL: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// WebhookUpstreamURL returns the configured webhook upstream URL that
// validated GitHub webhook payloads are proxied to. Override via the
// WEBHOOK_UPSTREAM_URL environment variable to point at your own ingest
// service.
func (c *Config) WebhookUpstreamURL() string {
	return c.WebhookUpstream
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
