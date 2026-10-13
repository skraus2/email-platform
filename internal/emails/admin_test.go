package emails

import "testing"

func TestTerminalSafe(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"a***@example.test":         "a***@example.test",
		"\u009b31m***@example.test": `\u009b31m***@example.test`,
		"a***@exa\u202eelpm.test":   `a***@exa\u202eelpm.test`,
		"ü***@bücher.test":          "ü***@bücher.test",
		"tab\there":                 `tab\u0009here`,
	} {
		if got := terminalSafe(in); got != want {
			t.Errorf("terminalSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
