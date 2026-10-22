package automations

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const sendStep = `{"type":"send","from":"news@example.test","template_id":"0192f0c4-7a4e-7c3a-9b1e-2f3a4b5c6d7e","template_version":"published","unsubscribe_url":"https://app.example.test/u?t={token}"}`

func TestParseStepsAccepts(t *testing.T) {
	raw := `[
		{"type":"wait","seconds":60},
		{"type":"condition","attribute":"plan","op":"eq","value":"pro"},
		{"type":"condition","attribute":"age","op":"neq","value":30},
		{"type":"condition","attribute":"vip","op":"exists"},
		{"type":"condition","event":"order.completed","op":"not_occurred"},
		{"type":"wait","seconds":2592000},
		` + sendStep + `
	]`
	steps, err := ParseSteps(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseSteps: %v", err)
	}
	if len(steps) != 7 || steps[0].Seconds != 60 || steps[4].Event != "order.completed" || steps[6].TemplateVersion != 0 {
		t.Fatalf("unexpected steps: %+v", steps)
	}
	stored, err := EncodeSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeSteps(stored)
	if err != nil {
		t.Fatal(err)
	}
	again, err := EncodeSteps(back)
	if err != nil || string(again) != string(stored) {
		t.Fatalf("stored form does not round-trip:\n%s\n%s", stored, again)
	}
	if !strings.Contains(string(stored), `"variables":{}`) || strings.Contains(string(stored), `"seconds":0`) {
		t.Fatalf("stored form has wrong fields: %s", stored)
	}
}

func TestParseStepsRefuses(t *testing.T) {
	cases := map[string]struct {
		raw   string
		field string
	}{
		"not an array":         {`{}`, "steps"},
		"empty":                {`[]`, "steps"},
		"no send":              {`[{"type":"wait","seconds":60}]`, "steps"},
		"too many":             {`[` + strings.Repeat(sendStep+",", 20) + sendStep + `]`, "steps"},
		"unknown type":         {`[{"type":"goto"},` + sendStep + `]`, "steps[0].type"},
		"step not object":      {`[1,` + sendStep + `]`, "steps[0]"},
		"wait too short":       {`[{"type":"wait","seconds":59},` + sendStep + `]`, "steps[0].seconds"},
		"wait too long":        {`[{"type":"wait","seconds":2592001},` + sendStep + `]`, "steps[0].seconds"},
		"wait fraction":        {`[{"type":"wait","seconds":60.5},` + sendStep + `]`, "steps[0].seconds"},
		"wait extra field":     {`[{"type":"wait","seconds":60,"op":"eq"},` + sendStep + `]`, "steps[0].op"},
		"condition both":       {`[{"type":"condition","attribute":"a","event":"e","op":"eq"},` + sendStep + `]`, "steps[0]"},
		"condition neither":    {`[{"type":"condition","op":"eq"},` + sendStep + `]`, "steps[0]"},
		"bad attribute":        {`[{"type":"condition","attribute":"Plan","op":"exists"},` + sendStep + `]`, "steps[0].attribute"},
		"bad attribute op":     {`[{"type":"condition","attribute":"a","op":"occurred"},` + sendStep + `]`, "steps[0].op"},
		"eq without value":     {`[{"type":"condition","attribute":"a","op":"eq"},` + sendStep + `]`, "steps[0].value"},
		"exists with value":    {`[{"type":"condition","attribute":"a","op":"exists","value":1},` + sendStep + `]`, "steps[0].value"},
		"object value":         {`[{"type":"condition","attribute":"a","op":"eq","value":{}},` + sendStep + `]`, "steps[0].value"},
		"null value":           {`[{"type":"condition","attribute":"a","op":"eq","value":null},` + sendStep + `]`, "steps[0].value"},
		"long value":           {`[{"type":"condition","attribute":"a","op":"eq","value":"` + strings.Repeat("x", 1001) + `"},` + sendStep + `]`, "steps[0].value"},
		"bad event":            {`[{"type":"condition","event":"Order","op":"occurred"},` + sendStep + `]`, "steps[0].event"},
		"bad event op":         {`[{"type":"condition","event":"e","op":"eq"},` + sendStep + `]`, "steps[0].op"},
		"event with value":     {`[{"type":"condition","event":"e","op":"occurred","value":1},` + sendStep + `]`, "steps[0].value"},
		"send bad template id": {`[{"type":"send","from":"a@b.test","template_id":"x","template_version":1,"unsubscribe_url":"u"}]`, "steps[0].template_id"},
		"send bad version":     {`[{"type":"send","from":"a@b.test","template_id":"0192f0c4-7a4e-7c3a-9b1e-2f3a4b5c6d7e","template_version":0,"unsubscribe_url":"u"}]`, "steps[0].template_version"},
		"send no from":         {`[{"type":"send","template_id":"0192f0c4-7a4e-7c3a-9b1e-2f3a4b5c6d7e","template_version":1,"unsubscribe_url":"u"}]`, "steps[0].from"},
		"send extra field":     {`[{"type":"send","from":"a@b.test","template_id":"0192f0c4-7a4e-7c3a-9b1e-2f3a4b5c6d7e","template_version":1,"unsubscribe_url":"u","to":"x@y.test"}]`, "steps[0].to"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSteps(json.RawMessage(tc.raw))
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("want *Error, got %v", err)
			}
			if e.Field != tc.field {
				t.Fatalf("field = %q, want %q (%v)", e.Field, tc.field, e)
			}
			if strings.Contains(e.Reason, "xxxxxxxx") {
				t.Fatalf("reason repeats the submitted value: %v", e)
			}
		})
	}
}

func TestEventNames(t *testing.T) {
	for _, ok := range []string{"a", "user.signed_up", "order:paid", "0-x", strings.Repeat("a", 100)} {
		if !ValidEventName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "User", ".x", "a b", "ä", strings.Repeat("a", 101), "a/b"} {
		if ValidEventName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMatchAttribute(t *testing.T) {
	attrs := []byte(`{"plan":"pro","age":30,"ratio":1.50,"vip":true,"one":"1"}`)
	cases := []struct {
		attr, op, value string
		want            bool
	}{
		{"plan", OpEq, `"pro"`, true},
		{"plan", OpEq, `"Pro"`, false},
		{"plan", OpNeq, `"free"`, true},
		{"age", OpEq, `30`, true},
		{"age", OpEq, `30.0`, true},
		{"age", OpEq, `3e1`, true},
		{"age", OpEq, `"30"`, false},
		{"ratio", OpEq, `1.5`, true},
		{"vip", OpEq, `true`, true},
		{"vip", OpEq, `"true"`, false},
		{"one", OpEq, `1`, false},
		{"missing", OpEq, `"x"`, false},
		{"missing", OpNeq, `"x"`, true},
		{"vip", OpExists, ``, true},
		{"missing", OpExists, ``, false},
		{"missing", OpNotExists, ``, true},
		{"plan", OpNotExists, ``, false},
	}
	for _, tc := range cases {
		s := Step{Type: StepCondition, Attribute: tc.attr, Op: tc.op, Value: json.RawMessage(tc.value)}
		got, err := matchAttribute(s, attrs)
		if err != nil || got != tc.want {
			t.Errorf("%s %s %s = %v, %v; want %v", tc.attr, tc.op, tc.value, got, err, tc.want)
		}
	}
	if got, err := matchAttribute(Step{Attribute: "a", Op: OpNotExists}, []byte(`{}`)); err != nil || !got {
		t.Errorf("empty attributes: %v %v", got, err)
	}
}
