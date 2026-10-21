package templates

import (
	"errors"
	"testing"
)

func TestPrintsTopLevel(t *testing.T) {
	schema, err := ParseSchema([]byte(`{"unsubscribe_url":{"type":"string","required":true},"vip":{"type":"boolean"},"name":{"type":"string"}}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		src       Source
		text, htm bool
	}{
		{Source{Subject: "s", Text: "Bye: {{.unsubscribe_url}}", HTML: `<a href="{{.unsubscribe_url}}">x</a>`}, true, true},
		{Source{Subject: "s", Text: "{{if .vip}}{{.unsubscribe_url}}{{end}}", HTML: `<p>{{.name}}</p>`}, false, false},
		{Source{Subject: "s", Text: "{{.name}}{{/* {{.unsubscribe_url}} */}}"}, false, false},
		{Source{Subject: "{{.unsubscribe_url}}", Text: "x"}, false, false},
	}
	for i, c := range cases {
		comp, err := Compile(c.src, schema)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got := comp.PrintsTopLevel("text", "unsubscribe_url"); got != c.text {
			t.Errorf("%d text: %v", i, got)
		}
		if got := comp.PrintsTopLevel("html", "unsubscribe_url"); got != c.htm {
			t.Errorf("%d html: %v", i, got)
		}
		if comp.HasHTML() != (c.src.HTML != "") {
			t.Errorf("%d HasHTML", i)
		}
	}
}

func TestValidateSharedVariables(t *testing.T) {
	schema, err := ParseSchema([]byte(`{"name":{"type":"string","required":true,"max_length":5},"n":{"type":"number"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{``, `{}`, `{"n":2}`, `{"name":"Ada"}`} {
		if err := ValidateSharedVariables(schema, []byte(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{`{"name":1}`, `{"name":"toolong"}`, `{"x":1}`, `[]`, `{"n":"1"}`} {
		var e *Error
		if err := ValidateSharedVariables(schema, []byte(bad)); !errors.As(err, &e) || e.Code != CodeVariablesInvalid {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestMergeVariables(t *testing.T) {
	schema, err := ParseSchema([]byte(`{"unsubscribe_url":{"type":"string","required":true},"name":{"type":"string","required":true},"plan":{"type":"string"},"n":{"type":"number"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := MergeVariables(schema, []byte(`{"plan":"basic","name":"Default"}`), []byte(`{"name":"Ada","n":2.50,"other":"x","unsubscribe_url":"evil"}`),
		map[string]string{"unsubscribe_url": "https://a.test/u?t=tok"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"n":2.50,"name":"Ada","plan":"basic","unsubscribe_url":"https://a.test/u?t=tok"}` {
		t.Fatalf("merged %s", got)
	}
	src := Source{Subject: "Hi {{.name}}", Text: "{{.plan}} {{.n}} {{.unsubscribe_url}}"}
	c, err := Compile(src, schema)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Render(got)
	if err != nil || r.Subject != "Hi Ada" || r.Text != "basic 2.50 https://a.test/u?t=tok" {
		t.Fatalf("render: %+v %v", r, err)
	}
	// A contact without the required name and no shared default fails.
	vars, err := MergeVariables(schema, nil, []byte(`{}`), map[string]string{"unsubscribe_url": "u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Render(vars); err == nil {
		t.Fatal("missing required variable rendered")
	}
	for _, bad := range [][2]string{{`[1]`, `{}`}, {`{}`, `nope`}} {
		if _, err := MergeVariables(schema, []byte(bad[0]), []byte(bad[1]), nil); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
