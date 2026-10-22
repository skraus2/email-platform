package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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
	Status       string            `json:"status"`
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
