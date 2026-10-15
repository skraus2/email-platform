package templates

import (
	"errors"
	"strings"
	"testing"
)

var testSchema = Schema{
	"name":  {Type: TypeString, Required: true, MaxLength: 100},
	"plan":  {Type: TypeString, MaxLength: 20},
	"vip":   {Type: TypeBoolean},
	"count": {Type: TypeNumber},
	"link":  {Type: TypeString, MaxLength: 200},
}

func TestCompileAccepts(t *testing.T) {
	for _, src := range []Source{
		{Subject: "Hi", Text: "plain"},
		{Subject: "Hi {{.name}}", Text: "Hello {{.name}}, you have {{.count}} items", HTML: "<p>{{.name}}</p>"},
		{Subject: "x", Text: `{{if .vip}}VIP{{else if eq .plan "pro"}}Pro{{else}}Free{{end}}`},
		{Subject: "x", Text: `{{if ne .plan "free"}}paid{{end}}{{if eq .vip true}}!{{end}}{{if .plan}}{{.plan}}{{end}}`},
		{Subject: "x", Text: "{{/* a comment */}}t", HTML: `<a href="{{.link}}">{{.name}}</a>{{- .plan -}}`},
		{Subject: "x", Text: "t", HTML: `<p style="color: red">{{if .vip}}<b>{{.name}}</b>{{end}}</p>`},
	} {
		if _, err := Compile(src, testSchema); err != nil {
			t.Errorf("%+v: %v", src, err)
		}
	}
}

func TestCompileRejects(t *testing.T) {
	for _, tc := range []struct {
		name, part string
		src        Source
	}{
		{"parse error", "text", Source{Subject: "x", Text: "{{.name"}},
		{"undeclared field", "text", Source{Subject: "x", Text: "{{.other}}"}},
		{"undeclared in subject", "subject", Source{Subject: "{{.other}}", Text: "t"}},
		{"undeclared in html", "html", Source{Subject: "x", Text: "t", HTML: "<p>{{.other}}</p>"}},
		{"dot", "text", Source{Subject: "x", Text: "{{.}}"}},
		{"root variable", "text", Source{Subject: "x", Text: "{{$.name}}"}},
		{"variable decl", "text", Source{Subject: "x", Text: "{{$x := .name}}{{$x}}"}},
		{"nested field", "text", Source{Subject: "x", Text: "{{.name.first}}"}},
		{"chain", "text", Source{Subject: "x", Text: "{{(.name).x}}"}},
		{"parenthesised", "text", Source{Subject: "x", Text: "{{(.name)}}"}},
		{"nil", "text", Source{Subject: "x", Text: "{{if nil}}x{{end}}"}},
		{"range", "text", Source{Subject: "x", Text: "{{range .name}}x{{end}}"}},
		{"with", "text", Source{Subject: "x", Text: "{{with .name}}{{.}}{{end}}"}},
		{"else with", "text", Source{Subject: "x", Text: "{{with .name}}a{{else with .plan}}b{{end}}"}},
		{"define", "text", Source{Subject: "x", Text: `{{define "a"}}x{{end}}t`}},
		{"only define", "text", Source{Subject: "x", Text: `{{define "a"}}x{{end}}`}},
		{"block", "text", Source{Subject: "x", Text: `{{block "a" .}}x{{end}}`}},
		{"template call", "text", Source{Subject: "x", Text: `{{template "a"}}`}},
		{"break", "text", Source{Subject: "x", Text: "{{range .name}}{{break}}{{end}}"}},
		{"printf", "text", Source{Subject: "x", Text: `{{printf "%s" .name}}`}},
		{"html func", "html", Source{Subject: "x", Text: "t", HTML: `{{html .name}}`}},
		{"js func", "html", Source{Subject: "x", Text: "t", HTML: `<script>{{js .name}}</script>`}},
		{"call", "text", Source{Subject: "x", Text: `{{call .name}}`}},
		{"index", "text", Source{Subject: "x", Text: `{{index .name 0}}`}},
		{"len", "text", Source{Subject: "x", Text: `{{len .name}}`}},
		{"urlquery", "text", Source{Subject: "x", Text: `{{urlquery .name}}`}},
		{"pipeline", "text", Source{Subject: "x", Text: `{{.name | print}}`}},
		{"lt", "text", Source{Subject: "x", Text: `{{if lt .count 3}}x{{end}}`}},
		{"and", "text", Source{Subject: "x", Text: `{{if and .vip .plan}}x{{end}}`}},
		{"not", "text", Source{Subject: "x", Text: `{{if not .vip}}x{{end}}`}},
		{"number condition", "text", Source{Subject: "x", Text: `{{if .count}}x{{end}}`}},
		{"number eq", "text", Source{Subject: "x", Text: `{{if eq .count 3}}x{{end}}`}},
		{"eq type mismatch", "text", Source{Subject: "x", Text: `{{if eq .plan true}}x{{end}}`}},
		{"eq bool vs string", "text", Source{Subject: "x", Text: `{{if eq .vip "true"}}x{{end}}`}},
		{"eq two fields", "text", Source{Subject: "x", Text: `{{if eq .plan .name}}x{{end}}`}},
		{"eq two constants", "text", Source{Subject: "x", Text: `{{if eq "a" "a"}}x{{end}}`}},
		{"eq three args", "text", Source{Subject: "x", Text: `{{if eq .plan "a" "b"}}x{{end}}`}},
		{"eq as action", "text", Source{Subject: "x", Text: `{{eq .plan "a"}}`}},
		{"constant action", "text", Source{Subject: "x", Text: `{{"x"}}`}},
		{"method call", "text", Source{Subject: "x", Text: `{{.name.Len}}`}},
		{"ambiguous html context", "html", Source{Subject: "x", Text: "t", HTML: `<a href="{{if .vip}}x{{end}}{{.link}}`}},
		{"branches end in different contexts", "html", Source{Subject: "x", Text: "t", HTML: `{{if .vip}}<a href="{{end}}x`}},
		{"missing text", "text", Source{Subject: "x", HTML: "<p>x</p>"}},
		{"empty subject", "subject", Source{Subject: "", Text: "t"}},
		{"subject too long", "subject", Source{Subject: strings.Repeat("a", 1001), Text: "t"}},
		{"text too long", "text", Source{Subject: "x", Text: strings.Repeat("a", 512<<10+1)}},
		{"html too long", "html", Source{Subject: "x", Text: "t", HTML: strings.Repeat("a", 512<<10+1)}},
		{"nul", "text", Source{Subject: "x", Text: "a\x00b"}},
	} {
		_, err := Compile(tc.src, testSchema)
		var te *Error
		if !errors.As(err, &te) {
			t.Errorf("%s: err %v, want *Error", tc.name, err)
			continue
		}
		if te.Code != CodeTemplateInvalid || te.Field != tc.part {
			t.Errorf("%s: code %s field %s (%v), want template_invalid on %s", tc.name, te.Code, te.Field, err, tc.part)
		}
	}
}

func TestCompileErrorNamesLine(t *testing.T) {
	_, err := Compile(Source{Subject: "x", Text: "line1\nline2 {{printf \"%s\" .name}}"}, testSchema)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err %v, want line 2", err)
	}
}

func FuzzCompile(f *testing.F) {
	for _, s := range []string{"{{.name}}", `{{if eq .plan "x"}}a{{else}}b{{end}}`, "{{range .}}{{end}}", "{{", `{{define "a"}}{{end}}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		c, err := Compile(Source{Subject: "s", Text: src, HTML: src}, testSchema)
		if err != nil {
			var te *Error
			if !errors.As(err, &te) {
				t.Fatalf("non-template error %T: %v", err, err)
			}
			return
		}
		// Whatever compiles must render with valid variables without panicking.
		_, _ = c.Render([]byte(`{"name":"Ada","plan":"pro","vip":true,"count":3,"link":"https://x.test"}`))
	})
}
