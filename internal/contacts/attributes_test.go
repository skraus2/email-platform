package contacts

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAttributes(t *testing.T) {
	t.Parallel()
	ok := []struct{ in, want string }{
		{``, `{}`},
		{`{}`, `{}`},
		{` {"b": true, "a": "x"} `, `{"a":"x","b":true}`},
		{`{"n": 1.50}`, `{"n":1.50}`},
		{`{"n": -12.5e-3, "m": 1E+20}`, `{"m":1E+20,"n":-12.5e-3}`},
		{`{"plan": "<pro>&"}`, `{"plan":"<pro>&"}`},
		{`{"s": "` + strings.Repeat("ü", 1000) + `"}`, `{"s":"` + strings.Repeat("ü", 1000) + `"}`},
	}
	for _, c := range ok {
		got, err := ValidateAttributes([]byte(c.in))
		if err != nil || string(got) != c.want {
			t.Errorf("%q: got %s, %v; want %s", c.in, got, err, c.want)
		}
	}

	many := make([]string, 21)
	for i := range many {
		many[i] = `"a` + string(rune('a'+i)) + `": 1`
	}
	big := make([]string, 9)
	for i := range big {
		big[i] = `"k` + string(rune('a'+i)) + `": "` + strings.Repeat("x", 1000) + `"`
	}
	bad := []struct{ name, in, field string }{
		{"not an object", `[1]`, "attributes"},
		{"null", `null`, "attributes"},
		{"invalid JSON", `{"a":`, "attributes"},
		{"trailing data", `{} {}`, "attributes"},
		{"bad name", `{"Name": "x"}`, "attributes"},
		{"name too long", `{"` + strings.Repeat("a", 41) + `": "x"}`, "attributes"},
		{"reserved name", `{"unsubscribe_url": "x"}`, "attributes.unsubscribe_url"},
		{"duplicate", `{"a": 1, "a": 2}`, "attributes"},
		{"null value", `{"a": null}`, "attributes.a"},
		{"nested", `{"a": {"b": 1}}`, "attributes.a"},
		{"array", `{"a": [1]}`, "attributes.a"},
		{"string too long", `{"a": "` + strings.Repeat("x", 1001) + `"}`, "attributes.a"},
		{"NUL", `{"a": "x\u0000y"}`, "attributes.a"},
		{"huge number", `{"a": 1e400}`, "attributes.a"},
		// PostgreSQL stores numbers as numeric and prints them written out:
		// short literals must not expand past the size limit.
		{"underflow", `{"a": 1e-800}`, "attributes.a"},
		{"large exponent", `{"a": 1e308}`, "attributes.a"},
		{"tiny exponent", `{"a": 1e-16000}`, "attributes.a"},
		{"zero with exponent", `{"a": 0e-10000}`, "attributes.a"},
		{"long fraction", `{"a": 0.` + strings.Repeat("1", 64) + `}`, "attributes.a"},
		{"too many", `{` + strings.Join(many, ",") + `}`, "attributes"},
		{"too big", `{` + strings.Join(big, ",") + `}`, "attributes"},
	}
	for _, c := range bad {
		_, err := ValidateAttributes([]byte(c.in))
		var e *Error
		if !errors.As(err, &e) || e.Field != c.field {
			t.Errorf("%s: got %v, want error on %s", c.name, err, c.field)
			continue
		}
		if strings.Contains(e.Error(), "xxx") {
			t.Errorf("%s: error echoes the value: %v", c.name, e)
		}
	}
}

func TestValidateConsentSource(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"signup-form", "Newsletter-Formular Ü", strings.Repeat("s", 100)} {
		if err := validateConsentSource(v); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	for _, v := range []string{"", strings.Repeat("s", 101), "a\nb", "a\x00b", "rtl\u202e", "\xff"} {
		if err := validateConsentSource(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestNewToken(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 100 {
		tok, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		if !validToken(tok) || seen[tok] {
			t.Fatalf("token %q invalid or repeated", tok)
		}
		seen[tok] = true
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("a", 42) + "=", strings.Repeat("a", 42) + "+"} {
		if validToken(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
