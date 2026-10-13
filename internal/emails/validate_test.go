package emails

import (
	"errors"
	"strings"
	"testing"
)

func valid() CreateInput {
	return CreateInput{From: "noreply@example.test", To: "customer@example.test", Subject: "Hi", Text: "Hello"}
}

func TestValidateAcceptsValidInput(t *testing.T) {
	for name, mutate := range map[string]func(*CreateInput){
		"text only":            func(*CreateInput) {},
		"html only":            func(in *CreateInput) { in.Text, in.HTML = "", "<p>x</p>" },
		"both":                 func(in *CreateInput) { in.HTML = "<p>x</p>" },
		"subject 255 runes":    func(in *CreateInput) { in.Subject = strings.Repeat("ä", 255) },
		"body at limit":        func(in *CreateInput) { in.Text = strings.Repeat("a", MaxBodyBytes) },
		"plus addressing":      func(in *CreateInput) { in.To = "customer+tag@example.test" },
		"subdomain sender":     func(in *CreateInput) { in.From = "noreply@mail.example.test" },
		"unicode subject":      func(in *CreateInput) { in.Subject = "Grüße ✓" },
		"tab in subject is ok": func(in *CreateInput) { in.Subject = "a\tb" },
	} {
		t.Run(name, func(t *testing.T) {
			in := valid()
			mutate(&in)
			if err := in.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name  string
		field string
		mut   func(*CreateInput)
	}{
		{"missing from", "from", func(in *CreateInput) { in.From = "" }},
		{"display name from", "from", func(in *CreateInput) { in.From = "Shop <noreply@example.test>" }},
		{"from with spaces", "from", func(in *CreateInput) { in.From = " noreply@example.test" }},
		{"from too long", "from", func(in *CreateInput) { in.From = strings.Repeat("a", 250) + "@example.test" }},
		{"missing to", "to", func(in *CreateInput) { in.To = "" }},
		{"to list", "to", func(in *CreateInput) { in.To = "a@example.test, b@example.test" }},
		{"to no domain", "to", func(in *CreateInput) { in.To = "customer" }},
		{"to CRLF", "to", func(in *CreateInput) { in.To = "customer@example.test\r\nBcc: x@example.test" }},
		{"missing subject", "subject", func(in *CreateInput) { in.Subject = "" }},
		{"subject too long", "subject", func(in *CreateInput) { in.Subject = strings.Repeat("a", 256) }},
		{"subject CRLF", "subject", func(in *CreateInput) { in.Subject = "Hi\r\nBcc: victim@example.test" }},
		{"subject LF", "subject", func(in *CreateInput) { in.Subject = "Hi\nX-Header: y" }},
		{"subject NUL", "subject", func(in *CreateInput) { in.Subject = "Hi\x00" }},
		{"no body", "text", func(in *CreateInput) { in.Text, in.HTML = "", "" }},
		{"text too large", "text", func(in *CreateInput) { in.Text = strings.Repeat("a", MaxBodyBytes+1) }},
		{"html too large", "html", func(in *CreateInput) { in.HTML = strings.Repeat("a", MaxBodyBytes+1) }},
		{"text NUL", "text", func(in *CreateInput) { in.Text = "a\x00b" }},
		{"html NUL", "html", func(in *CreateInput) { in.Text, in.HTML = "", "<p>\x00</p>" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid()
			tt.mut(&in)
			err := in.Validate()
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate() = %v, want *ValidationError", err)
			}
			if ve.Field != tt.field {
				t.Fatalf("field = %q, want %q (%v)", ve.Field, tt.field, err)
			}
			if strings.Contains(err.Error(), "customer@example.test") {
				t.Fatalf("validation message echoes the address: %q", err)
			}
		})
	}
}
