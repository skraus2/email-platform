package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Output limits.
const (
	MaxBodyOutput    = 512 << 10 // bytes, text and HTML each
	MaxSubjectOutput = 255       // characters
	maxSubjectBytes  = 4 << 10
)

// Rendered is the content of one email.
type Rendered struct {
	Subject string
	Text    string
	HTML    string // empty without an HTML part
}

// Render validates raw variables (a JSON object; nil means none) against the
// schema and renders all parts. Errors are *Error with code
// variables_invalid or render_failed; they name variables, never values.
func (c *Compiled) Render(raw []byte) (Rendered, error) {
	data, err := variables(c.schema, raw)
	if err != nil {
		return Rendered{}, err
	}
	var r Rendered
	var subject bytes.Buffer
	if err := c.subject.Execute(&limitWriter{buf: &subject, max: maxSubjectBytes}, data); err != nil {
		return Rendered{}, renderError("subject", err)
	}
	r.Subject = subject.String()
	switch n := utf8.RuneCountInString(r.Subject); {
	case n == 0:
		return Rendered{}, fail(CodeRenderFailed, "subject", "rendered subject is empty")
	case n > MaxSubjectOutput:
		return Rendered{}, fail(CodeRenderFailed, "subject", "rendered subject is longer than %d characters", MaxSubjectOutput)
	case strings.ContainsAny(r.Subject, "\r\n\x00"):
		return Rendered{}, fail(CodeRenderFailed, "subject", "rendered subject must not contain line breaks")
	}
	var text bytes.Buffer
	if err := c.text.Execute(&limitWriter{buf: &text, max: MaxBodyOutput}, data); err != nil {
		return Rendered{}, renderError("text", err)
	}
	r.Text = text.String()
	if c.html != nil {
		var html bytes.Buffer
		if err := c.html.Execute(&limitWriter{buf: &html, max: MaxBodyOutput}, data); err != nil {
			return Rendered{}, renderError("html", err)
		}
		r.HTML = html.String()
	}
	if r.Text == "" && r.HTML == "" {
		return Rendered{}, fail(CodeRenderFailed, "text", "rendered text and html are both empty")
	}
	return r, nil
}

func renderError(part string, err error) error {
	if errors.Is(err, errTooLarge) {
		if part == "subject" {
			return fail(CodeRenderFailed, part, "rendered subject is longer than %d characters", MaxSubjectOutput)
		}
		return fail(CodeRenderFailed, part, "rendered %s is larger than %d KiB", part, MaxBodyOutput>>10)
	}
	// Validation leaves no runtime error paths; this is a safety net whose
	// message carries no variable values (template errors name fields).
	return fail(CodeRenderFailed, part, "could not be rendered")
}

// zeroData is the data with every declared variable at its zero value.
func (c *Compiled) zeroData() map[string]any { return zeroData(c.schema) }

func zeroData(schema Schema) map[string]any {
	data := make(map[string]any, len(schema))
	for name, v := range schema {
		data[name] = zeroValue(v.Type)
	}
	return data
}

func zeroValue(typ string) any {
	switch typ {
	case TypeNumber:
		return json.Number("0")
	case TypeBoolean:
		return false
	default:
		return ""
	}
}

// ValidateVariables checks raw variables against a schema without compiling
// the template, so invalid requests cost no template work. Errors are *Error
// (variables_invalid).
func ValidateVariables(schema Schema, raw []byte) error {
	_, err := variables(schema, raw)
	return err
}

// ValidateSharedVariables checks values that cover only part of a schema
// (the rest comes from elsewhere at send time): declared names and types,
// without requiring any variable. Errors are *Error (variables_invalid).
func ValidateSharedVariables(schema Schema, raw []byte) error {
	_, err := decodeVariables(schema, raw, false)
	return err
}

// variables decodes and checks the request variables.
func variables(schema Schema, raw []byte) (map[string]any, error) {
	return decodeVariables(schema, raw, true)
}

// decodeVariables decodes and checks variables; requireAll also demands
// every required variable.
func decodeVariables(schema Schema, raw []byte, requireAll bool) (map[string]any, error) {
	bad := func(field, format string, args ...any) error {
		return fail(CodeVariablesInvalid, field, format, args...)
	}
	data := zeroData(schema)
	seen := make(map[string]bool, len(schema))
	if len(bytes.TrimSpace(raw)) > 0 {
		err := decodeObject(raw, func(name string, dec *json.Decoder) error {
			v, declared := schema[name]
			if !declared {
				return bad("variables", "a variable is not declared by this template version")
			}
			if seen[name] {
				return bad("variables", "variable names must be unique")
			}
			seen[name] = true
			field := "variables." + name
			var val any
			if err := dec.Decode(&val); err != nil {
				return bad(field, "is not valid JSON")
			}
			switch v.Type {
			case TypeString:
				s, ok := val.(string)
				switch {
				case !ok:
					return bad(field, "must be a string")
				case utf8.RuneCountInString(s) > v.maxLength():
					return bad(field, "must be at most %d characters", v.maxLength())
				case strings.ContainsRune(s, 0):
					return bad(field, "must not contain NUL")
				}
			case TypeNumber:
				n, ok := val.(json.Number)
				if !ok || len(n) > maxNumberLiteralSize {
					return bad(field, "must be a number")
				}
				if _, err := strconv.ParseFloat(string(n), 64); err != nil {
					return bad(field, "must be a finite number")
				}
			case TypeBoolean:
				if _, ok := val.(bool); !ok {
					return bad(field, "must be true or false")
				}
			}
			data[name] = val
			return nil
		})
		if err != nil {
			var te *Error
			if errors.As(err, &te) {
				return nil, err
			}
			return nil, bad("variables", "must be a JSON object")
		}
	}
	for name, v := range schema {
		if requireAll && v.Required && !seen[name] {
			return nil, bad("variables."+name, "is required")
		}
	}
	return data, nil
}

// limitWriter fails once more than max bytes were written.
type limitWriter struct {
	buf *bytes.Buffer
	max int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.buf.Len()+len(p) > l.max {
		return 0, errTooLarge
	}
	return l.buf.Write(p)
}
