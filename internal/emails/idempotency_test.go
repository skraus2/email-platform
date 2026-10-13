package emails

import (
	"errors"
	"strings"
	"testing"
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
