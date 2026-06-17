package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/StratusCloudLabs/stratus-runtime/internal/metrics"
)

// GitHubEvent represents a parsed GitHub webhook event.
type GitHubEvent struct {
	Event     string          `json:"event"`
	Delivery  string          `json:"delivery"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

// WebhookHandler processes incoming GitHub webhooks.
type WebhookHandler struct {
	secret      string
	upstreamURL string
	httpClient  *http.Client
	logger      *slog.Logger
}

// NewWebhookHandler creates a new GitHub webhook handler.
func NewWebhookHandler(secret, upstreamURL string, logger *slog.Logger) *WebhookHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &WebhookHandler{
		secret:      secret,
		upstreamURL: upstreamURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger.With("component", "webhook"),
	}
}

// ServeHTTP handles an incoming GitHub webhook request.
// It validates the HMAC-SHA256 signature, then proxies the payload to the
// upstream webhook-ingest service.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metrics.WebhooksReceivedTotal.Inc()

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse("Method not allowed"))
		return
	}

	// Read the raw body for signature verification.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse("Failed to read request body"))
		return
	}
	defer r.Body.Close()

	// Verify signature.
	signature := getWebhookSignature(r)
	if signature == "" {
		metrics.WebhookSignatureErrorsTotal.Inc()
		writeJSON(w, http.StatusUnauthorized, errorResponse("Missing GitHub webhook signature"))
		return
	}

	if h.secret != "" {
		if !verifyHMACSignature(signature, body, h.secret) {
			metrics.WebhookSignatureErrorsTotal.Inc()
			writeJSON(w, http.StatusUnauthorized, errorResponse("Invalid webhook signature"))
			return
		}
	}

	// Extract event type for logging.
	eventType := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")

	h.logger.Info("received webhook",
		"event", eventType,
		"delivery", deliveryID,
		"size", len(body),
	)

	// Proxy to upstream.
	statusCode, respBody, err := h.proxyToUpstream(r, body)
	if err != nil {
		h.logger.Error("upstream proxy failed",
			"event", eventType,
			"error", err,
		)
		writeJSON(w, http.StatusBadGateway, errorResponse("Upstream unavailable"))
		return
	}

	// Copy upstream response back.
	for key, values := range w.Header() {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(statusCode)
	if respBody != nil {
		w.Write(respBody)
	}
}

// proxyToUpstream forwards the webhook payload to the internal webhook-ingest service.
func (h *WebhookHandler) proxyToUpstream(originalReq *http.Request, body []byte) (int, []byte, error) {
	upstreamReq, err := http.NewRequestWithContext(
		originalReq.Context(),
		http.MethodPost,
		h.upstreamURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return 0, nil, fmt.Errorf("create upstream request: %w", err)
	}

	// Forward relevant headers.
	upstreamReq.Header.Set("Content-Type", originalReq.Header.Get("Content-Type"))
	upstreamReq.Header.Set("X-GitHub-Event", originalReq.Header.Get("X-GitHub-Event"))
	upstreamReq.Header.Set("X-GitHub-Delivery", originalReq.Header.Get("X-GitHub-Delivery"))
	upstreamReq.Header.Set("X-Hub-Signature-256", originalReq.Header.Get("X-Hub-Signature-256"))
	upstreamReq.Header.Set("User-Agent", originalReq.Header.Get("User-Agent"))

	resp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		return 0, nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read upstream response: %w", err)
	}

	return resp.StatusCode, respBody, nil
}

// getWebhookSignature extracts the webhook signature from the request headers.
func getWebhookSignature(r *http.Request) string {
	if sig := r.Header.Get("X-Hub-Signature-256"); sig != "" {
		return sig
	}
	return r.Header.Get("X-Hub-Signature")
}

// verifyHMACSignature verifies an HMAC-SHA256 (or SHA1) webhook signature.
// The signature format is: `sha256=<hex>` or `sha1=<hex>`.
func verifyHMACSignature(signature string, body []byte, secret string) bool {
	parts := strings.SplitN(signature, "=", 2)
	if len(parts) != 2 {
		return false
	}

	algorithm := parts[0]
	expectedSig := parts[1]

	var mac hash.Hash
	switch algorithm {
	case "sha256":
		mac = hmac.New(sha256.New, []byte(secret))
	case "sha1":
		mac = hmac.New(sha1.New, []byte(secret))
	default:
		return false
	}

	mac.Write(body)
	computedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expectedSig), []byte(computedSig))
}
