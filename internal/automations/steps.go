// Package automations runs per-contact flows: an app reports contact events,
// every active automation whose trigger is that event name starts a run for
// the contact, and the run executes the automation's fixed list of steps
// (wait, condition, send) in order. Run state lives in automation_runs; each
// due step is one job, so a run survives worker restarts while it waits.
package automations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"slices"
	"strconv"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/contacts"
)

// Step types.
const (
	StepWait      = "wait"
	StepCondition = "condition"
	StepSend      = "send"
)

// Condition operators.
const (
	OpEq          = "eq"
	OpNeq         = "neq"
	OpExists      = "exists"
	OpNotExists   = "not_exists"
	OpOccurred    = "occurred"
	OpNotOccurred = "not_occurred"
)

// Definition limits.
const (
	MaxSteps          = 20
	MinWaitSeconds    = 60
	MaxWaitSeconds    = 30 * 24 * 60 * 60
	MaxDefinitionSize = 64 << 10
	MaxNameLength     = 200
)

var eventName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,99}$`)

// ValidEventName reports whether s is a valid event name.
func ValidEventName(s string) bool { return eventName.MatchString(s) }

var attributeName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Error is a client-facing validation error; Reason never repeats a
// submitted value.
type Error struct {
	Field  string
	Reason string
}

func (e *Error) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, format string, args ...any) error {
	return &Error{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Step is one step of a definition. Which fields are set depends on Type.
type Step struct {
	Type string
	// Wait.
	Seconds int
	// Condition: exactly one of Attribute and Event.
	Attribute string
	Event     string
	Op        string
	// Value is the compact JSON value compared by eq/neq.
	Value json.RawMessage
	// Send. TemplateVersion 0 means "published" until resolved at create.
	From            string
	TemplateID      uuid.UUID
	TemplateVersion int
	// Variables is compact JSON (an object, "{}" when none).
	Variables      json.RawMessage
	UnsubscribeURL string
}

// MarshalJSON writes the step in its stored and returned form: only the
// fields of its type.
func (s Step) MarshalJSON() ([]byte, error) {
	switch s.Type {
	case StepWait:
		return json.Marshal(struct {
			Type    string `json:"type"`
			Seconds int    `json:"seconds"`
		}{s.Type, s.Seconds})
	case StepCondition:
		if s.Event != "" {
			return json.Marshal(struct {
				Type  string `json:"type"`
				Event string `json:"event"`
				Op    string `json:"op"`
			}{s.Type, s.Event, s.Op})
		}
		return json.Marshal(struct {
			Type      string          `json:"type"`
			Attribute string          `json:"attribute"`
			Op        string          `json:"op"`
			Value     json.RawMessage `json:"value,omitempty"`
		}{s.Type, s.Attribute, s.Op, s.Value})
	case StepSend:
		vars := s.Variables
		if len(vars) == 0 {
			vars = json.RawMessage(`{}`)
		}
		return json.Marshal(struct {
			Type            string          `json:"type"`
			From            string          `json:"from"`
			TemplateID      uuid.UUID       `json:"template_id"`
			TemplateVersion int             `json:"template_version"`
			Variables       json.RawMessage `json:"variables"`
			UnsubscribeURL  string          `json:"unsubscribe_url"`
		}{s.Type, s.From, s.TemplateID, s.TemplateVersion, vars, s.UnsubscribeURL})
	}
	return nil, fmt.Errorf("unknown step type %q", s.Type)
}

// stepFields lists the fields each step shape accepts.
var stepFields = map[string][]string{
	StepWait:             {"type", "seconds"},
	StepCondition + ".a": {"type", "attribute", "op", "value"},
	StepCondition + ".e": {"type", "event", "op"},
	StepSend:             {"type", "from", "template_id", "template_version", "variables", "unsubscribe_url"},
}

// ParseSteps decodes and checks the shape of a request's steps (a JSON
// array). Send steps still need their template, sender and variables checked
// against the database (Store.Create does that). Errors are *Error.
func ParseSteps(raw json.RawMessage) ([]Step, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, invalid("steps", "must be an array of step objects")
	}
	if len(items) == 0 || len(items) > MaxSteps {
		return nil, invalid("steps", "must have 1 to %d steps", MaxSteps)
	}
	steps := make([]Step, 0, len(items))
	sends := 0
	for i, item := range items {
		s, err := parseStep(fmt.Sprintf("steps[%d]", i), item)
		if err != nil {
			return nil, err
		}
		if s.Type == StepSend {
			sends++
		}
		steps = append(steps, s)
	}
	if sends == 0 {
		return nil, invalid("steps", "must contain at least one send step")
	}
	return steps, nil
}

func parseStep(field string, raw json.RawMessage) (Step, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return Step{}, invalid(field, "must be an object")
	}
	var typ string
	if err := json.Unmarshal(obj["type"], &typ); err != nil {
		return Step{}, invalid(field+".type", "must be wait, condition or send")
	}
	shape := typ
	switch typ {
	case StepWait, StepSend:
	case StepCondition:
		_, hasAttr := obj["attribute"]
		_, hasEvent := obj["event"]
		switch {
		case hasAttr == hasEvent:
			return Step{}, invalid(field, "a condition needs exactly one of attribute and event")
		case hasAttr:
			shape += ".a"
		default:
			shape += ".e"
		}
	default:
		return Step{}, invalid(field+".type", "must be wait, condition or send")
	}
	for k := range obj {
		if !slices.Contains(stepFields[shape], k) {
			return Step{}, invalid(field+"."+k, "is not a field of a %s step", typ)
		}
	}
	s := Step{Type: typ}
	switch shape {
	case StepWait:
		if err := json.Unmarshal(obj["seconds"], &s.Seconds); err != nil || s.Seconds < MinWaitSeconds || s.Seconds > MaxWaitSeconds {
			return Step{}, invalid(field+".seconds", "must be an integer from %d to %d", MinWaitSeconds, MaxWaitSeconds)
		}
	case StepCondition + ".a":
		if json.Unmarshal(obj["attribute"], &s.Attribute) != nil || !attributeName.MatchString(s.Attribute) {
			return Step{}, invalid(field+".attribute", "must match ^[a-z][a-z0-9_]{0,39}$")
		}
		if json.Unmarshal(obj["op"], &s.Op) != nil || !slices.Contains([]string{OpEq, OpNeq, OpExists, OpNotExists}, s.Op) {
			return Step{}, invalid(field+".op", "must be eq, neq, exists or not_exists")
		}
		v, has := obj["value"]
		switch {
		case (s.Op == OpEq || s.Op == OpNeq) && !has:
			return Step{}, invalid(field+".value", "is required for %s", s.Op)
		case (s.Op == OpExists || s.Op == OpNotExists) && has:
			return Step{}, invalid(field+".value", "is not allowed for %s", s.Op)
		case has:
			value, err := conditionValue(v)
			if err != nil {
				return Step{}, invalid(field+".value", "%s", err.Error())
			}
			s.Value = value
		}
	case StepCondition + ".e":
		if json.Unmarshal(obj["event"], &s.Event) != nil || !ValidEventName(s.Event) {
			return Step{}, invalid(field+".event", "must match ^[a-z0-9][a-z0-9_.:-]{0,99}$")
		}
		if json.Unmarshal(obj["op"], &s.Op) != nil || (s.Op != OpOccurred && s.Op != OpNotOccurred) {
			return Step{}, invalid(field+".op", "must be occurred or not_occurred")
		}
	case StepSend:
		if json.Unmarshal(obj["from"], &s.From) != nil || s.From == "" {
			return Step{}, invalid(field+".from", "must be a single plain ASCII email address")
		}
		var id string
		if json.Unmarshal(obj["template_id"], &id) != nil || len(id) != 36 {
			return Step{}, invalid(field+".template_id", "must be a UUID")
		}
		var err error
		if s.TemplateID, err = uuid.Parse(id); err != nil {
			return Step{}, invalid(field+".template_id", "must be a UUID")
		}
		if s.TemplateVersion, err = parseVersion(obj["template_version"]); err != nil {
			return Step{}, invalid(field+".template_version", `must be a positive integer or "published"`)
		}
		if v, ok := obj["variables"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			s.Variables = v
		}
		if json.Unmarshal(obj["unsubscribe_url"], &s.UnsubscribeURL) != nil {
			return Step{}, invalid(field+".unsubscribe_url", "must be a string")
		}
	}
	return s, nil
}

// parseVersion reads a positive integer or "published" (0).
func parseVersion(raw json.RawMessage) (int, error) {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		if name == "published" {
			return 0, nil
		}
		return 0, errors.New("unknown version name")
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n < 1 || n > 1<<30 {
		return 0, errors.New("not a version")
	}
	return n, nil
}

// conditionValue checks a comparison value with the rules of a contact
// attribute value and returns it as compact JSON.
func conditionValue(raw json.RawMessage) (json.RawMessage, error) {
	wrapped := append(append([]byte(`{"v":`), raw...), '}')
	out, err := contacts.ValidateAttributes(wrapped)
	if err != nil {
		var ce *contacts.Error
		if errors.As(err, &ce) {
			return nil, errors.New(ce.Reason)
		}
		return nil, errors.New("must be a string, number or boolean")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, errors.New("must be a string, number or boolean")
	}
	return m["v"], nil
}

// EncodeSteps returns the stored form of a definition (compact JSON).
func EncodeSteps(steps []Step) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(steps); err != nil {
		return nil, fmt.Errorf("encode steps: %w", err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// DecodeSteps reads a stored definition. It trusts the stored form (written
// by EncodeSteps after validation).
func DecodeSteps(stored []byte) ([]Step, error) {
	var items []struct {
		Type            string          `json:"type"`
		Seconds         int             `json:"seconds"`
		Attribute       string          `json:"attribute"`
		Event           string          `json:"event"`
		Op              string          `json:"op"`
		Value           json.RawMessage `json:"value"`
		From            string          `json:"from"`
		TemplateID      uuid.UUID       `json:"template_id"`
		TemplateVersion int             `json:"template_version"`
		Variables       json.RawMessage `json:"variables"`
		UnsubscribeURL  string          `json:"unsubscribe_url"`
	}
	if err := json.Unmarshal(stored, &items); err != nil {
		return nil, fmt.Errorf("decode stored steps: %w", err)
	}
	steps := make([]Step, 0, len(items))
	for _, it := range items {
		s := Step(it)
		// The database stores jsonb, which comes back reformatted.
		s.Value, s.Variables = compact(s.Value), compact(s.Variables)
		steps = append(steps, s)
	}
	return steps, nil
}

// compact returns raw without insignificant space; null and empty give nil.
func compact(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return raw
	}
	return b.Bytes()
}

// matchAttribute evaluates an attribute condition against a contact's
// attributes (a stored JSON object).
func matchAttribute(s Step, attributes []byte) (bool, error) {
	var attrs map[string]json.RawMessage
	if len(bytes.TrimSpace(attributes)) > 0 {
		if err := json.Unmarshal(attributes, &attrs); err != nil {
			return false, fmt.Errorf("decode contact attributes: %w", err)
		}
	}
	got, ok := attrs[s.Attribute]
	switch s.Op {
	case OpExists:
		return ok, nil
	case OpNotExists:
		return !ok, nil
	case OpEq:
		return ok && sameValue(got, s.Value), nil
	case OpNeq:
		return !ok || !sameValue(got, s.Value), nil
	}
	return false, fmt.Errorf("unknown attribute operator %q", s.Op)
}

// sameValue compares two JSON scalars: same type, numbers by decimal value.
func sameValue(a, b json.RawMessage) bool {
	va, ta := scalar(a)
	vb, tb := scalar(b)
	if ta != tb || ta == "" {
		return false
	}
	if ta == "number" {
		ra, oka := new(big.Rat).SetString(va)
		rb, okb := new(big.Rat).SetString(vb)
		return oka && okb && ra.Cmp(rb) == 0
	}
	return va == vb
}

// scalar returns a JSON scalar's value as text and its type ("" if it is
// not a string, number or boolean).
func scalar(raw json.RawMessage) (string, string) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", ""
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", ""
	}
	switch v := v.(type) {
	case string:
		return v, "string"
	case json.Number:
		return v.String(), "number"
	case bool:
		return strconv.FormatBool(v), "bool"
	}
	return "", ""
}
