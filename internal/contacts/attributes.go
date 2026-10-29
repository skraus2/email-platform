package contacts

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/skraus2/email-platform/internal/templates"
)

// Attribute limits.
const (
	MaxAttributes          = 20
	MaxAttributeLength     = 1000 // characters of a string value
	MaxAttributesSize      = 8 << 10
	MaxConsentSourceLength = 100
	maxNumberLiteral       = maxNumberDigits + 2 // sign and decimal point of a written-out value
	// maxNumberDigits bounds a number written out without exponent: the
	// database stores numbers exactly and prints them that way, so 1e-800
	// would take 800 bytes.
	maxNumberDigits = 64
)

// ReservedAttribute is filled in by the platform when a broadcast renders.
const ReservedAttribute = templates.UnsubscribeVariable

// TokenPlaceholder in a broadcast's unsubscribe URL is replaced by the
// contact's unsubscribe token.
const TokenPlaceholder = templates.TokenPlaceholder

// UnsubscribeURL puts a contact's token into an unsubscribe URL template.
func UnsubscribeURL(template, token string) string { return templates.UnsubscribeURL(template, token) }

var attributeName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Error is a client-facing validation error. Field names the input field or
// attribute; Reason never repeats a submitted value.
type Error struct {
	Field  string
	Reason string
}

func (e *Error) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, format string, args ...any) error {
	return &Error{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// ValidateAttributes checks raw attributes (a JSON object; empty means none)
// and returns them as compact JSON with sorted names. Errors are *Error.
func ValidateAttributes(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte(`{}`), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, invalid("attributes", "must be a JSON object")
	}
	values := make(map[string]any)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, invalid("attributes", "must be a JSON object")
		}
		name, _ := tok.(string)
		switch {
		case !attributeName.MatchString(name):
			return nil, invalid("attributes", "names must match ^[a-z][a-z0-9_]{0,39}$")
		case name == ReservedAttribute:
			return nil, invalid("attributes."+name, "is reserved")
		}
		if _, dup := values[name]; dup {
			return nil, invalid("attributes", "names must be unique")
		}
		field := "attributes." + name
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, invalid("attributes", "must be a JSON object")
		}
		switch v := v.(type) {
		case string:
			if utf8.RuneCountInString(v) > MaxAttributeLength {
				return nil, invalid(field, "must be at most %d characters", MaxAttributeLength)
			}
			if strings.ContainsRune(v, 0) {
				return nil, invalid(field, "must not contain NUL")
			}
		case json.Number:
			if len(v) > maxNumberLiteral || writtenOutDigits(string(v)) > maxNumberDigits {
				return nil, invalid(field, "must be a number of at most %d digits written out", maxNumberDigits)
			}
			if _, err := strconv.ParseFloat(string(v), 64); err != nil {
				return nil, invalid(field, "must be a finite number")
			}
		case bool:
		default:
			return nil, invalid(field, "must be a string, number or boolean")
		}
		values[name] = v
		if len(values) > MaxAttributes {
			return nil, invalid("attributes", "must have at most %d entries", MaxAttributes)
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, invalid("attributes", "must be a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("attributes", "must be a single JSON object")
	}
	out, err := canonical(values)
	if err != nil {
		return nil, invalid("attributes", "must be a JSON object")
	}
	if len(out) > MaxAttributesSize {
		return nil, invalid("attributes", "must be at most %d bytes as compact JSON", MaxAttributesSize)
	}
	return out, nil
}

// writtenOutDigits is an upper bound of the digits of a JSON number literal
// written out without exponent: its mantissa digits plus the exponent's
// magnitude.
func writtenOutDigits(n string) int {
	mantissa, exp, hasExp := strings.Cut(strings.ToLower(n), "e")
	digits := len(strings.TrimPrefix(strings.Replace(mantissa, ".", "", 1), "-"))
	if !hasExp {
		return digits
	}
	e, err := strconv.Atoi(strings.TrimPrefix(exp, "+"))
	if err != nil || e > maxNumberDigits || e < -maxNumberDigits {
		return maxNumberDigits + 1
	}
	return digits + max(e, -e)
}

// canonical encodes values as compact JSON, names sorted, numbers in their
// submitted spelling and without HTML escaping.
func canonical(values map[string]any) ([]byte, error) {
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	slices.Sort(names)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := enc.Encode(n); err != nil {
			return nil, fmt.Errorf("encode attribute name: %w", err)
		}
		b.Truncate(b.Len() - 1) // Encode appends a newline
		b.WriteByte(':')
		if err := enc.Encode(values[n]); err != nil {
			return nil, fmt.Errorf("encode attribute value: %w", err)
		}
		b.Truncate(b.Len() - 1)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// fromStored turns stored jsonb text (keys ordered by length, spaces after
// separators) back into the canonical form.
func fromStored(stored []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(stored))
	dec.UseNumber()
	var values map[string]any
	if err := dec.Decode(&values); err != nil {
		return nil, fmt.Errorf("decode stored attributes: %w", err)
	}
	return canonical(values)
}

func validateConsentSource(v string) error {
	switch {
	case v == "":
		return invalid("consent_source", "must not be empty")
	case !utf8.ValidString(v):
		return invalid("consent_source", "must be valid UTF-8")
	case utf8.RuneCountInString(v) > MaxConsentSourceLength:
		return invalid("consent_source", "must be at most %d characters", MaxConsentSourceLength)
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Zl, unicode.Zp) {
			return invalid("consent_source", "must not contain control or text-direction characters")
		}
	}
	return nil
}

// tokenBytes is the entropy of an unsubscribe token (256 bits).
const tokenBytes = 32

// newToken returns a random unsubscribe token, base64url without padding.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate unsubscribe token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validToken reports whether s has the shape of an unsubscribe token.
func validToken(s string) bool {
	if len(s) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == tokenBytes
}
