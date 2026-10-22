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
	Templates    TemplateService
	// Webhooks registers webhook endpoints; nil turns the route into
	// 503 webhook_registration_disabled.
	Webhooks      WebhookRegistrar
	Contacts      ContactService
	Audiences     AudienceService
	Broadcasts    BroadcastService
	Automations   AutomationService
	ContactEvents ContactEventService
	Runs          AutomationRunService
}

// Server holds the HTTP handler and readiness state.
type Server struct {
	logger        *slog.Logger
	checks        []Check
	authenticate  Authenticator
	emails        EmailService
	templates     TemplateService
	webhooks      WebhookRegistrar
	contacts      ContactService
	audiences     AudienceService
	broadcasts    BroadcastService
	automations   AutomationService
	contactEvents ContactEventService
	runs          AutomationRunService
	draining      atomic.Bool
	handler       http.Handler
}

// New builds the API handler.
func New(d Deps) *Server {
	s := &Server{logger: d.Logger, checks: d.Checks, authenticate: d.Authenticate, emails: d.Emails, templates: d.Templates, webhooks: d.Webhooks, contacts: d.Contacts, audiences: d.Audiences, broadcasts: d.Broadcasts, automations: d.Automations, contactEvents: d.ContactEvents, runs: d.Runs}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	// Without these, the catch-all below would turn wrong-method requests
	// into 404s instead of 405s.
	mux.HandleFunc("/healthz", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("/readyz", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/emails", s.requireScope(auth.ScopeEmailsSend, s.handleCreateEmail))
	mux.HandleFunc("GET /v1/emails", s.requireScope(auth.ScopeEmailsRead, s.handleListEmails))
	mux.HandleFunc("/v1/emails", methodNotAllowed(http.MethodGet+", "+http.MethodPost))
	mux.HandleFunc("GET /v1/emails/{id}", s.requireScope(auth.ScopeEmailsRead, s.handleGetEmail))
	mux.HandleFunc("/v1/emails/{id}", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("GET /v1/emails/{id}/events", s.requireScope(auth.ScopeEmailsRead, s.handleListEvents))
	mux.HandleFunc("/v1/emails/{id}/events", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/emails/{id}/cancel", s.requireScope(auth.ScopeEmailsSend, s.handleCancelEmail))
	mux.HandleFunc("/v1/emails/{id}/cancel", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("POST /v1/templates", s.requireScope(auth.ScopeTemplatesWrite, s.handleCreateTemplate))
	mux.HandleFunc("/v1/templates", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("GET /v1/templates/{id}", s.requireScope(auth.ScopeTemplatesRead, s.handleGetTemplate))
	mux.HandleFunc("/v1/templates/{id}", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/templates/{id}/versions", s.requireScope(auth.ScopeTemplatesWrite, s.handlePublishVersion))
	mux.HandleFunc("/v1/templates/{id}/versions", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("GET /v1/templates/{id}/versions/{version}", s.requireScope(auth.ScopeTemplatesRead, s.handleGetVersion))
	mux.HandleFunc("/v1/templates/{id}/versions/{version}", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/webhook-endpoints", s.requireScope(auth.ScopeWebhooksWrite, s.handleCreateWebhookEndpoint))
	mux.HandleFunc("/v1/webhook-endpoints", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("POST /v1/contacts", s.requireScope(auth.ScopeContactsWrite, s.handleCreateContact))
	mux.HandleFunc("GET /v1/contacts", s.requireScope(auth.ScopeContactsRead, s.handleListContacts))
	mux.HandleFunc("/v1/contacts", methodNotAllowed(http.MethodGet+", "+http.MethodPost))
	mux.HandleFunc("GET /v1/contacts/{id}", s.requireScope(auth.ScopeContactsRead, s.handleGetContact))
	mux.HandleFunc("PATCH /v1/contacts/{id}", s.requireScope(auth.ScopeContactsWrite, s.handleUpdateContact))
	mux.HandleFunc("DELETE /v1/contacts/{id}", s.requireScope(auth.ScopeContactsWrite, s.handleDeleteContact))
	mux.HandleFunc("/v1/contacts/{id}", methodNotAllowed(http.MethodGet+", "+http.MethodPatch+", "+http.MethodDelete))
	mux.HandleFunc("POST /v1/unsubscribes", s.requireScope(auth.ScopeContactsWrite, s.handleUnsubscribe))
	mux.HandleFunc("/v1/unsubscribes", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("POST /v1/audiences", s.requireScope(auth.ScopeContactsWrite, s.handleCreateAudience))
	mux.HandleFunc("GET /v1/audiences", s.requireScope(auth.ScopeContactsRead, s.handleListAudiences))
	mux.HandleFunc("/v1/audiences", methodNotAllowed(http.MethodGet+", "+http.MethodPost))
	mux.HandleFunc("GET /v1/audiences/{id}", s.requireScope(auth.ScopeContactsRead, s.handleGetAudience))
	mux.HandleFunc("/v1/audiences/{id}", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/audiences/{id}/contacts", s.requireScope(auth.ScopeContactsWrite, s.handleAddAudienceContacts))
	mux.HandleFunc("GET /v1/audiences/{id}/contacts", s.requireScope(auth.ScopeContactsRead, s.handleListAudienceContacts))
	mux.HandleFunc("/v1/audiences/{id}/contacts", methodNotAllowed(http.MethodGet+", "+http.MethodPost))
	mux.HandleFunc("PUT /v1/audiences/{id}/contacts/{contact_id}", s.requireScope(auth.ScopeContactsWrite, s.handleAddAudienceContact))
	mux.HandleFunc("DELETE /v1/audiences/{id}/contacts/{contact_id}", s.requireScope(auth.ScopeContactsWrite, s.handleRemoveAudienceContact))
	mux.HandleFunc("/v1/audiences/{id}/contacts/{contact_id}", methodNotAllowed(http.MethodPut+", "+http.MethodDelete))
	mux.HandleFunc("POST /v1/broadcasts", s.requireScope(auth.ScopeBroadcastsWrite, s.handleCreateBroadcast))
	mux.HandleFunc("GET /v1/broadcasts", s.requireScope(auth.ScopeBroadcastsRead, s.handleListBroadcasts))
	mux.HandleFunc("/v1/broadcasts", methodNotAllowed(http.MethodGet+", "+http.MethodPost))
	mux.HandleFunc("GET /v1/broadcasts/{id}", s.requireScope(auth.ScopeBroadcastsRead, s.handleGetBroadcast))
	mux.HandleFunc("/v1/broadcasts/{id}", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/broadcasts/{id}/schedule", s.requireScope(auth.ScopeBroadcastsWrite, s.handleScheduleBroadcast))
	mux.HandleFunc("/v1/broadcasts/{id}/schedule", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("POST /v1/broadcasts/{id}/cancel", s.requireScope(auth.ScopeBroadcastsWrite, s.handleCancelBroadcast))
	mux.HandleFunc("/v1/broadcasts/{id}/cancel", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("POST /v1/automations", s.requireScope(auth.ScopeAutomationsWrite, s.handleCreateAutomation))
	mux.HandleFunc("GET /v1/automations", s.requireScope(auth.ScopeAutomationsRead, s.handleListAutomations))
	mux.HandleFunc("/v1/automations", methodNotAllowed(http.MethodGet+", "+http.MethodPost))
	mux.HandleFunc("GET /v1/automations/{id}", s.requireScope(auth.ScopeAutomationsRead, s.handleGetAutomation))
	mux.HandleFunc("PATCH /v1/automations/{id}", s.requireScope(auth.ScopeAutomationsWrite, s.handleUpdateAutomation))
	mux.HandleFunc("/v1/automations/{id}", methodNotAllowed(http.MethodGet+", "+http.MethodPatch))
	mux.HandleFunc("GET /v1/automations/{id}/runs", s.requireScope(auth.ScopeAutomationsRead, s.handleListRuns))
	mux.HandleFunc("/v1/automations/{id}/runs", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("GET /v1/automation-runs/{id}", s.requireScope(auth.ScopeAutomationsRead, s.handleGetRun))
	mux.HandleFunc("/v1/automation-runs/{id}", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("POST /v1/automation-runs/{id}/cancel", s.requireScope(auth.ScopeAutomationsWrite, s.handleCancelRun))
	mux.HandleFunc("/v1/automation-runs/{id}/cancel", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("POST /v1/contact-events", s.requireScope(auth.ScopeEventsWrite, s.handleCreateContactEvent))
	mux.HandleFunc("/v1/contact-events", methodNotAllowed(http.MethodPost))
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

func writeJSON(w http.ResponseWriter, status int, v any) { writeJSONWith(w, status, v, true) }

// writeJSONUnescaped is writeJSON without escaping <, > and & in strings.
func writeJSONUnescaped(w http.ResponseWriter, status int, v any) { writeJSONWith(w, status, v, false) }

func writeJSONWith(w http.ResponseWriter, status int, v any, escapeHTML bool) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(escapeHTML)
	_ = enc.Encode(v) // client gone; nothing useful to do
}

// writeUnavailable answers "dependency down, retry later" with a short
// Retry-After, so clients do not hammer a recovering database.
func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	writeError(w, http.StatusServiceUnavailable, "service_unavailable", "service temporarily unavailable; retry later")
}
