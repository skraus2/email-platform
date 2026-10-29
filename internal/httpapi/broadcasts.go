package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/templates"
)

// BroadcastService is what the HTTP layer needs from the broadcasts module.
type BroadcastService interface {
	Create(ctx context.Context, projectID uuid.UUID, in broadcasts.CreateInput) (broadcasts.Broadcast, error)
	Get(ctx context.Context, projectID, id uuid.UUID) (broadcasts.Broadcast, error)
	List(ctx context.Context, projectID uuid.UUID, status string, after uuid.UUID, limit int) ([]broadcasts.Broadcast, bool, error)
	Schedule(ctx context.Context, projectID, id uuid.UUID, at *time.Time) (broadcasts.Broadcast, error)
	Cancel(ctx context.Context, projectID, id uuid.UUID) (broadcasts.Broadcast, error)
}

// broadcastResponse mirrors Broadcast / BroadcastWithCounts in
// api/openapi.yaml; Counts is only set for a single broadcast.
type broadcastResponse struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Status          string                  `json:"status"`
	AudienceID      string                  `json:"audience_id"`
	From            string                  `json:"from"`
	TemplateID      string                  `json:"template_id"`
	TemplateVersion int                     `json:"template_version"`
	UnsubscribeURL  string                  `json:"unsubscribe_url"`
	ScheduledAt     *time.Time              `json:"scheduled_at"`
	StartedAt       *time.Time              `json:"started_at"`
	FinishedAt      *time.Time              `json:"finished_at"`
	CreatedAt       time.Time               `json:"created_at"`
	Counts          *broadcastCountResponse `json:"counts,omitempty"`
}

type broadcastCountResponse struct {
	Recipients          int            `json:"recipients"`
	SkippedUnsubscribed int            `json:"skipped_unsubscribed"`
	SkippedSuppressed   int            `json:"skipped_suppressed"`
	SkippedInvalid      int            `json:"skipped_invalid"`
	ByStatus            map[string]int `json:"by_status"`
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toBroadcastResponse(b broadcasts.Broadcast) broadcastResponse {
	r := broadcastResponse{
		ID: b.ID.String(), Name: b.Name, Status: b.Status, AudienceID: b.AudienceID.String(), From: b.From,
		TemplateID: b.TemplateID.String(), TemplateVersion: b.TemplateVersion, UnsubscribeURL: b.UnsubscribeURL,
		ScheduledAt: utcPtr(b.ScheduledAt), StartedAt: utcPtr(b.StartedAt), FinishedAt: utcPtr(b.FinishedAt),
		CreatedAt: b.CreatedAt.UTC(),
	}
	if c := b.Counts; c != nil {
		r.Counts = &broadcastCountResponse{
			Recipients: c.Recipients, SkippedUnsubscribed: c.SkippedUnsubscribed, SkippedSuppressed: c.SkippedSuppressed,
			SkippedInvalid: c.SkippedInvalid, ByStatus: c.ByStatus,
		}
	}
	return r
}

type broadcastListResponse struct {
	Data       []broadcastResponse `json:"data"`
	NextCursor *string             `json:"next_cursor"`
}

type createBroadcastRequest struct {
	Name            string          `json:"name"`
	AudienceID      string          `json:"audience_id"`
	From            string          `json:"from"`
	TemplateID      string          `json:"template_id"`
	TemplateVersion json.RawMessage `json:"template_version"`
	Variables       json.RawMessage `json:"variables"`
	UnsubscribeURL  string          `json:"unsubscribe_url"`
}

type scheduleBroadcastRequest struct {
	ScheduledAt *string `json:"scheduled_at"`
}

func (s *Server) handleCreateBroadcast(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req createBroadcastRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	in := broadcasts.CreateInput{Name: req.Name, From: req.From, Variables: req.Variables, UnsubscribeURL: req.UnsubscribeURL}
	var err error
	if in.AudienceID, err = uuid.Parse(req.AudienceID); err != nil || len(req.AudienceID) != 36 {
		s.writeBroadcastError(w, r, "create broadcast", &broadcasts.Error{Field: "audience_id", Reason: "must be a UUID"})
		return
	}
	if in.TemplateID, err = uuid.Parse(req.TemplateID); err != nil || len(req.TemplateID) != 36 {
		s.writeBroadcastError(w, r, "create broadcast", &broadcasts.Error{Field: "template_id", Reason: "must be a UUID"})
		return
	}
	if in.TemplateVersion, err = parseTemplateVersion(req.TemplateVersion); err != nil {
		s.writeBroadcastError(w, r, "create broadcast", &broadcasts.Error{Field: "template_version", Reason: `must be a positive integer or "published"`})
		return
	}
	b, err := s.broadcasts.Create(r.Context(), principalFrom(r.Context()).ProjectID, in)
	if err != nil {
		s.writeBroadcastError(w, r, "create broadcast", err)
		return
	}
	writeJSON(w, http.StatusCreated, toBroadcastResponse(b))
}

// parseTemplateVersion reads a positive integer or "published" (0).
func parseTemplateVersion(raw json.RawMessage) (int, error) {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		if name == "published" {
			return 0, nil
		}
		return 0, errors.New("unknown version name")
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n < 1 || n > 1<<30 {
		return 0, errors.New("not a version")
	}
	return n, nil
}

func (s *Server) handleGetBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := s.broadcasts.Get(r.Context(), principalFrom(r.Context()).ProjectID, id)
	if err != nil {
		s.writeBroadcastError(w, r, "get broadcast", err)
		return
	}
	writeJSON(w, http.StatusOK, toBroadcastResponse(b))
}

var broadcastListParams = map[string]bool{"status": true, "limit": true, "cursor": true}

func (s *Server) handleListBroadcasts(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	limit, after, status, code, msg := parsePage(query, broadcastListParams, defaultPage, broadcasts.MaxPage)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	var st string
	if v, ok := query["status"]; ok {
		if len(v) != 1 || !broadcasts.IsStatus(v[0]) {
			writeError(w, http.StatusBadRequest, "invalid_filter", "status must be a broadcast status")
			return
		}
		st = v[0]
	}
	page, more, err := s.broadcasts.List(r.Context(), principalFrom(r.Context()).ProjectID, st, after, limit)
	if err != nil {
		s.writeBroadcastError(w, r, "list broadcasts", err)
		return
	}
	resp := broadcastListResponse{Data: make([]broadcastResponse, 0, len(page))}
	for _, b := range page {
		resp.Data = append(resp.Data, toBroadcastResponse(b))
	}
	if more {
		c := encodeCursor(page[len(page)-1].ID)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleScheduleBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// The body is optional: without one the broadcast starts now.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body must be at most 4 KiB")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not read request body")
		return
	}
	var at *time.Time
	if len(bytes.TrimSpace(body)) > 0 {
		if !requireJSON(w, r) {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req scheduleBroadcastRequest
		if status, code, msg := decodeStrict(w, r, &req); status != 0 {
			writeError(w, status, code, msg)
			return
		}
		if req.ScheduledAt != nil {
			t, err := time.Parse(time.RFC3339Nano, *req.ScheduledAt)
			if err != nil {
				s.writeBroadcastError(w, r, "schedule broadcast", &broadcasts.Error{Field: "scheduled_at", Reason: "must be an RFC 3339 timestamp"})
				return
			}
			at = &t
		}
	}
	b, err := s.broadcasts.Schedule(r.Context(), principalFrom(r.Context()).ProjectID, id, at)
	if err != nil {
		s.writeBroadcastError(w, r, "schedule broadcast", err)
		return
	}
	writeJSON(w, http.StatusOK, toBroadcastResponse(b))
}

func (s *Server) handleCancelBroadcast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := s.broadcasts.Cancel(r.Context(), principalFrom(r.Context()).ProjectID, id)
	if err != nil {
		s.writeBroadcastError(w, r, "cancel broadcast", err)
		return
	}
	writeJSON(w, http.StatusOK, toBroadcastResponse(b))
}

// writeBroadcastError maps broadcast errors to the contract. Client errors
// carry no submitted values.
func (s *Server) writeBroadcastError(w http.ResponseWriter, r *http.Request, op string, err error) {
	var be *broadcasts.Error
	var te *templates.Error
	switch {
	case errors.As(err, &be):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", be.Error())
	case errors.As(err, &te):
		writeError(w, http.StatusUnprocessableEntity, te.Code, te.Error())
	case errors.Is(err, broadcasts.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, broadcasts.ErrAudienceNotFound):
		writeError(w, http.StatusUnprocessableEntity, "audience_not_found", "no such audience in this project")
	case errors.Is(err, broadcasts.ErrTemplateNotFound):
		writeError(w, http.StatusUnprocessableEntity, "template_not_found", "no such template or version in this project")
	case errors.Is(err, broadcasts.ErrMissingUnsubscribe):
		writeError(w, http.StatusUnprocessableEntity, "template_missing_unsubscribe",
			"the template version must declare unsubscribe_url as a string, print {{.unsubscribe_url}} outside conditions in its text part and link it as an <a href> outside conditions in its HTML part")
	case errors.Is(err, broadcasts.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "the project created as many broadcasts in the last hour as it may; retry later")
	case errors.Is(err, broadcasts.ErrLimit):
		writeError(w, http.StatusUnprocessableEntity, "broadcast_limit_reached", "the project has reached its limit of open broadcasts")
	case errors.Is(err, emails.ErrSenderNotVerified):
		writeError(w, http.StatusUnprocessableEntity, "sender_not_verified", "from is not a verified sender address or domain of this project")
	case errors.Is(err, broadcasts.ErrNotDraft):
		writeError(w, http.StatusConflict, "broadcast_not_draft", "only a draft can be scheduled; GET the broadcast for its current state")
	case errors.Is(err, broadcasts.ErrNotCancelable):
		writeError(w, http.StatusConflict, "broadcast_not_cancelable", "the broadcast is already canceled")
	case errors.Is(err, broadcasts.ErrBusy):
		writeUnavailable(w)
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		s.logger.DebugContext(r.Context(), op+": client cancelled")
		writeUnavailable(w)
	case database.Unavailable(err):
		s.logger.ErrorContext(r.Context(), op+": database unavailable",
			slog.String("project_id", principalFrom(r.Context()).ProjectID.String()), slog.Any("error", err))
		writeUnavailable(w)
	default:
		s.logger.ErrorContext(r.Context(), op+" failed",
			slog.String("project_id", principalFrom(r.Context()).ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}
