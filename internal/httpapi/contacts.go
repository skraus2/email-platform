package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
)

// ContactService is what the HTTP layer needs from the contacts module.
type ContactService interface {
	Create(ctx context.Context, projectID uuid.UUID, in contacts.CreateInput) (contacts.Contact, error)
	Get(ctx context.Context, projectID, id uuid.UUID) (contacts.Contact, error)
	Update(ctx context.Context, projectID, id uuid.UUID, in contacts.UpdateInput) (contacts.Contact, error)
	Delete(ctx context.Context, projectID, id uuid.UUID) error
	List(ctx context.Context, projectID uuid.UUID, f contacts.ListFilter, limit int) ([]contacts.Contact, bool, error)
	Unsubscribe(ctx context.Context, projectID uuid.UUID, token string) (uuid.UUID, error)
}

// contactResponse mirrors the Contact schema in api/openapi.yaml.
type contactResponse struct {
	ID              string          `json:"id"`
	Email           string          `json:"email"`
	Status          string          `json:"status"`
	ConsentSource   *string         `json:"consent_source"`
	StatusChangedAt time.Time       `json:"status_changed_at"`
	Attributes      json.RawMessage `json:"attributes"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func toContactResponse(c contacts.Contact) contactResponse {
	return contactResponse{
		ID: c.ID.String(), Email: c.Email, Status: c.Status, ConsentSource: c.ConsentSource,
		StatusChangedAt: c.StatusChangedAt.UTC(), Attributes: c.Attributes,
		CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}
}

type contactListResponse struct {
	Data       []contactResponse `json:"data"`
	NextCursor *string           `json:"next_cursor"`
}

type createContactRequest struct {
	Email         string          `json:"email"`
	Status        string          `json:"status"`
	ConsentSource *string         `json:"consent_source"`
	Attributes    json.RawMessage `json:"attributes"`
}

// updateContactRequest keeps raw values so that an explicit null can be
// told apart from an absent field.
type updateContactRequest struct {
	Status        json.RawMessage `json:"status"`
	ConsentSource json.RawMessage `json:"consent_source"`
	Attributes    json.RawMessage `json:"attributes"`
}

// optionalString decodes a present string field; absent gives nil.
func optionalString(field string, raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	var s *string
	if err := json.Unmarshal(raw, &s); err != nil || s == nil {
		return nil, &contacts.Error{Field: field, Reason: "must be a string"}
	}
	return s, nil
}

func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req createContactRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	p := principalFrom(r.Context())
	c, err := s.contacts.Create(r.Context(), p.ProjectID, contacts.CreateInput{
		Email: req.Email, Status: req.Status, ConsentSource: req.ConsentSource, Attributes: req.Attributes,
	})
	if err != nil {
		s.writeContactError(w, r, "create contact", err)
		return
	}
	writeJSON(w, http.StatusCreated, toContactResponse(c))
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.contacts.Get(r.Context(), principalFrom(r.Context()).ProjectID, id)
	if err != nil {
		s.writeContactError(w, r, "get contact", err)
		return
	}
	writeJSON(w, http.StatusOK, toContactResponse(c))
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || !requireJSON(w, r) {
		return
	}
	var req updateContactRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	in := contacts.UpdateInput{Attributes: req.Attributes}
	var err error
	if in.Status, err = optionalString("status", req.Status); err == nil {
		in.ConsentSource, err = optionalString("consent_source", req.ConsentSource)
	}
	if err != nil {
		s.writeContactError(w, r, "update contact", err)
		return
	}
	c, err := s.contacts.Update(r.Context(), principalFrom(r.Context()).ProjectID, id, in)
	if err != nil {
		s.writeContactError(w, r, "update contact", err)
		return
	}
	writeJSON(w, http.StatusOK, toContactResponse(c))
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.contacts.Delete(r.Context(), principalFrom(r.Context()).ProjectID, id); err != nil {
		s.writeContactError(w, r, "delete contact", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// defaultPage is the page size of list endpoints without a limit.
const defaultPage = 50

type unsubscribeRequest struct {
	Token string `json:"token"`
}

// handleUnsubscribe takes an unsubscribe reported by the app's own page.
// The token is never logged or echoed; every failure to resolve it is the
// same 404.
func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req unsubscribeRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	id, err := s.contacts.Unsubscribe(r.Context(), principalFrom(r.Context()).ProjectID, req.Token)
	if err != nil {
		s.writeContactError(w, r, "unsubscribe", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"contact_id": id.String(), "status": contacts.StatusUnsubscribed})
}

// contactListParams are the query parameters GET /v1/contacts understands.
var contactListParams = map[string]bool{"email": true, "status": true, "limit": true, "cursor": true}

func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	var f contacts.ListFilter
	limit, after, status, code, msg := parsePage(query, contactListParams, defaultPage, contacts.MaxPage)
	if status == 0 {
		f.After = after
		if v, ok := query["email"]; ok {
			if len(v) != 1 || emails.ValidateAddress(v[0]) != nil {
				status, code, msg = http.StatusBadRequest, "invalid_filter", "email must be a single plain email address"
			} else {
				f.Email = v[0]
			}
		}
	}
	if status == 0 {
		if v, ok := query["status"]; ok {
			if len(v) != 1 || !contacts.IsStatus(v[0]) {
				status, code, msg = http.StatusBadRequest, "invalid_filter", "status must be subscribed or unsubscribed"
			} else {
				f.Status = v[0]
			}
		}
	}
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	page, more, err := s.contacts.List(r.Context(), principalFrom(r.Context()).ProjectID, f, limit)
	if err != nil {
		s.writeContactError(w, r, "list contacts", err)
		return
	}
	resp := contactListResponse{Data: make([]contactResponse, 0, len(page))}
	for _, c := range page {
		resp.Data = append(resp.Data, toContactResponse(c))
	}
	if more {
		c := encodeCursor(page[len(page)-1].ID)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

// parsePage checks the parameter names against allowed and reads limit
// (def when absent, 1–maxLimit) and cursor. Messages name the parameter,
// never its value.
func parsePage(query url.Values, allowed map[string]bool, def, maxLimit int) (limit int, after uuid.UUID, status int, code, msg string) {
	for k := range query {
		if !allowed[k] {
			return 0, uuid.Nil, http.StatusBadRequest, "invalid_filter", "unknown query parameter"
		}
	}
	limit = def
	if v, ok := query["limit"]; ok {
		n, err := strconv.Atoi(v[0])
		if len(v) != 1 || err != nil || n < 1 || n > maxLimit {
			return 0, uuid.Nil, http.StatusBadRequest, "invalid_limit", "limit must be a single integer between 1 and " + strconv.Itoa(maxLimit)
		}
		limit = n
	}
	if v, ok := query["cursor"]; ok {
		c, valid := decodeCursor(v[0])
		if len(v) != 1 || !valid {
			return 0, uuid.Nil, http.StatusBadRequest, "invalid_cursor", "cursor must be the next_cursor of a previous page"
		}
		after = c
	}
	return limit, after, 0, "", ""
}

// writeContactError maps contact and audience errors to the contract. Client errors carry
// no submitted values; anything else is logged without content.
func (s *Server) writeContactError(w http.ResponseWriter, r *http.Request, op string, err error) {
	var ce *contacts.Error
	switch {
	case errors.As(err, &ce):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", ce.Error())
	case errors.Is(err, contacts.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, contacts.ErrExists):
		writeError(w, http.StatusConflict, "contact_exists", "this project already has a contact with this address")
	case errors.Is(err, contacts.ErrLimit):
		writeError(w, http.StatusUnprocessableEntity, "contact_limit_reached", "the project has reached its contact limit")
	case errors.Is(err, contacts.ErrAudienceExists):
		writeError(w, http.StatusConflict, "audience_exists", "this project already has an audience with this name")
	case errors.Is(err, contacts.ErrAudienceLimit):
		writeError(w, http.StatusUnprocessableEntity, "audience_limit_reached", "the project has reached its audience limit")
	case errors.Is(err, contacts.ErrAudienceFull):
		writeError(w, http.StatusUnprocessableEntity, "audience_full", "the audience would exceed its contact limit; nothing was added")
	case errors.Is(err, contacts.ErrBusy):
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
