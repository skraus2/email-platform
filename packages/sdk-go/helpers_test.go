package emailplatform

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestTemplateVersions(t *testing.T) {
	v, err := TemplateVersionNumber(3)
	if err != nil || string(v) != "3" {
		t.Fatalf("%s, %v", v, err)
	}
	if _, err := TemplateVersionNumber(0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("0: %v", err)
	}
	if p := TemplateVersionPublished(); string(p) != `"published"` {
		t.Fatalf("published = %s", p)
	}
	cases := map[string]struct {
		n         int
		published bool
		ok        bool
	}{`7`: {7, false, true}, `"published"`: {0, true, true}, "\t\"published\"\n": {0, true, true}, `0`: {}, `-1`: {}, `"7"`: {}, `1.5`: {}, `null`: {}, ``: {}}
	for raw, want := range cases {
		n, published, err := ParseTemplateVersion(json.RawMessage(raw))
		if n != want.n || published != want.published || (err == nil) != want.ok {
			t.Errorf("%q → %d, %v, %v", raw, n, published, err)
		}
	}
}

func TestAutomationSteps(t *testing.T) {
	version, _ := TemplateVersionNumber(2)
	value := any("pro")
	steps := []any{
		WaitStep{Seconds: 3600},
		SendStep{From: "app@example.com", TemplateId: "t1", TemplateVersion: version, UnsubscribeUrl: "https://app.example.com/u/{token}"},
		AttributeConditionStep{Attribute: "plan", Op: "eq", Value: &value},
		EventConditionStep{Event: "user.upgraded", Op: "occurred"},
	}
	wantTypes := []string{"wait", "send", "condition", "condition"}
	for i, step := range steps {
		var encoded AutomationStep
		var err error
		switch s := step.(type) {
		case WaitStep:
			encoded, err = NewAutomationStep(s)
		case SendStep:
			encoded, err = NewAutomationStep(s)
		case AttributeConditionStep:
			encoded, err = NewAutomationStep(s)
		case EventConditionStep:
			encoded, err = NewAutomationStep(s)
		}
		if err != nil {
			t.Fatal(err)
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(encoded, &probe); err != nil || probe.Type != wantTypes[i] {
			t.Fatalf("%T encoded as %s", step, encoded)
		}
		decoded, err := DecodeAutomationStep(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.TypeOf(decoded) != reflect.TypeOf(step) {
			t.Fatalf("decoded %T, want %T", decoded, step)
		}
		again, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(encoded) {
			t.Fatalf("round trip %s != %s", again, encoded)
		}
	}
	// Contract example shape.
	decoded, err := DecodeAutomationStep(AutomationStep(`{"type":"condition","event":"user.upgraded","op":"occurred"}`))
	if e, ok := decoded.(EventConditionStep); err != nil || !ok || e.Event != "user.upgraded" {
		t.Fatalf("%#v, %v", decoded, err)
	}
	for _, bad := range []string{`{}`, `{"type":"jump"}`, `{"type":"condition"}`, `{"type":"condition","attribute":"a","event":"e"}`, `[]`, `x`, `{"type":"wait","seconds":"x"}`} {
		if _, err := DecodeAutomationStep(AutomationStep(bad)); err == nil {
			t.Errorf("%s decoded", bad)
		}
	}
	if _, err := NewAutomationStep(SendStep{TemplateVersion: json.RawMessage("{")}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid raw: %v", err)
	}
}
