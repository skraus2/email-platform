package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
)

type emailListResponse struct {
	Data       []emailResponse `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}

const (
	defaultEmailPage = 50
	maxStatusFilters = 9 // every email status once
)

// listQueryParams are the query parameters GET /v1/emails understands.
var listQueryParams = map[string]bool{
	"status": true, "recipient": true, "created_after": true, "created_before": true,
	"limit": true, "cursor": true, "template_id": true, "broadcast_id": true,
}

func (s *Server) handleListEmails(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	f, limit, status, code, msg := parseListQuery(query)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}

	page, more, err := s.emails.List(r.Context(), p.ProjectID, f, limit)
	switch {
	case errors.Is(err, emails.ErrListBusy):
		writeUnavailable(w)
		return
	case database.Unavailable(err):
		s.logger.ErrorContext(r.Context(), "list emails: database unavailable",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeUnavailable(w)
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "list emails failed",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	resp := emailListResponse{Data: make([]emailResponse, 0, len(page))}
	for _, e := range page {
		resp.Data = append(resp.Data, toEmailResponse(e))
	}
	if more {
		c := encodeCursor(page[len(page)-1].ID)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseListQuery validates the list parameters. Error messages name the
// parameter, never the submitted value.
func parseListQuery(query url.Values) (f emails.ListFilter, limit, status int, code, msg string) {
	bad := func(code, msg string) (emails.ListFilter, int, int, string, string) {
		return emails.ListFilter{}, 0, http.StatusBadRequest, code, msg
	}
	limit, f.After, status, code, msg = parsePage(query, listQueryParams, defaultEmailPage, emails.MaxEmailPage)
	if status != 0 {
		return emails.ListFilter{}, 0, status, code, msg
	}
	if v, ok := query["status"]; ok {
		if len(v) > maxStatusFilters {
			return bad("invalid_filter", "status may be given at most 9 times")
		}
		for _, st := range v {
			if !emails.IsStatus(st) {
				return bad("invalid_filter", "status must be an email status")
			}
		}
		f.Statuses = v
	}
	if v, ok := query["recipient"]; ok {
		if len(v) != 1 || emails.ValidateAddress(v[0]) != nil {
			return bad("invalid_filter", "recipient must be a single plain email address")
		}
		f.Recipient = v[0]
	}
	if v, ok := query["template_id"]; ok {
		id, err := uuid.Parse(v[0])
		if len(v) != 1 || err != nil || len(v[0]) != 36 {
			return bad("invalid_filter", "template_id must be a single UUID")
		}
		f.TemplateID = &id
	}
	if v, ok := query["broadcast_id"]; ok {
		id, err := uuid.Parse(v[0])
		if len(v) != 1 || err != nil || len(v[0]) != 36 {
			return bad("invalid_filter", "broadcast_id must be a single UUID")
		}
		f.BroadcastID = &id
	}
	for _, b := range []struct {
		name string
		dst  **time.Time
	}{{"created_after", &f.CreatedAfter}, {"created_before", &f.CreatedBefore}} {
		v, ok := query[b.name]
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, v[0])
		if len(v) != 1 || err != nil {
			return bad("invalid_filter", b.name+" must be a single RFC 3339 timestamp")
		}
		*b.dst = &t
	}
	if f.CreatedAfter != nil && f.CreatedBefore != nil && !f.CreatedAfter.Before(*f.CreatedBefore) {
		return bad("invalid_filter", "created_after must be earlier than created_before")
	}
	return f, limit, 0, "", ""
}
