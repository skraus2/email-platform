package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/emails"
)

// maxRequestBytes caps a send request; bodies are limited to 512 KiB each.
const maxRequestBytes = 1 << 20

// EmailService is what the HTTP layer needs from the emails module.
type EmailService interface {
	Create(ctx context.Context, projectID uuid.UUID, in emails.CreateInput) (emails.Email, error)
	Get(ctx context.Context, projectID, id uuid.UUID) (emails.Email, error)
}

// createEmailRequest mirrors CreateEmailRequest in api/openapi.yaml. The
// template fields are declared so that using them yields a precise 422
// instead of a generic unknown-field error.
type createEmailRequest struct {
	From            string          `json:"from"`
	To              string          `json:"to"`
	Subject         string          `json:"subject"`
	Text            string          `json:"text"`
	HTML            string          `json:"html"`
	TemplateID      json.RawMessage `json:"template_id"`
	TemplateVersion json.RawMessage `json:"template_version"`
	Variables       json.RawMessage `json:"variables"`
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
	if req.TemplateID != nil || req.TemplateVersion != nil || req.Variables != nil {
		writeError(w, http.StatusUnprocessableEntity, "templates_not_supported",
			"templates are not supported yet; send subject with text and/or html")
		return
	}

	p := principalFrom(r.Context())
	e, err := s.emails.Create(r.Context(), p.ProjectID, emails.CreateInput{
		From: req.From, To: req.To, Subject: req.Subject, Text: req.Text, HTML: req.HTML,
		IdempotencyKey: idemKey,
	})
	var ve *emails.ValidationError
	var rl *emails.RateLimitError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", ve.Error())
		return
	case errors.Is(err, emails.ErrInvalidIdempotencyKey):
		writeInvalidIdempotencyKey(w)
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

func writeInvalidIdempotencyKey(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_idempotency_key",
		"Idempotency-Key must be sent once, with 1-255 printable ASCII characters and no spaces")
}

// decodeStrict reads exactly one JSON object with known fields only. It
// returns a non-zero status with error code and message on failure.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) (int, string, string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return http.StatusRequestEntityTooLarge, "payload_too_large", "request body must be at most 1 MiB"
	}
	if err != nil {
		return http.StatusBadRequest, "invalid_json", "could not read request body"
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
