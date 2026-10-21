package emails

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateIdempotencyKey(t *testing.T) {
	t.Parallel()
	valid := []string{"a", "welcome-user-123", "!~", strings.Repeat("k", 255), "Ab0:/_.=+"}
	for _, k := range valid {
		if err := ValidateIdempotencyKey(k); err != nil {
			t.Errorf("ValidateIdempotencyKey(%q) = %v, want nil", k, err)
		}
	}
	invalid := map[string]string{
		"empty":        "",
		"too long":     strings.Repeat("k", 256),
		"space":        "a b",
		"tab":          "a\tb",
		"newline":      "a\nb",
		"NUL":          "a\x00b",
		"DEL":          "a\x7fb",
		"non-ASCII":    "schlüssel",
		"invalid utf8": "a\xffb",
	}
	for name, k := range invalid {
		if err := ValidateIdempotencyKey(k); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Errorf("%s: ValidateIdempotencyKey = %v, want ErrInvalidIdempotencyKey", name, err)
		}
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()
	base := CreateInput{From: "a@example.test", To: "b@example.test", Subject: "Hi", Text: "Hello", HTML: "<p>Hello</p>"}
	fp := base.Fingerprint()
	if len(fp) != 32 {
		t.Fatalf("len = %d, want 32 (SHA-256)", len(fp))
	}
	if again := base.Fingerprint(); string(again) != string(fp) {
		t.Error("fingerprint is not deterministic")
	}

	// The key itself is not part of the payload.
	withKey := base
	withKey.IdempotencyKey = "other"
	if string(withKey.Fingerprint()) != string(fp) {
		t.Error("idempotency key changed the fingerprint")
	}

	// Every payload field changes the fingerprint.
	changes := map[string]func(*CreateInput){
		"from":    func(in *CreateInput) { in.From = "c@example.test" },
		"to":      func(in *CreateInput) { in.To = "c@example.test" },
		"subject": func(in *CreateInput) { in.Subject = "Hi!" },
		"text":    func(in *CreateInput) { in.Text = "Hello!" },
		"html":    func(in *CreateInput) { in.HTML = "" },
	}
	for name, change := range changes {
		in := base
		change(&in)
		if string(in.Fingerprint()) == string(fp) {
			t.Errorf("changing %s did not change the fingerprint", name)
		}
	}

	// Field boundaries are unambiguous: moving bytes between adjacent
	// fields must not collide.
	a := CreateInput{Subject: "ab", Text: "c"}
	b := CreateInput{Subject: "a", Text: "bc"}
	if string(a.Fingerprint()) == string(b.Fingerprint()) {
		t.Error("fingerprint collides when bytes move between fields")
	}
	c := CreateInput{Text: "x"}
	d := CreateInput{HTML: "x"}
	if string(c.Fingerprint()) == string(d.Fingerprint()) {
		t.Error("text and html with the same value collide")
	}
}

func TestRawFingerprintIsStable(t *testing.T) {
	// Keys stored before template sends existed must keep replaying.
	in := CreateInput{From: "a@example.test", To: "b@example.test", Subject: "Hi", Text: "Hello", HTML: "<p>Hello</p>"}
	if got := hex.EncodeToString(in.Fingerprint()); got != "0272c9d4c2cbcec6ccd0dee97e11043a74b2aa8f015482402a1e6d170e4a59c6" {
		t.Fatalf("raw fingerprint changed: %s", got)
	}
}

func TestTemplateFingerprint(t *testing.T) {
	id := uuid.MustParse("0192f0aa-0000-7000-8000-000000000001")
	base := CreateInput{From: "a@example.test", To: "b@example.test", TemplateID: id, TemplateVersion: 0,
		Variables: []byte(`{"name":"Ada","n":1.50,"vip":true}`)}
	fp, err := base.TemplateFingerprint()
	if err != nil || len(fp) != 32 {
		t.Fatalf("fingerprint %x %v", fp, err)
	}
	// Formatting and key order of the variables do not matter.
	same := base
	same.Variables = []byte(" {\n \"vip\" : true, \"n\":1.50 ,\"name\":\"Ada\"}")
	if fp2, _ := same.TemplateFingerprint(); string(fp2) != string(fp) {
		t.Error("formatting changed the fingerprint")
	}
	// Absent and empty variables are the same request.
	a, b := base, base
	a.Variables, b.Variables = nil, []byte(`{}`)
	fa, _ := a.TemplateFingerprint()
	fb, _ := b.TemplateFingerprint()
	if string(fa) != string(fb) {
		t.Error("absent vs empty variables differ")
	}
	for name, change := range map[string]func(*CreateInput){
		"from":      func(in *CreateInput) { in.From = "c@example.test" },
		"to":        func(in *CreateInput) { in.To = "c@example.test" },
		"template":  func(in *CreateInput) { in.TemplateID = uuid.New() },
		"version":   func(in *CreateInput) { in.TemplateVersion = 1 },
		"variables": func(in *CreateInput) { in.Variables = []byte(`{"name":"Bob","n":1.50,"vip":true}`) },
		"number":    func(in *CreateInput) { in.Variables = []byte(`{"name":"Ada","n":1.5,"vip":true}`) },
		"type":      func(in *CreateInput) { in.Variables = []byte(`{"name":"Ada","n":"1.50","vip":true}`) },
	} {
		in := base
		change(&in)
		if f, _ := in.TemplateFingerprint(); string(f) == string(fp) {
			t.Errorf("changing %s did not change the fingerprint", name)
		}
	}
	// A template request never collides with a raw one.
	raw := CreateInput{From: base.From, To: base.To}
	if string(raw.Fingerprint()) == string(fp) {
		t.Error("raw and template fingerprints collide")
	}
	for _, bad := range []string{`[]`, `{"a":1,"a":2}`, `{"a":1} x`, `null`} {
		in := base
		in.Variables = []byte(bad)
		if _, err := in.TemplateFingerprint(); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestValidateTemplateForm(t *testing.T) {
	ok := CreateInput{From: "a@example.test", To: "b@example.test", TemplateID: uuid.New(), TemplateVersion: 2}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		change func(*CreateInput)
		field  string
	}{
		"subject with template": {func(in *CreateInput) { in.Subject = "x" }, "subject"},
		"text with template":    {func(in *CreateInput) { in.Text = "x" }, "text"},
		"html with template":    {func(in *CreateInput) { in.HTML = "x" }, "html"},
		"negative version":      {func(in *CreateInput) { in.TemplateVersion = -1 }, "template_version"},
		"bad to":                {func(in *CreateInput) { in.To = "nope" }, "to"},
	} {
		in := ok
		tc.change(&in)
		var ve *ValidationError
		if err := in.Validate(); !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: %v, want error on %s", name, err, tc.field)
		}
	}
}

func TestScheduledFingerprint(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 30, 0, 123456789, time.UTC)
	raw := CreateInput{From: "a@example.test", To: "b@example.test", Subject: "Hi", Text: "Hello", HTML: "<p>Hello</p>"}
	tmpl := CreateInput{From: "a@example.test", To: "b@example.test", TemplateID: uuid.MustParse("0192f0aa-0000-7000-8000-000000000001")}
	base := map[string][]byte{}
	base["raw"] = raw.Fingerprint()
	base["template"], _ = tmpl.TemplateFingerprint()
	for name, in := range map[string]CreateInput{"raw": raw, "template": tmpl} {
		fp := func(in CreateInput) []byte {
			if name == "raw" {
				return in.Fingerprint()
			}
			f, _ := in.TemplateFingerprint()
			return f
		}
		in.ScheduledAt = &at
		scheduled := fp(in)
		if string(scheduled) == string(base[name]) {
			t.Errorf("%s: scheduled_at not part of the fingerprint", name)
		}
		// Same instant in another zone and below microseconds: same request.
		other := at.In(time.FixedZone("x", 2*3600)).Add(-789 * time.Nanosecond)
		in.ScheduledAt = &other
		if string(fp(in)) != string(scheduled) {
			t.Errorf("%s: zone or nanoseconds changed the fingerprint", name)
		}
		later := at.Add(time.Microsecond)
		in.ScheduledAt = &later
		if string(fp(in)) == string(scheduled) {
			t.Errorf("%s: a different time gave the same fingerprint", name)
		}
	}
}

func TestTemplateFingerprintIsStable(t *testing.T) {
	// Keys stored before scheduled sends existed must keep replaying.
	in := CreateInput{From: "a@example.test", To: "b@example.test", TemplateID: uuid.MustParse("0192f0aa-0000-7000-8000-000000000001"),
		TemplateVersion: 3, Variables: []byte(`{"name":"Ada","n":1.50}`)}
	fp, err := in.TemplateFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(fp); got != "c99eeb1688683630fb05dfecb8b098bed20885d07e73be1f7caabd52bf351b6a" {
		t.Fatalf("template fingerprint changed: %s", got)
	}
}
