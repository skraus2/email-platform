package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
)

// emailResponse mirrors the Email schema in api/openapi.yaml.
type emailResponse struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	Subject    string     `json:"subject"`
	Attempts   int        `json:"attempts"`
	ErrorCode  *string    `json:"error_code"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (s *Server) handleGetEmail(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	// A malformed id gets the same 404 as a missing or foreign one.
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	e, err := s.emails.Get(r.Context(), p.ProjectID, id)
	switch {
	case errors.Is(err, emails.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	case database.Unavailable(err):
		s.logger.WarnContext(r.Context(), "get email: database unavailable",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "service temporarily unavailable; retry later")
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "get email failed",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	resp := emailResponse{
		ID: e.ID.String(), Status: e.Status, From: e.From, To: e.To, Subject: e.Subject,
		Attempts: e.Attempts, ErrorCode: e.ErrorCode,
		CreatedAt: e.CreatedAt.UTC(), UpdatedAt: e.UpdatedAt.UTC(),
	}
	if e.FinishedAt != nil {
		f := e.FinishedAt.UTC()
		resp.FinishedAt = &f
	}
	writeJSON(w, http.StatusOK, resp)
}
