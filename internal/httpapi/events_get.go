package httpapi

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
)

// eventResponse mirrors the EmailEvent schema in api/openapi.yaml.
type eventResponse struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Origin     string            `json:"origin"`
	Provider   *string           `json:"provider"`
	OccurredAt time.Time         `json:"occurred_at"`
	ReceivedAt time.Time         `json:"received_at"`
	Details    map[string]string `json:"details"`
}

type eventListResponse struct {
	Data       []eventResponse `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}

const defaultEventPage = 50

// A cursor is the opaque form of the last event id of a page. It carries no
// scope: the email and project always come from the path and the key.
func encodeCursor(id uuid.UUID) string { return base64.RawURLEncoding.EncodeToString(id[:]) }

func decodeCursor(s string) (uuid.UUID, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 16 {
		return uuid.Nil, false
	}
	return uuid.UUID(b), true
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	query := r.URL.Query()
	limit := defaultEventPage
	if v, ok := query["limit"]; ok {
		n, err := strconv.Atoi(v[0])
		if len(v) != 1 || err != nil || n < 1 || n > emails.MaxEventPage {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a single integer between 1 and 100")
			return
		}
		limit = n
	}
	after := uuid.Nil
	if v, ok := query["cursor"]; ok {
		c, valid := decodeCursor(v[0])
		if len(v) != 1 || !valid {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor must be the next_cursor of a previous page")
			return
		}
		after = c
	}

	page, more, err := s.emails.Events(r.Context(), p.ProjectID, id, after, limit)
	switch {
	case errors.Is(err, emails.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	case database.Unavailable(err):
		s.logger.ErrorContext(r.Context(), "list events: database unavailable",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeUnavailable(w)
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "list events failed",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	resp := eventListResponse{Data: make([]eventResponse, 0, len(page))}
	for _, e := range page {
		resp.Data = append(resp.Data, eventResponse{
			ID: e.ID.String(), Type: e.Type, Origin: e.Origin, Provider: e.Provider,
			OccurredAt: e.OccurredAt.UTC(), ReceivedAt: e.ReceivedAt.UTC(), Details: e.Details,
		})
	}
	if more {
		c := encodeCursor(page[len(page)-1].ID)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}
