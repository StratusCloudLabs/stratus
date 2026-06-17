package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Logger is a chi-compatible request logger that uses slog.
func Logger(logger *slog.Logger) func(next http.Handler) http.Handler {
	return middleware.RequestLogger(&logFormatter{logger: logger})
}

type logFormatter struct {
	logger *slog.Logger
}

func (f *logFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	return &logEntry{
		logger: f.logger,
		r:      r,
		start:  time.Now(),
	}
}

type logEntry struct {
	logger *slog.Logger
	r      *http.Request
	start  time.Time
}

func (e *logEntry) Write(status, bytes int, header http.Header, elapsed time.Duration, extra interface{}) {
	e.logger.Info("http request",
		"method", e.r.Method,
		"path", e.r.URL.Path,
		"status", status,
		"bytes", bytes,
		"duration_ms", elapsed.Milliseconds(),
		"remote_addr", e.r.RemoteAddr,
	)
}

func (e *logEntry) Panic(v interface{}, stack []byte) {
	e.logger.Error("http panic",
		"panic", v,
		"stack", string(stack),
	)
}

// SecurityHeaders sets standard security-related HTTP headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// errorResponse creates a standard JSON error body.
func errorResponse(msg string) map[string]string {
	return map[string]string{"error": msg}
}
