package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/contacts"
)

// AudienceService is what the HTTP layer needs for audiences.
type AudienceService interface {
	Create(ctx context.Context, projectID uuid.UUID, name string) (contacts.Audience, error)
	Get(ctx context.Context, projectID, id uuid.UUID) (contacts.Audience, int64, error)
	List(ctx context.Context, projectID, after uuid.UUID, limit int) ([]contacts.Audience, bool, error)
	Add(ctx context.Context, projectID, audienceID uuid.UUID, ids []uuid.UUID) (contacts.AddResult, error)
	AddOne(ctx context.Context, projectID, audienceID, contactID uuid.UUID) error
	Remove(ctx context.Context, projectID, audienceID, contactID uuid.UUID) error
	Members(ctx context.Context, projectID, audienceID, after uuid.UUID, limit int) ([]contacts.Contact, bool, error)
}

// audienceResponse mirrors the Audience schema in api/openapi.yaml;
// ContactCount is only set for a single audience.
type audienceResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"created_at"`
	ContactCount *int64    `json:"contact_count,omitempty"`
}

func toAudienceResponse(a contacts.Audience) audienceResponse {
	return audienceResponse{ID: a.ID.String(), Name: a.Name, CreatedAt: a.CreatedAt.UTC()}
}

type audienceListResponse struct {
	Data       []audienceResponse `json:"data"`
	NextCursor *string            `json:"next_cursor"`
}

type createAudienceRequest struct {
	Name string `json:"name"`
}

type addAudienceContactsRequest struct {
	ContactIDs []string `json:"contact_ids"`
}

var pageParams = map[string]bool{"limit": true, "cursor": true}

func (s *Server) handleCreateAudience(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req createAudienceRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	a, err := s.audiences.Create(r.Context(), principalFrom(r.Context()).ProjectID, req.Name)
	if err != nil {
		s.writeContactError(w, r, "create audience", err)
		return
	}
	writeJSON(w, http.StatusCreated, toAudienceResponse(a))
}

func (s *Server) handleGetAudience(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, n, err := s.audiences.Get(r.Context(), principalFrom(r.Context()).ProjectID, id)
	if err != nil {
		s.writeContactError(w, r, "get audience", err)
		return
	}
	resp := toAudienceResponse(a)
	resp.ContactCount = &n
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListAudiences(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	limit, after, status, code, msg := parsePage(query, pageParams, defaultPage, contacts.MaxPage)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	page, more, err := s.audiences.List(r.Context(), principalFrom(r.Context()).ProjectID, after, limit)
	if err != nil {
		s.writeContactError(w, r, "list audiences", err)
		return
	}
	resp := audienceListResponse{Data: make([]audienceResponse, 0, len(page))}
	for _, a := range page {
		resp.Data = append(resp.Data, toAudienceResponse(a))
	}
	if more {
		c := encodeCursor(page[len(page)-1].ID)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAddAudienceContacts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || !requireJSON(w, r) {
		return
	}
	var req addAudienceContactsRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	ids := make([]uuid.UUID, 0, len(req.ContactIDs))
	for _, v := range req.ContactIDs {
		cid, err := uuid.Parse(v)
		if err != nil || len(v) != 36 {
			s.writeContactError(w, r, "add audience contacts", &contacts.Error{Field: "contact_ids", Reason: "must hold UUIDs"})
			return
		}
		ids = append(ids, cid)
	}
	res, err := s.audiences.Add(r.Context(), principalFrom(r.Context()).ProjectID, id, ids)
	if err != nil {
		s.writeContactError(w, r, "add audience contacts", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": res.Added, "already_members": res.AlreadyMembers, "not_found": res.NotFound})
}

// memberIDs parses the audience and contact ids of a membership path.
func memberIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	id, err1 := uuid.Parse(r.PathValue("id"))
	cid, err2 := uuid.Parse(r.PathValue("contact_id"))
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return uuid.Nil, uuid.Nil, false
	}
	return id, cid, true
}

func (s *Server) handleAddAudienceContact(w http.ResponseWriter, r *http.Request) {
	id, cid, ok := memberIDs(w, r)
	if !ok {
		return
	}
	if err := s.audiences.AddOne(r.Context(), principalFrom(r.Context()).ProjectID, id, cid); err != nil {
		s.writeContactError(w, r, "add audience contact", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveAudienceContact(w http.ResponseWriter, r *http.Request) {
	id, cid, ok := memberIDs(w, r)
	if !ok {
		return
	}
	if err := s.audiences.Remove(r.Context(), principalFrom(r.Context()).ProjectID, id, cid); err != nil {
		s.writeContactError(w, r, "remove audience contact", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAudienceContacts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	limit, after, status, code, msg := parsePage(query, pageParams, defaultPage, contacts.MaxPage)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	page, more, err := s.audiences.Members(r.Context(), principalFrom(r.Context()).ProjectID, id, after, limit)
	if err != nil {
		s.writeContactError(w, r, "list audience contacts", err)
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
