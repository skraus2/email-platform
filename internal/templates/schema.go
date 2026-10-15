// Package templates stores email templates with immutable versions and
// renders them: subject and text with text/template, HTML with html/template
// (contextual escaping), restricted to a small, reviewed language subset and
// typed variables.
package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
)

// Error codes of template errors, as the API reports them.
const (
	CodeValidation       = "validation_failed"
	CodeTemplateInvalid  = "template_invalid"
	CodeVariablesInvalid = "variables_invalid"
	CodeRenderFailed     = "render_failed"
)

// Error is a client-facing template error. Field names the template part or
// variable; Reason never repeats a submitted variable value.
type Error struct {
	Code   string
	Field  string
	Reason string
}

func (e *Error) Error() string { return e.Field + ": " + e.Reason }

func fail(code, field, format string, args ...any) error {
	return &Error{Code: code, Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Variable types.
const (
	TypeString  = "string"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
)

// Schema limits.
const (
	MaxVariables         = 50
	DefaultMaxLength     = 1000
	MaxMaxLength         = 10000
	maxNumberLiteralSize = 100
)

var variableName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Var declares one template variable.
type Var struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
	// MaxLength bounds string values in characters (0 = DefaultMaxLength).
	MaxLength int `json:"max_length,omitempty"`
}

func (v Var) maxLength() int {
	if v.MaxLength == 0 {
		return DefaultMaxLength
	}
	return v.MaxLength
}

// Schema declares the variables of a template version by name.
type Schema map[string]Var

// ParseSchema decodes and validates a variable schema. Empty input and JSON
// null mean "no variables".
func ParseSchema(raw []byte) (Schema, error) {
	bad := func(field, format string, args ...any) error { return fail(CodeValidation, field, format, args...) }
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return Schema{}, nil
	}
	s := Schema{}
	err := decodeObject(raw, func(name string, dec *json.Decoder) error {
		if !variableName.MatchString(name) {
			return bad("variables", "variable names must match ^[a-z][a-z0-9_]{0,39}$")
		}
		if _, dup := s[name]; dup {
			return bad("variables", "variable names must be unique")
		}
		if len(s) == MaxVariables {
			return bad("variables", "at most %d variables are allowed", MaxVariables)
		}
		field := "variables." + name
		var rawVar json.RawMessage
		if err := dec.Decode(&rawVar); err != nil {
			return bad(field, "must be an object")
		}
		// Walked key by key with exact, lowercase keys: encoding/json would
		// match keys case-insensitively and keep the last of duplicates.
		shape := `must be an object with "type", optional "required" (boolean) and "max_length" (integer)`
		var in struct {
			Type      string
			Required  *bool
			MaxLength *int
		}
		seen := map[string]bool{}
		err := decodeObject(rawVar, func(key string, vd *json.Decoder) error {
			if seen[key] {
				return bad(field, "%s", shape)
			}
			seen[key] = true
			var dst any
			switch key {
			case "type":
				dst = &in.Type
			case "required":
				dst = &in.Required
			case "max_length":
				dst = &in.MaxLength
			default:
				return bad(field, "%s", shape)
			}
			var val json.RawMessage
			if err := vd.Decode(&val); err != nil || string(val) == "null" {
				return bad(field, "%s", shape)
			}
			if err := json.Unmarshal(val, dst); err != nil {
				return bad(field, "%s", shape)
			}
			return nil
		})
		if err != nil {
			var te *Error
			if errors.As(err, &te) {
				return err
			}
			return bad(field, "%s", shape)
		}
		v := Var{Type: in.Type, Required: in.Required != nil && *in.Required}
		switch v.Type {
		case TypeString:
			v.MaxLength = DefaultMaxLength
			if in.MaxLength != nil {
				v.MaxLength = *in.MaxLength
			}
			if v.MaxLength < 1 || v.MaxLength > MaxMaxLength {
				return bad(field, "max_length must be between 1 and %d", MaxMaxLength)
			}
		case TypeNumber, TypeBoolean:
			if in.MaxLength != nil {
				return bad(field, "max_length applies to strings only")
			}
		default:
			return bad(field, `type must be "string", "number" or "boolean"`)
		}
		s[name] = v
		return nil
	})
	if err != nil {
		var te *Error
		if errors.As(err, &te) {
			return nil, err
		}
		return nil, bad("variables", "must be a JSON object of variable declarations")
	}
	return s, nil
}

// Canonical is the schema as stored: sorted names, defaults filled in.
func (s Schema) Canonical() []byte {
	names := make([]string, 0, len(s))
	for n := range s {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		v := s[n]
		if v.Type == TypeString {
			v.MaxLength = v.maxLength()
		}
		k, _ := json.Marshal(n)
		val, _ := json.Marshal(v)
		b.Write(k)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// decodeObject walks one JSON object key by key, so duplicate keys are seen
// (encoding/json would silently keep the last one), and refuses trailing
// data. each must consume the value.
func decodeObject(raw []byte, each func(key string, dec *json.Decoder) error) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("not an object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("bad key")
		}
		if err := each(key, dec); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}
