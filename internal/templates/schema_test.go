package templates

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseSchemaCanonical(t *testing.T) {
	s, err := ParseSchema([]byte(`{"plan":{"type":"string"},"name":{"type":"string","required":true,"max_length":100},
		"count":{"type":"number"},"vip":{"type":"boolean","required":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"count":{"type":"number","required":false},"name":{"type":"string","required":true,"max_length":100},` +
		`"plan":{"type":"string","required":false,"max_length":1000},"vip":{"type":"boolean","required":false}}`
	if got := string(s.Canonical()); got != want {
		t.Fatalf("canonical\n got %s\nwant %s", got, want)
	}
	// Re-parsing the canonical form is stable.
	again, err := ParseSchema(s.Canonical())
	if err != nil || string(again.Canonical()) != want {
		t.Fatalf("re-parse: %v %s", err, again.Canonical())
	}
	for _, empty := range []string{``, `{}`, `null`} {
		s, err := ParseSchema([]byte(empty))
		if err != nil || len(s) != 0 || string(s.Canonical()) != `{}` {
			t.Fatalf("%q: %v %v", empty, s, err)
		}
	}
}

func TestParseSchemaRejects(t *testing.T) {
	many := make([]string, 0, 51)
	for i := range 51 {
		many = append(many, fmt.Sprintf(`"v%d":{"type":"string"}`, i))
	}
	for _, tc := range []struct {
		raw, field string
	}{
		{`[]`, "variables"},
		{`"x"`, "variables"},
		{`{"Name":{"type":"string"}}`, "variables"},
		{`{"1abc":{"type":"string"}}`, "variables"},
		{`{"` + strings.Repeat("a", 41) + `":{"type":"string"}}`, "variables"},
		{`{"name":{"type":"date"}}`, "variables.name"},
		{`{"name":{}}`, "variables.name"},
		{`{"name":null}`, "variables.name"},
		{`{"name":{"type":"string","extra":1}}`, "variables.name"},
		{`{"name":{"type":"string","max_length":0}}`, "variables.name"},
		{`{"name":{"type":"string","max_length":10001}}`, "variables.name"},
		{`{"name":{"type":"number","max_length":10}}`, "variables.name"},
		{`{"name":{"type":"string","required":"yes"}}`, "variables.name"},
		{`{"name":{"type":"string"}} {}`, "variables"},
		{`{` + strings.Join(many, ",") + `}`, "variables"},
	} {
		_, err := ParseSchema([]byte(tc.raw))
		var te *Error
		if !errors.As(err, &te) || te.Code != CodeValidation || te.Field != tc.field {
			t.Errorf("%s: err %v, want validation error on %s", tc.raw, err, tc.field)
		}
	}
}
