package logging

import "testing"

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"customer@example.test": "c***@example.test",
		"a@b.test":              "a***@b.test",
		"no-at-sign":            "***",
		"@example.test":         "***",
		"":                      "***",
	} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
