package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/webhooks"
)

// WebhookRegistrar is what the HTTP layer needs to register webhook
// endpoints. A nil registrar means registration through the API is off.
type WebhookRegistrar interface {
	Create(ctx context.Context, projectID uuid.UUID, rawURL string, types []string) (webhooks.Endpoint, string, error)
}

type createWebhookEndpointRequest struct {
	URL        string          `json:"url"`
	EventTypes json.RawMessage `json:"event_types"`
}

// webhookEndpointCreated mirrors the WebhookEndpointCreated schema in
// api/openapi.yaml.
type webhookEndpointCreated struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	EventTypes []string  `json:"event_types"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	Secret     string    `json:"secret"`
}

var eventTypesMessage = "event_types: each entry must be one of " + strings.Join(webhooks.EventTypes, ", ")

func (s *Server) handleCreateWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		writeError(w, http.StatusServiceUnavailable, "webhook_registration_disabled",
			"webhook registration through the API is not enabled in this deployment; ask an administrator")
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var req createWebhookEndpointRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	types, status, code, msg := parseEventTypes(req.EventTypes)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	p := principalFrom(r.Context())
	e, secret, err := s.webhooks.Create(r.Context(), p.ProjectID, req.URL, types)
	if err != nil {
		s.writeWebhookError(w, r, err)
		return
	}
	s.logger.InfoContext(r.Context(), "webhook endpoint created",
		slog.String("project_id", p.ProjectID.String()), slog.String("endpoint_id", e.ID.String()))
	writeJSON(w, http.StatusCreated, webhookEndpointCreated{
		ID: e.ID.String(), URL: e.URL, EventTypes: e.EventTypes, Status: e.Status,
		CreatedAt: e.CreatedAt.UTC(), Secret: secret,
	})
}

// parseEventTypes reads the optional event_types member: absent means all
// types (nil); null, an empty list, duplicates and unknown types are refused
// without echoing the submitted values.
func parseEventTypes(raw json.RawMessage) (types []string, status int, code, msg string) {
	if len(raw) == 0 {
		return nil, 0, "", ""
	}
	if bytes.Equal(raw, []byte("null")) {
		return nil, http.StatusUnprocessableEntity, "validation_failed", "event_types: must be an array; omit it to receive all types"
	}
	if err := json.Unmarshal(raw, &types); err != nil {
		return nil, http.StatusBadRequest, "invalid_json", "request body must be a JSON object matching the schema: event_types must be an array of strings"
	}
	if len(types) == 0 {
		return nil, http.StatusUnprocessableEntity, "validation_failed", "event_types: must contain at least one type; omit it to receive all types"
	}
	for i, t := range types {
		if !slices.Contains(webhooks.EventTypes, t) {
			return nil, http.StatusUnprocessableEntity, "validation_failed", eventTypesMessage
		}
		if slices.Contains(types[:i], t) {
			return nil, http.StatusUnprocessableEntity, "validation_failed", "event_types: must not contain duplicates"
		}
	}
	return types, 0, "", ""
}

func (s *Server) writeWebhookError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, webhooks.ErrForbiddenTarget):
		// The policy reason names the rule, never a resolved address.
		writeError(w, http.StatusUnprocessableEntity, "invalid_url", "url: "+strings.TrimPrefix(err.Error(), webhooks.ErrForbiddenTarget.Error()+": "))
	case errors.Is(err, webhooks.ErrUnknownEventType):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", eventTypesMessage)
	case errors.Is(err, webhooks.ErrEndpointLimit):
		writeError(w, http.StatusUnprocessableEntity, "endpoint_limit_reached",
			"the project has reached its webhook endpoint limit (disabled endpoints count); ask an administrator")
	case errors.Is(err, webhooks.ErrBusy):
		writeUnavailable(w)
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		s.logger.DebugContext(r.Context(), "create webhook endpoint: client cancelled")
		writeUnavailable(w)
	case database.Unavailable(err):
		s.logger.ErrorContext(r.Context(), "create webhook endpoint: database unavailable",
			slog.String("project_id", principalFrom(r.Context()).ProjectID.String()))
		writeUnavailable(w)
	default:
		// Only the SQLSTATE is logged: wrapped causes may carry the URL.
		var pgErr *pgconn.PgError
		sqlstate := ""
		if errors.As(err, &pgErr) {
			sqlstate = pgErr.Code
		}
		s.logger.ErrorContext(r.Context(), "create webhook endpoint failed",
			slog.String("project_id", principalFrom(r.Context()).ProjectID.String()), slog.String("sqlstate", sqlstate))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}
