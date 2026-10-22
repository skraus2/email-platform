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

	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/templates"
)

// AutomationService is what the HTTP layer needs for automation definitions.
type AutomationService interface {
	Create(ctx context.Context, projectID uuid.UUID, in automations.CreateInput) (automations.Automation, error)
	Get(ctx context.Context, projectID, id uuid.UUID) (automations.Automation, error)
	List(ctx context.Context, projectID uuid.UUID, status string, after uuid.UUID, limit int) ([]automations.Automation, bool, error)
	Update(ctx context.Context, projectID, id uuid.UUID, in automations.UpdateInput) (automations.Automation, error)
}

// automationResponse mirrors Automation / AutomationWithCounts in
// api/openapi.yaml; RunCounts is only set for a single automation.
type automationResponse struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Status       string              `json:"status"`
	Trigger      automationTrigger   `json:"trigger"`
	AllowReentry bool                `json:"allow_reentry"`
	Steps        []automations.Step  `json:"steps"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
	RunCounts    *automationRunCount `json:"run_counts,omitempty"`
}

type automationTrigger struct {
	Event string `json:"event"`
}

type automationRunCount struct {
	Active    int `json:"active"`
	Completed int `json:"completed"`
	Exited    int `json:"exited"`
	Canceled  int `json:"canceled"`
	Failed    int `json:"failed"`
}

func toAutomationResponse(a automations.Automation) automationResponse {
	r := automationResponse{
		ID: a.ID.String(), Name: a.Name, Status: a.Status, Trigger: automationTrigger{Event: a.TriggerEvent},
		AllowReentry: a.AllowReentry, Steps: a.Steps, CreatedAt: a.CreatedAt.UTC(), UpdatedAt: a.UpdatedAt.UTC(),
	}
	if c := a.RunCounts; c != nil {
		r.RunCounts = &automationRunCount{
			Active: c[automations.RunActive], Completed: c[automations.RunCompleted], Exited: c[automations.RunExited],
			Canceled: c[automations.RunCanceled], Failed: c[automations.RunFailed],
		}
	}
	return r
}

type automationListResponse struct {
	Data       []automationResponse `json:"data"`
	NextCursor *string              `json:"next_cursor"`
}

type createAutomationRequest struct {
	Name         string            `json:"name"`
	Trigger      automationTrigger `json:"trigger"`
	AllowReentry bool              `json:"allow_reentry"`
	Status       *string           `json:"status"`
	Steps        json.RawMessage   `json:"steps"`
}

type updateAutomationRequest struct {
	Name   json.RawMessage `json:"name"`
	Status json.RawMessage `json:"status"`
}

func (s *Server) handleCreateAutomation(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req createAutomationRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	a, err := s.automations.Create(r.Context(), principalFrom(r.Context()).ProjectID, automations.CreateInput{
		Name: req.Name, TriggerEvent: req.Trigger.Event, AllowReentry: req.AllowReentry, Status: req.Status, Steps: req.Steps,
	})
	if err != nil {
		s.writeAutomationError(w, r, "create automation", err)
		return
	}
	writeJSON(w, http.StatusCreated, toAutomationResponse(a))
}

func (s *Server) handleGetAutomation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := s.automations.Get(r.Context(), principalFrom(r.Context()).ProjectID, id)
	if err != nil {
		s.writeAutomationError(w, r, "get automation", err)
		return
	}
	writeJSON(w, http.StatusOK, toAutomationResponse(a))
}

var automationListParams = map[string]bool{"status": true, "limit": true, "cursor": true}

func (s *Server) handleListAutomations(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	limit, after, status, code, msg := parsePage(query, automationListParams, defaultPage, automations.MaxPage)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	var st string
	if v, ok := query["status"]; ok {
		if len(v) != 1 || !automations.IsStatus(v[0]) {
			writeError(w, http.StatusBadRequest, "invalid_filter", "status must be an automation status")
			return
		}
		st = v[0]
	}
	page, more, err := s.automations.List(r.Context(), principalFrom(r.Context()).ProjectID, st, after, limit)
	if err != nil {
		s.writeAutomationError(w, r, "list automations", err)
		return
	}
	resp := automationListResponse{Data: make([]automationResponse, 0, len(page))}
	for _, a := range page {
		resp.Data = append(resp.Data, toAutomationResponse(a))
	}
	if more {
		c := encodeCursor(page[len(page)-1].ID)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateAutomation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || !requireJSON(w, r) {
		return
	}
	var req updateAutomationRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	var in automations.UpdateInput
	for _, f := range []struct {
		field string
		raw   json.RawMessage
		dst   **string
	}{{"name", req.Name, &in.Name}, {"status", req.Status, &in.Status}} {
		if f.raw == nil {
			continue
		}
		var v *string
		if err := json.Unmarshal(f.raw, &v); err != nil || v == nil {
			s.writeAutomationError(w, r, "update automation", &automations.Error{Field: f.field, Reason: "must be a string"})
			return
		}
		*f.dst = v
	}
	a, err := s.automations.Update(r.Context(), principalFrom(r.Context()).ProjectID, id, in)
	if err != nil {
		s.writeAutomationError(w, r, "update automation", err)
		return
	}
	writeJSON(w, http.StatusOK, toAutomationResponse(a))
}

// writeAutomationError maps automation errors to the contract. Client
// errors carry no submitted values.
func (s *Server) writeAutomationError(w http.ResponseWriter, r *http.Request, op string, err error) {
	var ae *automations.Error
	var te *templates.Error
	switch {
	case errors.As(err, &ae):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", ae.Error())
	case errors.As(err, &te):
		writeError(w, http.StatusUnprocessableEntity, te.Code, te.Error())
	case errors.Is(err, automations.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, automations.ErrTemplateNotFound):
		writeError(w, http.StatusUnprocessableEntity, "template_not_found", "no such template or version in this project")
	case errors.Is(err, automations.ErrMissingUnsubscribe):
		writeError(w, http.StatusUnprocessableEntity, "template_missing_unsubscribe",
			"the template version of a send step must declare unsubscribe_url as a string, print {{.unsubscribe_url}} outside conditions in its text part and link it as an <a href> outside conditions in its HTML part")
	case errors.Is(err, automations.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "the project created as many automations in the last hour as it may; retry later")
	case errors.Is(err, automations.ErrLimit):
		writeError(w, http.StatusUnprocessableEntity, "automation_limit_reached", "the project has reached its limit of automations that are not archived")
	case errors.Is(err, emails.ErrSenderNotVerified):
		writeError(w, http.StatusUnprocessableEntity, "sender_not_verified", "from is not a verified sender address or domain of this project")
	case errors.Is(err, automations.ErrArchived):
		writeError(w, http.StatusConflict, "automation_archived", "the automation is archived and can no longer change")
	case errors.Is(err, automations.ErrBusy):
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

// maxContactEventBytes bounds a contact event request (data is at most
// 8 KiB as compact JSON).
const maxContactEventBytes = 64 << 10

// ContactEventService records contact events and starts automation runs.
type ContactEventService interface {
	Report(ctx context.Context, projectID uuid.UUID, in automations.EventInput) (automations.Event, error)
}

type createContactEventRequest struct {
	Name      string          `json:"name"`
	ContactID *string         `json:"contact_id"`
	Email     *string         `json:"email"`
	Data      json.RawMessage `json:"data"`
}

// contactEventResponse mirrors ContactEvent in api/openapi.yaml.
type contactEventResponse struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	ContactID string           `json:"contact_id"`
	CreatedAt time.Time        `json:"created_at"`
	Runs      []startedRunJSON `json:"runs"`
}

type startedRunJSON struct {
	ID           string `json:"id"`
	AutomationID string `json:"automation_id"`
}

func (s *Server) handleCreateContactEvent(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	// As for emails: a present but empty, repeated or malformed key is
	// refused rather than treated as "no key".
	var key string
	if vals, ok := r.Header["Idempotency-Key"]; ok {
		if len(vals) != 1 || emails.ValidateIdempotencyKey(vals[0]) != nil {
			writeInvalidIdempotencyKey(w)
			return
		}
		key = vals[0]
	}
	var req createContactEventRequest
	if status, code, msg := decodeStrictLimit(w, r, &req, maxContactEventBytes); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	in := automations.EventInput{Name: req.Name, Data: req.Data, IdempotencyKey: key}
	if req.ContactID != nil {
		id, err := uuid.Parse(*req.ContactID)
		if err != nil || len(*req.ContactID) != 36 || id == uuid.Nil {
			s.writeContactEventError(w, r, &automations.Error{Field: "contact_id", Reason: "must be a UUID"})
			return
		}
		in.ContactID = id
	}
	if req.Email != nil {
		if *req.Email == "" {
			s.writeContactEventError(w, r, &automations.Error{Field: "email", Reason: "must be a single plain ASCII email address"})
			return
		}
		in.Email = *req.Email
	}
	if string(req.Data) == "null" {
		in.Data = nil
	}
	ev, err := s.contactEvents.Report(r.Context(), principalFrom(r.Context()).ProjectID, in)
	if err != nil {
		s.writeContactEventError(w, r, err)
		return
	}
	resp := contactEventResponse{ID: ev.ID.String(), Name: ev.Name, ContactID: ev.ContactID.String(), CreatedAt: ev.CreatedAt.UTC(), Runs: make([]startedRunJSON, 0, len(ev.Runs))}
	for _, run := range ev.Runs {
		resp.Runs = append(resp.Runs, startedRunJSON{ID: run.ID.String(), AutomationID: run.AutomationID.String()})
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) writeContactEventError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *automations.Error
	var rl *automations.RateLimitError
	switch {
	case errors.As(err, &ae):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", ae.Error())
	case errors.Is(err, emails.ErrInvalidIdempotencyKey):
		writeInvalidIdempotencyKey(w)
	case errors.Is(err, automations.ErrContactNotFound):
		writeError(w, http.StatusUnprocessableEntity, "contact_not_found", "no such contact in this project; nothing was stored")
	case errors.Is(err, automations.ErrKeyReused):
		writeError(w, http.StatusConflict, "idempotency_key_reused",
			"this Idempotency-Key was already used with a different request; use a new key for a new event")
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter/time.Second)))
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			"the project's contact event rate limit is exhausted; retry after the time in Retry-After")
	default:
		// The outcome is unknown if the connection broke during commit; a
		// retry with the same Idempotency-Key is safe.
		s.writeAutomationError(w, r, "report contact event", err)
	}
}

// AutomationRunService reads and cancels automation runs.
type AutomationRunService interface {
	Get(ctx context.Context, projectID, id uuid.UUID) (automations.Run, error)
	List(ctx context.Context, projectID, automationID uuid.UUID, f automations.RunFilter, after uuid.UUID, limit int) ([]automations.Run, bool, error)
	Cancel(ctx context.Context, projectID, id uuid.UUID) (automations.Run, error)
}

// runResponse mirrors AutomationRun / AutomationRunWithEmails in
// api/openapi.yaml; the trigger event's data is never part of it.
type runResponse struct {
	ID           string     `json:"id"`
	AutomationID string     `json:"automation_id"`
	ContactID    string     `json:"contact_id"`
	Status       string     `json:"status"`
	Reason       *string    `json:"reason"`
	Step         int        `json:"step"`
	NextStepAt   *time.Time `json:"next_step_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// runWithEmailsResponse is a single run with the emails it created.
type runWithEmailsResponse struct {
	runResponse
	Emails []runEmailResult `json:"emails"`
}

type runEmailResult struct {
	ID     string `json:"id"`
	Step   int    `json:"step"`
	Status string `json:"status"`
}

func toRunResponse(r automations.Run) runResponse {
	return runResponse{
		ID: r.ID.String(), AutomationID: r.AutomationID.String(), ContactID: r.ContactID.String(), Status: r.Status,
		Reason: r.Reason, Step: r.Step, NextStepAt: utcPtr(r.NextStepAt), CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(), FinishedAt: utcPtr(r.FinishedAt),
	}
}

func toRunWithEmailsResponse(r automations.Run) runWithEmailsResponse {
	resp := runWithEmailsResponse{runResponse: toRunResponse(r), Emails: make([]runEmailResult, 0, len(r.Emails))}
	for _, e := range r.Emails {
		resp.Emails = append(resp.Emails, runEmailResult{ID: e.ID.String(), Step: e.Step, Status: e.Status})
	}
	return resp
}

type runListResponse struct {
	Data       []runResponse `json:"data"`
	NextCursor *string       `json:"next_cursor"`
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	run, err := s.runs.Get(r.Context(), principalFrom(r.Context()).ProjectID, id)
	if err != nil {
		s.writeRunError(w, r, "get run", err)
		return
	}
	writeJSON(w, http.StatusOK, toRunWithEmailsResponse(run))
}

var runListParams = map[string]bool{"status": true, "contact_id": true, "limit": true, "cursor": true}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", "the query string is malformed")
		return
	}
	limit, after, status, code, msg := parsePage(query, runListParams, defaultPage, automations.MaxPage)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	var f automations.RunFilter
	if v, ok := query["status"]; ok {
		if len(v) != 1 || !automations.IsRunStatus(v[0]) {
			writeError(w, http.StatusBadRequest, "invalid_filter", "status must be a run status")
			return
		}
		f.Status = v[0]
	}
	if v, ok := query["contact_id"]; ok {
		c, err := uuid.Parse(v[0])
		if len(v) != 1 || err != nil || len(v[0]) != 36 {
			writeError(w, http.StatusBadRequest, "invalid_filter", "contact_id must be a single UUID")
			return
		}
		f.ContactID = c
	}
	page, more, err := s.runs.List(r.Context(), principalFrom(r.Context()).ProjectID, id, f, after, limit)
	if err != nil {
		s.writeRunError(w, r, "list runs", err)
		return
	}
	resp := runListResponse{Data: make([]runResponse, 0, len(page))}
	for _, run := range page {
		resp.Data = append(resp.Data, toRunResponse(run))
	}
	if more {
		c := encodeCursor(page[len(page)-1].ID)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	run, err := s.runs.Cancel(r.Context(), principalFrom(r.Context()).ProjectID, id)
	if err != nil {
		s.writeRunError(w, r, "cancel run", err)
		return
	}
	writeJSON(w, http.StatusOK, toRunResponse(run))
}

func (s *Server) writeRunError(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, automations.ErrRunNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, automations.ErrRunNotCancelable):
		writeError(w, http.StatusConflict, "run_not_cancelable", "the run already ended; GET it for its final state")
	default:
		s.writeAutomationError(w, r, op, err)
	}
}
