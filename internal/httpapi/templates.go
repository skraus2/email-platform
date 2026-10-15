package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/templates"
)

// maxPublishBytes caps a publish request: text and HTML may be 512 KiB each,
// and JSON escaping can inflate HTML considerably.
const maxPublishBytes = 3 << 20

// TemplateService is what the HTTP layer needs from the templates module.
type TemplateService interface {
	Create(ctx context.Context, projectID uuid.UUID, slug, name string) (templates.Template, error)
	Publish(ctx context.Context, projectID, templateID uuid.UUID, src templates.Source, rawSchema []byte) (templates.Version, error)
	Get(ctx context.Context, projectID, id uuid.UUID) (templates.Template, error)
	GetVersion(ctx context.Context, projectID, id uuid.UUID, version int) (templates.Version, error)
}

// templateResponse mirrors the Template schema in api/openapi.yaml.
type templateResponse struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	LatestVersion *int      `json:"latest_version"`
	CreatedAt     time.Time `json:"created_at"`
}

func toTemplateResponse(t templates.Template) templateResponse {
	r := templateResponse{ID: t.ID.String(), Slug: t.Slug, Name: t.Name, CreatedAt: t.CreatedAt.UTC()}
	if t.LatestVersion > 0 {
		v := t.LatestVersion
		r.LatestVersion = &v
	}
	return r
}

// versionResponse mirrors the TemplateVersion schema in api/openapi.yaml.
type versionResponse struct {
	TemplateID string          `json:"template_id"`
	Version    int             `json:"version"`
	Subject    string          `json:"subject"`
	Text       string          `json:"text"`
	HTML       *string         `json:"html"`
	Variables  json.RawMessage `json:"variables"`
	CreatedAt  time.Time       `json:"created_at"`
}

type createTemplateRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type publishVersionRequest struct {
	Subject   string          `json:"subject"`
	Text      string          `json:"text"`
	HTML      string          `json:"html"`
	Variables json.RawMessage `json:"variables"`
}

func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	return true
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req createTemplateRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	p := principalFrom(r.Context())
	t, err := s.templates.Create(r.Context(), p.ProjectID, req.Slug, req.Name)
	if err != nil {
		s.writeTemplateError(w, r, "create template", err)
		return
	}
	writeJSON(w, http.StatusCreated, toTemplateResponse(t))
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	p := principalFrom(r.Context())
	t, err := s.templates.Get(r.Context(), p.ProjectID, id)
	if err != nil {
		s.writeTemplateError(w, r, "get template", err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateResponse(t))
}

func (s *Server) handlePublishVersion(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var req publishVersionRequest
	if status, code, msg := decodeStrictLimit(w, r, &req, maxPublishBytes); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	p := principalFrom(r.Context())
	v, err := s.templates.Publish(r.Context(), p.ProjectID, id,
		templates.Source{Subject: req.Subject, Text: req.Text, HTML: req.HTML}, req.Variables)
	if err != nil {
		s.writeTemplateError(w, r, "publish template version", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"template_id": v.TemplateID.String(), "version": v.Version, "created_at": v.CreatedAt.UTC(),
	})
}

func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	// Version 0 would mean "latest" to the store; the path names concrete
	// versions only.
	n, nerr := strconv.Atoi(r.PathValue("version"))
	if err != nil || nerr != nil || n < 1 || strconv.Itoa(n) != r.PathValue("version") {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	p := principalFrom(r.Context())
	v, err := s.templates.GetVersion(r.Context(), p.ProjectID, id, n)
	if err != nil {
		s.writeTemplateError(w, r, "get template version", err)
		return
	}
	// The stored schema is jsonb (normalised formatting); answer with the
	// canonical form the version was published with.
	schema, err := templates.ParseSchema(v.Schema)
	if err != nil {
		// A stored schema is always valid; never report this as a client error.
		s.logger.ErrorContext(r.Context(), "get template version: stored schema unreadable",
			slog.String("project_id", p.ProjectID.String()), slog.String("template_id", id.String()))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	resp := versionResponse{
		TemplateID: v.TemplateID.String(), Version: v.Version, Subject: v.Source.Subject, Text: v.Source.Text,
		Variables: schema.Canonical(), CreatedAt: v.CreatedAt.UTC(),
	}
	if v.Source.HTML != "" {
		html := v.Source.HTML
		resp.HTML = &html
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeTemplateError maps template errors to the contract. Client errors
// carry no submitted values; anything else is logged without content.
func (s *Server) writeTemplateError(w http.ResponseWriter, r *http.Request, op string, err error) {
	var te *templates.Error
	switch {
	case errors.As(err, &te):
		writeError(w, http.StatusUnprocessableEntity, te.Code, te.Error())
	case errors.Is(err, templates.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, templates.ErrSlugTaken):
		writeError(w, http.StatusConflict, "slug_taken", "this project already has a template with this slug")
	case errors.Is(err, templates.ErrTemplateLimit):
		writeError(w, http.StatusUnprocessableEntity, "template_limit_reached", "the project has reached its template limit")
	case errors.Is(err, templates.ErrVersionLimit):
		writeError(w, http.StatusUnprocessableEntity, "version_limit_reached", "the template has reached its version limit")
	case errors.Is(err, templates.ErrStorageLimit):
		writeError(w, http.StatusUnprocessableEntity, "template_storage_limit_reached",
			"the project has reached its template storage limit")
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
