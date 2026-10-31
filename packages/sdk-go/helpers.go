package emailplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

// The contract types a few values as a choice the generated types cannot
// express; these helpers build and read them.

var (
	published = json.RawMessage(`"published"`)

	errTemplateVersion = errors.New(`emailplatform: template_version is neither a positive integer nor "published"`)
	errStepType        = errors.New("emailplatform: automation step has no known type")
)

// TemplateVersionNumber is a template_version naming version n (n ≥ 1).
func TemplateVersionNumber(n int) (json.RawMessage, error) {
	if n < 1 {
		return nil, invalid("template version must be a positive integer")
	}
	return json.RawMessage(strconv.Itoa(n)), nil
}

// TemplateVersionPublished is a template_version naming the template's
// published version, resolved when the request is accepted.
func TemplateVersionPublished() json.RawMessage {
	return bytes.Clone(published)
}

// ParseTemplateVersion reads a template_version: a version number, or
// published = true for "published".
func ParseTemplateVersion(raw json.RawMessage) (n int, isPublished bool, err error) {
	if bytes.Equal(bytes.TrimSpace(raw), published) {
		return 0, true, nil
	}
	if err := json.Unmarshal(raw, &n); err != nil || n < 1 {
		return 0, false, errTemplateVersion
	}
	return n, false, nil
}

// Step is one of the four automation step shapes.
type Step interface {
	WaitStep | SendStep | AttributeConditionStep | EventConditionStep
}

// NewAutomationStep sets the step's type and encodes it as an
// AutomationStep for CreateAutomationRequest.Steps.
func NewAutomationStep[S Step](step S) (AutomationStep, error) {
	switch s := any(&step).(type) {
	case *WaitStep:
		s.Type = "wait"
	case *SendStep:
		s.Type = "send"
	case *AttributeConditionStep:
		s.Type = "condition"
	case *EventConditionStep:
		s.Type = "condition"
	}
	data, err := json.Marshal(step)
	if err != nil {
		return nil, invalid("automation step cannot be encoded as JSON")
	}
	return AutomationStep(data), nil
}

// DecodeAutomationStep returns the typed step: a WaitStep, SendStep,
// AttributeConditionStep or EventConditionStep (as a value).
func DecodeAutomationStep(step AutomationStep) (any, error) {
	var probe struct {
		Type      string           `json:"type"`
		Attribute *json.RawMessage `json:"attribute"`
		Event     *json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(step, &probe); err != nil {
		return nil, errStepType
	}
	switch {
	case probe.Type == "wait":
		return decodeStep[WaitStep](step)
	case probe.Type == "send":
		return decodeStep[SendStep](step)
	case probe.Type == "condition" && probe.Attribute != nil && probe.Event == nil:
		return decodeStep[AttributeConditionStep](step)
	case probe.Type == "condition" && probe.Event != nil && probe.Attribute == nil:
		return decodeStep[EventConditionStep](step)
	}
	return nil, errStepType
}

func decodeStep[S Step](step AutomationStep) (any, error) {
	var s S
	if err := json.Unmarshal(step, &s); err != nil {
		return nil, errStepType
	}
	return s, nil
}
