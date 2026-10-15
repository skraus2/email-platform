// Package emails accepts send requests: validation, persistence of the email
// with its content, and enqueueing the dispatch job in the same transaction.
package emails

import (
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Limits for a send request.
const (
	MaxAddressLen  = 254
	MaxSubjectLen  = 255 // characters
	MaxBodyBytes   = 512 << 10
	headerForbidCh = "\r\n\x00"
)

// CreateInput is one send request for one recipient: either the raw form
// (Subject with Text and/or HTML) or the template form (TemplateID set).
type CreateInput struct {
	From    string
	To      string
	Subject string
	Text    string
	HTML    string
	// TemplateID selects a template of the project (uuid.Nil: raw form).
	TemplateID uuid.UUID
	// TemplateVersion is the version to send; 0 means the published
	// (highest) version at accept time.
	TemplateVersion int
	// Variables is the JSON object of template variables (nil: none).
	Variables []byte
	// IdempotencyKey is optional (empty = no deduplication). It is not part
	// of the payload fingerprint.
	IdempotencyKey string
}

// ValidationError names the invalid field. Its message never repeats the
// submitted value, so it is safe to return to clients and to log.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

// Validate checks the request against the API contract.
func (in CreateInput) Validate() error {
	if in.IdempotencyKey != "" {
		if err := ValidateIdempotencyKey(in.IdempotencyKey); err != nil {
			return err
		}
	}
	if err := validateAddress("from", in.From); err != nil {
		return err
	}
	if err := validateAddress("to", in.To); err != nil {
		return err
	}
	if in.TemplateID != uuid.Nil {
		// The subject and bodies come from the template version.
		for _, f := range []struct{ field, v string }{{"subject", in.Subject}, {"text", in.Text}, {"html", in.HTML}} {
			if f.v != "" {
				return invalid(f.field, "must not be combined with template_id")
			}
		}
		if in.TemplateVersion < 0 {
			return invalid("template_version", `must be a positive integer or "published"`)
		}
		return nil
	}
	switch n := utf8.RuneCountInString(in.Subject); {
	case n == 0:
		return invalid("subject", "is required")
	case n > MaxSubjectLen:
		return invalid("subject", "must be at most 255 characters")
	case strings.ContainsAny(in.Subject, headerForbidCh):
		return invalid("subject", "must not contain line breaks or NUL")
	}
	if in.Text == "" && in.HTML == "" {
		return invalid("text", "text or html is required")
	}
	for _, b := range []struct{ field, v string }{{"text", in.Text}, {"html", in.HTML}} {
		if len(b.v) > MaxBodyBytes {
			return invalid(b.field, "must be at most 512 KiB")
		}
		if strings.ContainsRune(b.v, 0) {
			return invalid(b.field, "must not contain NUL")
		}
	}
	return nil
}

// ValidateAddress checks that v is exactly one bare address, the same rule
// as for the from/to fields of a send request.
func ValidateAddress(v string) error { return validateAddress("email", v) }

func printableASCII(v string) bool {
	for i := range len(v) {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

// validateAddress accepts exactly one bare address (no display name, no
// surrounding whitespace, no list).
func validateAddress(field, v string) error {
	switch {
	case v == "":
		return invalid(field, "is required")
	case len(v) > MaxAddressLen:
		return invalid(field, "must be at most 254 characters")
	case strings.ContainsAny(v, headerForbidCh):
		return invalid(field, "must not contain line breaks or NUL")
	case !printableASCII(v):
		// One mailbox must have one spelling, or suppressions could be
		// bypassed (Unicode vs. punycode domains, normalisation forms).
		// Internationalised domains are accepted in punycode (xn--...).
		return invalid(field, "must contain only ASCII characters (use punycode for international domains)")
	}
	a, err := mail.ParseAddress(v)
	if err != nil || a.Name != "" || a.Address != v {
		return invalid(field, "must be a single plain email address like name@example.com")
	}
	return nil
}
