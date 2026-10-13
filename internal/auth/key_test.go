package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestGenerateKeyFormat(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Token, "ep_"+k.Prefix+"_") {
		t.Fatalf("token %q does not start with ep_<prefix>_", k.Token)
	}
	if len(k.Prefix) != prefixLen {
		t.Errorf("prefix length = %d, want %d", len(k.Prefix), prefixLen)
	}
	if len(k.Token) != len("ep_")+prefixLen+1+secretLen {
		t.Errorf("token length = %d", len(k.Token))
	}
	if len(k.SecretHash) != 32 {
		t.Errorf("hash length = %d, want 32 (SHA-256)", len(k.SecretHash))
	}
	if bytes.Contains(k.SecretHash, []byte(k.Token)) {
		t.Error("hash contains the token")
	}
}

func TestGenerateKeyIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		k, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if seen[k.Prefix] || seen[k.Token] {
			t.Fatal("duplicate prefix or token")
		}
		seen[k.Prefix], seen[k.Token] = true, true
	}
}

func TestParseRoundTripIncludingUnderscoreInSecret(t *testing.T) {
	sawUnderscore := false
	for range 500 {
		k, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		_, secret, _ := strings.Cut(strings.TrimPrefix(k.Token, "ep_"+k.Prefix), "_")
		if strings.Contains(secret, "_") {
			sawUnderscore = true
		}
		prefix, gotSecret, err := ParseToken(k.Token)
		if err != nil {
			t.Fatalf("ParseToken(%q): %v", k.Token, err)
		}
		if prefix != k.Prefix || gotSecret != secret {
			t.Fatalf("ParseToken(%q) = %q, %q", k.Token, prefix, gotSecret)
		}
		if !Matches(gotSecret, k.SecretHash) {
			t.Fatal("generated secret does not match its own hash")
		}
	}
	if !sawUnderscore {
		t.Fatal("test never exercised a secret containing '_'")
	}
}

func TestParseTokenRejectsMalformed(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty":           "",
		"wrong scheme":    strings.Replace(k.Token, "ep_", "xx_", 1),
		"no separators":   "ep" + k.Prefix + "secret",
		"short prefix":    "ep_ABC_" + strings.Repeat("a", secretLen),
		"short secret":    "ep_" + k.Prefix + "_abc",
		"long secret":     k.Token + "a",
		"bad prefix char": "ep_" + strings.Repeat("!", prefixLen) + "_" + strings.Repeat("a", secretLen),
		"bad secret char": "ep_" + k.Prefix + "_" + strings.Repeat("$", secretLen),
		"whitespace":      " " + k.Token,
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseToken(token); !errors.Is(err, ErrMalformedKey) {
				t.Fatalf("ParseToken(%q) err = %v, want ErrMalformedKey", token, err)
			}
		})
	}
}

func TestMatchesRejectsWrongSecret(t *testing.T) {
	a, _ := GenerateKey()
	b, _ := GenerateKey()
	_, secretB, _ := ParseToken(b.Token)
	if Matches(secretB, a.SecretHash) {
		t.Fatal("secret of key b matched hash of key a")
	}
	if Matches("", a.SecretHash) {
		t.Fatal("empty secret matched")
	}
	if Matches(secretB, nil) {
		t.Fatal("nil hash matched")
	}
}

func TestDummyHashNeverMatches(t *testing.T) {
	k, _ := GenerateKey()
	_, secret, _ := ParseToken(k.Token)
	if Matches(secret, DummyHash()) {
		t.Fatal("dummy hash matched a real secret")
	}
	if len(DummyHash()) != 32 {
		t.Fatal("dummy hash must have the real hash length for equal-time compare")
	}
}

func TestScopes(t *testing.T) {
	if err := ValidateScopes([]string{ScopeEmailsSend, ScopeEmailsRead}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{nil, {}, {"emails:delete"}, {ScopeEmailsSend, ScopeEmailsSend}} {
		if err := ValidateScopes(bad); err == nil {
			t.Errorf("ValidateScopes(%v) = nil, want error", bad)
		}
	}
}
