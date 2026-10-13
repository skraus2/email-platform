// Package httpapi wires the HTTP surface of the API process: liveness
// (/healthz), readiness (/readyz) and the project-authenticated /v1 API
// described in api/openapi.yaml.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/skraus2/email-platform/internal/auth"
)

// readinessTimeout bounds all readiness checks together so a hung dependency
// cannot hang the probe.
const readinessTimeout = 2 * time.Second

// Check is a named readiness dependency. Its error is logged but never
// returned to the client: probe responses must not leak internals.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Deps are the collaborators of the HTTP layer.
type Deps struct {
	Logger       *slog.Logger
	Checks       []Check
	Authenticate Authenticator
	Emails       EmailService
}

// Server holds the HTTP handler and readiness state.
type Server struct {
	logger       *slog.Logger
	checks       []Check
	authenticate Authenticator
	emails       EmailService
	draining     atomic.Bool
	handler      http.Handler
}

// New builds the API handler.
func New(d Deps) *Server {
	s := &Server{logger: d.Logger, checks: d.Checks, authenticate: d.Authenticate, emails: d.Emails}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	// Without these, the catch-all below would turn wrong-method requests
	// into 404s instead of 405s.
	mux.HandleFunc("/healthz", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("/readyz", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/emails", s.requireScope(auth.ScopeEmailsSend, s.handleCreateEmail))
	mux.HandleFunc("/v1/emails", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("GET /v1/emails/{id}", s.requireScope(auth.ScopeEmailsRead, s.handleGetEmail))
	mux.HandleFunc("/v1/emails/{id}", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("/", handleNotFound)

	s.handler = requestLogger(s.logger, mux)
	return s
}

// Handler returns the root handler including middleware.
func (s *Server) Handler() http.Handler { return s.handler }

// SetDraining marks the process as shutting down so /readyz reports 503 on
// connections that are still open. cmd/api calls http.Server.Shutdown right
// after, which closes the listener: new probes then get "connection refused",
// which is the not-ready signal. A drain delay before Shutdown is only needed
// once a load balancer sits in front (none yet).
func (s *Server) SetDraining() { s.draining.Store(true) }

// handleHealthz is liveness: the process is up and serving HTTP. It must not
// depend on the database, or a DB outage would restart every API instance.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz is readiness: the process can do useful work right now.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, readyResponse{Status: "not_ready", Checks: map[string]string{"process": "draining"}})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	resp := readyResponse{Status: "ready", Checks: make(map[string]string, len(s.checks))}
	code := http.StatusOK
	for _, c := range s.checks {
		if err := c.Fn(ctx); err != nil {
			s.logger.WarnContext(ctx, "readiness check failed", slog.String("check", c.Name), slog.Any("error", err))
			resp.Checks[c.Name] = "unavailable"
			resp.Status = "not_ready"
			code = http.StatusServiceUnavailable
			continue
		}
		resp.Checks[c.Name] = "ok"
	}
	writeJSON(w, code, resp)
}

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "resource not found")
}

func methodNotAllowed(allowed string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allowed+", "+http.MethodHead)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// writeError emits the API error envelope {"error":{"code":"…","message":"…"}}.
func writeError(w http.ResponseWriter, status int, code, message string) {
	type body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	writeJSON(w, status, map[string]body{"error": {Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // client gone; nothing useful to do
}
