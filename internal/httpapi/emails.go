package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/templates"
)

// maxRequestBytes caps a send request; bodies are limited to 512 KiB each.
const maxRequestBytes = 1 << 20

// EmailService is what the HTTP layer needs from the emails module.
type EmailService interface {
	Create(ctx context.Context, projectID uuid.UUID, in emails.CreateInput) (emails.Email, error)
	Get(ctx context.Context, projectID, id uuid.UUID) (emails.Email, error)
	List(ctx context.Context, projectID uuid.UUID, f emails.ListFilter, limit int) ([]emails.Email, bool, error)
	Events(ctx context.Context, projectID, id, after uuid.UUID, limit int) ([]events.Event, bool, error)
	Cancel(ctx context.Context, projectID, id uuid.UUID) (emails.Email, error)
}

// createEmailRequest mirrors CreateEmailRequest in api/openapi.yaml. The
// template fields stay raw so that their type errors get precise 422s.
type createEmailRequest struct {
	From            string          `json:"from"`
	To              string          `json:"to"`
	Subject         *string         `json:"subject"`
	Text            *string         `json:"text"`
	HTML            *string         `json:"html"`
	TemplateID      json.RawMessage `json:"template_id"`
	TemplateVersion json.RawMessage `json:"template_version"`
	Variables       json.RawMessage `json:"variables"`
	ScheduledAt     *string         `json:"scheduled_at"`
}

func (s *Server) handleCreateEmail(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	// Present but empty, repeated or malformed keys are refused rather than
	// silently treated as "no key": the client asked for deduplication.
	var idemKey string
	if vals, ok := r.Header["Idempotency-Key"]; ok {
		if len(vals) != 1 || emails.ValidateIdempotencyKey(vals[0]) != nil {
			writeInvalidIdempotencyKey(w)
			return
		}
		idemKey = vals[0]
	}

	var req createEmailRequest
	if status, code, msg := decodeStrict(w, r, &req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	in := emails.CreateInput{
		From: req.From, To: req.To, Subject: deref(req.Subject), Text: deref(req.Text), HTML: deref(req.HTML),
		IdempotencyKey: idemKey,
	}
	if field, msg := parseTemplateForm(req, &in); field != "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", field+": "+msg)
		return
	}
	if req.ScheduledAt != nil {
		at, err := time.Parse(time.RFC3339Nano, *req.ScheduledAt)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "scheduled_at: must be an RFC 3339 timestamp")
			return
		}
		in.ScheduledAt = &at
	}

	p := principalFrom(r.Context())
	e, err := s.emails.Create(r.Context(), p.ProjectID, in)
	var ve *emails.ValidationError
	var rl *emails.RateLimitError
	var te *templates.Error
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", ve.Error())
		return
	case errors.As(err, &te):
		writeError(w, http.StatusUnprocessableEntity, te.Code, te.Error())
		return
	case errors.Is(err, emails.ErrRenderBusy):
		writeUnavailable(w)
		return
	case errors.Is(err, emails.ErrTemplateNotFound):
		writeError(w, http.StatusUnprocessableEntity, "template_not_found",
			"no such template or version in this project; nothing was sent")
		return
	case errors.Is(err, emails.ErrInvalidIdempotencyKey):
		writeInvalidIdempotencyKey(w)
		return
	case errors.Is(err, emails.ErrSenderNotVerified):
		writeError(w, http.StatusUnprocessableEntity, "sender_not_verified",
			"from is not a verified sender address or domain of this project; nothing was sent")
		return
	case errors.Is(err, emails.ErrScheduleLimit):
		writeError(w, http.StatusUnprocessableEntity, "schedule_limit_reached",
			"the project has reached its limit of planned sends waiting; nothing was stored")
		return
	case errors.Is(err, emails.ErrRecipientSuppressed):
		writeError(w, http.StatusUnprocessableEntity, "recipient_suppressed",
			"the recipient is on this project's suppression list; nothing was sent")
		return
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter/time.Second)))
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			"the project's send rate limit is exhausted; retry after the time in Retry-After")
		return
	case errors.Is(err, emails.ErrIdempotencyKeyReused):
		writeError(w, http.StatusConflict, "idempotency_key_reused",
			"this Idempotency-Key was already used with a different request; use a new key for a new email")
		return
	case database.Unavailable(err):
		// The outcome is unknown if the connection broke during commit; the
		// contract tells clients to retry with the same Idempotency-Key.
		s.logger.ErrorContext(r.Context(), "create email: database unavailable",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeUnavailable(w)
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "create email failed",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	if e.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	// The creation response always says "queued"; a replay repeats it even
	// if the email has moved on (its current status is on GET).
	writeJSON(w, http.StatusAccepted, map[string]string{"id": e.ID.String(), "status": "queued"})
}

// handleCancelEmail cancels a queued email of the project.
func (s *Server) handleCancelEmail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := principalFrom(r.Context())
	e, err := s.emails.Cancel(r.Context(), p.ProjectID, id)
	switch {
	case errors.Is(err, emails.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, emails.ErrNotCancelable):
		writeError(w, http.StatusConflict, "email_not_cancelable", "only a queued email can be cancelled; GET it for its current status")
	case database.Unavailable(err):
		s.logger.ErrorContext(r.Context(), "cancel email: database unavailable",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeUnavailable(w)
	case err != nil:
		s.logger.ErrorContext(r.Context(), "cancel email failed",
			slog.String("project_id", p.ProjectID.String()), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	default:
		writeJSON(w, http.StatusOK, toEmailResponse(e))
	}
}

func writeInvalidIdempotencyKey(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_idempotency_key",
		"Idempotency-Key must be sent once, with 1-255 printable ASCII characters and no spaces")
}

// decodeStrict reads exactly one JSON object with known fields only. It
// returns a non-zero status with error code and message on failure.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) (int, string, string) {
	return decodeStrictLimit(w, r, v, maxRequestBytes)
}

// decodeStrictLimit is decodeStrict with a body limit of limit bytes (a
// whole number of MiB or, below 1 MiB, of KiB).
func decodeStrictLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) (int, string, string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		if limit < 1<<20 {
			return http.StatusRequestEntityTooLarge, "payload_too_large", fmt.Sprintf("request body must be at most %d KiB", limit>>10)
		}
		return http.StatusRequestEntityTooLarge, "payload_too_large", fmt.Sprintf("request body must be at most %d MiB", limit>>20)
	}
	if err != nil {
		return http.StatusBadRequest, "invalid_json", "could not read request body"
	}
	if err := exactKeys(body, v); err != nil {
		return http.StatusBadRequest, "invalid_json", "request body must be a JSON object matching the schema: " + err.Error()
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return http.StatusBadRequest, "invalid_json", "request body must be a JSON object matching the schema: " + jsonProblem(err)
	}
	if dec.More() {
		return http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object"
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object"
	}
	return 0, "", ""
}

// jsonProblem describes a decode error without echoing request content.
func jsonProblem(err error) string {
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case errors.As(err, &typeErr):
		return "field " + typeErr.Field + " has the wrong type"
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "malformed JSON"
	default:
		// json reports unknown fields as `json: unknown field "name"`.
		return "unknown or invalid field"
	}
}

// exactKeys checks the top-level keys of a JSON object against the json tags
// of the struct v points to: encoding/json alone would match keys
// case-insensitively and silently keep the last of duplicate keys, so two
// readers of the same body could see different values.
func exactKeys(body []byte, v any) error {
	allowed := map[string]bool{}
	t := reflect.TypeOf(v).Elem()
	for field := range t.Fields() {
		if name, _, _ := strings.Cut(field.Tag.Get("json"), ","); name != "" && name != "-" {
			allowed[name] = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("expected an object")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errors.New("malformed object")
		}
		key, _ := tok.(string)
		switch {
		case !allowed[key]:
			return errors.New("unknown field (field names are case-sensitive)")
		case seen[key]:
			return errors.New("duplicate field")
		}
		seen[key] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return errors.New("malformed value")
		}
	}
	return nil
}

// parseTemplateForm fills the template fields of in from the request. It
// returns the offending field and a message (never the submitted value).
func parseTemplateForm(req createEmailRequest, in *emails.CreateInput) (string, string) {
	present := func(raw json.RawMessage) bool { return raw != nil }
	if !present(req.TemplateID) {
		switch {
		case present(req.TemplateVersion):
			return "template_id", "is required with template_version"
		case present(req.Variables):
			return "template_id", "is required with variables"
		}
		return "", ""
	}
	var id string
	if json.Unmarshal(req.TemplateID, &id) != nil {
		return "template_id", "must be a UUID"
	}
	tid, err := uuid.Parse(id)
	if err != nil || tid == uuid.Nil || len(id) != 36 {
		return "template_id", "must be a UUID"
	}
	// The subject and bodies come from the template; sending them at all,
	// even empty, is a mixed request.
	for _, f := range []struct {
		name string
		v    *string
	}{{"subject", req.Subject}, {"text", req.Text}, {"html", req.HTML}} {
		if f.v != nil {
			return f.name, "must not be combined with template_id"
		}
	}
	in.TemplateID = tid
	if !present(req.TemplateVersion) {
		return "template_version", `is required: a version number or "published"`
	}
	var published string
	var number int
	switch {
	case json.Unmarshal(req.TemplateVersion, &published) == nil && published == "published":
		in.TemplateVersion = 0
	case json.Unmarshal(req.TemplateVersion, &number) == nil && number >= 1 && bytes.Equal(bytes.TrimSpace(req.TemplateVersion), []byte(strconv.Itoa(number))):
		in.TemplateVersion = number
	default:
		return "template_version", `must be a positive integer or "published"`
	}
	in.Variables = req.Variables
	return "", ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
