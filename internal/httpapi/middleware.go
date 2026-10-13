package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

const requestIDHeader = "X-Request-Id"

// requestLogger assigns a request ID and logs one line per request. It logs
// the path only, never the query string or headers, which may carry secrets.
func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)

		info := &logInfo{}
		ctx := context.WithValue(r.Context(), logInfoKey{}, info)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		attrs := []slog.Attr{
			slog.String("request_id", id),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
		}
		if info.projectID != "" {
			attrs = append(attrs, slog.String("project_id", info.projectID))
		}
		logger.LogAttrs(r.Context(), slog.LevelInfo, "http request", attrs...)
	})
}

// logInfo collects request attributes that are only known deep inside the
// handler chain (the authenticated project) for the single request log line.
type logInfo struct{ projectID string }

type logInfoKey struct{}

func setLogProject(ctx context.Context, projectID string) {
	if info, ok := ctx.Value(logInfoKey{}).(*logInfo); ok {
		info.projectID = projectID
	}
}

// validRequestID accepts caller-supplied IDs only if short and plain, so they
// cannot be abused for log injection or oversized log lines.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		ok := c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error (Go 1.24+)
	return hex.EncodeToString(b[:])
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
