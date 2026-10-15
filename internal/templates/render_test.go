package templates

import (
	"errors"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, src Source) *Compiled {
	t.Helper()
	c, err := Compile(src, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRenderParts(t *testing.T) {
	c := mustCompile(t, Source{
		Subject: "Welcome, {{.name}}",
		Text:    `Hi {{.name}} <{{.plan}}> {{if .vip}}VIP{{else}}regular{{end}} {{.count}}`,
		HTML:    `<p>Hi {{.name}}</p>{{if eq .plan "pro"}}<p>Pro</p>{{end}}<a href="{{.link}}">go</a>`,
	})
	r, err := c.Render([]byte(`{"name":"Ada & <Bob>","plan":"pro","vip":false,"count":1.50,"link":"https://example.test/a?b=1&c=2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "Welcome, Ada & <Bob>" {
		t.Errorf("subject %q", r.Subject)
	}
	// Text is not HTML-escaped; numbers keep their JSON spelling.
	if r.Text != "Hi Ada & <Bob> <pro> regular 1.50" {
		t.Errorf("text %q", r.Text)
	}
	want := `<p>Hi Ada &amp; &lt;Bob&gt;</p><p>Pro</p><a href="https://example.test/a?b=1&amp;c=2">go</a>`
	if r.HTML != want {
		t.Errorf("html\n got %q\nwant %q", r.HTML, want)
	}
}

func TestRenderEscapingVectors(t *testing.T) {
	for _, tc := range []struct {
		html, value, mustNot, must string
	}{
		{`<p>{{.name}}</p>`, `<script>alert(1)</script>`, "<script>", "&lt;script&gt;"},
		{`<p title="{{.name}}">x</p>`, `" onmouseover="alert(1)`, `" onmouseover`, "&#34;"},
		{`<a href="{{.link}}">x</a>`, `javascript:alert(1)`, "javascript:", "#ZgotmplZ"},
		{`<img src="{{.link}}">`, `data:text/html;base64,PHNjcmlwdD4=`, "data:text", "#ZgotmplZ"},
		{`<a href="{{.link}}">x</a>`, ` JaVaScRiPt:alert(1)`, "JaVaScRiPt", "#ZgotmplZ"},
		{`<p style="color: {{.name}}">x</p>`, `red; background: url(javascript:alert(1))`, "javascript", "ZgotmplZ"},
		{`<p>{{.name}}</p>`, `{{.plan}}`, "pro", "{{.plan}}"},
		{`<script>var a = {{.name}};</script>`, `"; alert(1); "`, `= ""; alert(1)`, `= "\"; alert(1); \""`},
		{`<script>var a = "{{.name}}";</script>`, `</script><script>alert(1)`, "</script><script>", `\u003c\/script\u003e`},
		{`<a title={{.name}}>x</a>`, `x onmouseover=alert(1)`, " onmouseover=", "&#32;onmouseover&#61;"},
		{`<p {{.name}}="x">y</p>`, `onclick`, "onclick=", "ZgotmplZ"},
	} {
		c := mustCompile(t, Source{Subject: "x", Text: "t", HTML: tc.html})
		vars := `{"name":` + jsonString(tc.value) + `,"link":` + jsonString(tc.value) + `,"plan":"pro"}`
		r, err := c.Render([]byte(vars))
		if err != nil {
			t.Fatalf("%s: %v", tc.html, err)
		}
		if strings.Contains(r.HTML, tc.mustNot) || !strings.Contains(r.HTML, tc.must) {
			t.Errorf("%s with %q rendered %q", tc.html, tc.value, r.HTML)
		}
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestRenderOptionalZeroValues(t *testing.T) {
	c := mustCompile(t, Source{Subject: "{{.name}}", Text: "[{{.plan}}][{{.count}}][{{.vip}}]{{if .plan}}P{{end}}{{if .vip}}V{{end}}"})
	r, err := c.Render([]byte(`{"name":"Ada"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "[][0][false]" {
		t.Fatalf("text %q", r.Text)
	}
	// Absent variables (nil raw) work when nothing is required.
	c2, err := Compile(Source{Subject: "x", Text: "[{{.plan}}]"}, Schema{"plan": {Type: TypeString, MaxLength: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := c2.Render(nil); err != nil || r.Text != "[]" {
		t.Fatalf("nil variables: %q %v", r.Text, err)
	}
}

func TestRenderVariablesInvalid(t *testing.T) {
	c := mustCompile(t, Source{Subject: "x", Text: "{{.name}}"})
	secret := "s3cr3t"
	for _, tc := range []struct{ raw, field string }{
		{`{}`, "variables.name"},
		{`{"name":null}`, "variables.name"},
		{`{"name":1}`, "variables.name"},
		{`{"name":"Ada","vip":"yes"}`, "variables.vip"},
		{`{"name":"Ada","count":"3"}`, "variables.count"},
		{`{"name":"Ada","count":true}`, "variables.count"},
		{`{"name":"Ada","plan":"` + strings.Repeat("ü", 21) + `"}`, "variables.plan"},
		{`{"name":"a\u0000b"}`, "variables.name"},
		{`{"name":"Ada","` + secret + `":1}`, "variables"},
		{`{"name":"Ada","name":"Bob"}`, "variables"},
		{`[]`, "variables"},
		{`null`, "variables"},
		{`"x"`, "variables"},
		{`{"name":"Ada"} {}`, "variables"},
		{`{"name":{"a":1}}`, "variables.name"},
		{`{"name":"Ada","count":` + strings.Repeat("9", 101) + `}`, "variables.count"},
		{`{"name":"Ada","count":1e400}`, "variables.count"},
		{`{"name":"Ada","count":-1e400}`, "variables.count"},
		{`{"name":"Ada","count":NaN}`, "variables.count"},
		{`{"name":"Ada","count":01}`, "variables"},
		{`{"name":"Ada","count":+1}`, "variables.count"},
	} {
		_, err := c.Render([]byte(tc.raw))
		var te *Error
		if !errors.As(err, &te) || te.Code != CodeVariablesInvalid || te.Field != tc.field {
			t.Errorf("%s: err %v, want variables_invalid on %s", tc.raw, err, tc.field)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Ada") {
			t.Errorf("%s: error echoes input: %v", tc.raw, err)
		}
	}
	// max_length counts characters, not bytes.
	if _, err := c.Render([]byte(`{"name":"Ada","plan":"` + strings.Repeat("ü", 20) + `"}`)); err != nil {
		t.Fatalf("20 characters: %v", err)
	}
}

func TestRenderOutputLimits(t *testing.T) {
	big := Schema{"s": {Type: TypeString, MaxLength: 10000}, "nl": {Type: TypeString}}
	long := strings.Repeat("a", 10000)
	for _, tc := range []struct {
		name string
		src  Source
		vars string
	}{
		{"subject newline", Source{Subject: "Hi {{.nl}}", Text: "t"}, `{"nl":"a\r\nBcc: x@example.test"}`},
		{"subject empty", Source{Subject: "{{.nl}}", Text: "t"}, `{}`},
		{"subject too long", Source{Subject: "{{.s}}", Text: "t"}, `{"s":"` + long + `"}`},
		{"text too long", Source{Subject: "x", Text: strings.Repeat("{{.s}}", 53)}, `{"s":"` + long + `"}`},
		{"html too long", Source{Subject: "x", Text: "t", HTML: strings.Repeat("{{.s}}", 53)}, `{"s":"` + long + `"}`},
		{"all bodies empty", Source{Subject: "x", Text: "{{.nl}}", HTML: "{{.nl}}"}, `{}`},
	} {
		c, err := Compile(tc.src, big)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		_, err = c.Render([]byte(tc.vars))
		var te *Error
		if !errors.As(err, &te) || te.Code != CodeRenderFailed {
			t.Errorf("%s: err %v, want render_failed", tc.name, err)
		}
	}
	// Subject length counts characters after rendering: 255 pass, 256 fail.
	sc, _ := Compile(Source{Subject: "{{.s}}", Text: "t"}, big)
	if _, err := sc.Render([]byte(`{"s":"` + strings.Repeat("ü", 255) + `"}`)); err != nil {
		t.Fatalf("255 characters: %v", err)
	}
	var te *Error
	if _, err := sc.Render([]byte(`{"s":"` + strings.Repeat("a", 256) + `"}`)); !errors.As(err, &te) || te.Code != CodeRenderFailed {
		t.Fatalf("256 characters: %v", err)
	}
	// 52 × 10000 bytes stays below the 512 KiB limit.
	c, _ := Compile(Source{Subject: "x", Text: strings.Repeat("{{.s}}", 52)}, big)
	if r, err := c.Render([]byte(`{"s":"` + long + `"}`)); err != nil || len(r.Text) != 520000 {
		t.Fatalf("below limit: %d %v", len(r.Text), err)
	}
}
