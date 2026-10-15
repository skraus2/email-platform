// Package auth implements project API keys: generation, parsing, hashing and
// scope checks.
//
// Token format: "ep_" + prefix + "_" + secret, where prefix is 12 lowercase
// base32 characters (public, used for lookup, safe to log) and secret is 32
// random bytes in unpadded base64url (43 characters; it may itself contain
// "_"). Only SHA-256(secret) is stored. With 256 bits of entropy a slow
// password hash would add latency without adding protection.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	tokenScheme = "ep"
	prefixLen   = 12
	secretBytes = 32
	secretLen   = 43 // base64.RawURLEncoding.EncodedLen(secretBytes)
)

// Scopes a key can carry.
const (
	ScopeEmailsSend     = "emails:send"
	ScopeEmailsRead     = "emails:read"
	ScopeTemplatesRead  = "templates:read"
	ScopeTemplatesWrite = "templates:write"
)

// KnownScopes lists every scope a key may carry.
var KnownScopes = []string{ScopeEmailsSend, ScopeEmailsRead, ScopeTemplatesRead, ScopeTemplatesWrite}

// DefaultScopes is what a new key gets unless the admin narrows it.
var DefaultScopes = []string{ScopeEmailsSend, ScopeEmailsRead}

// ErrMalformedKey means the token does not have the API key shape.
var ErrMalformedKey = errors.New("malformed api key")

var prefixEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GeneratedKey is a new key. Token is shown to the admin exactly once and never
// stored; Prefix and SecretHash are persisted.
type GeneratedKey struct {
	Token      string
	Prefix     string
	SecretHash []byte
}

// GenerateKey creates a new random key.
func GenerateKey() (GeneratedKey, error) {
	var p [8]byte
	var s [secretBytes]byte
	if _, err := rand.Read(p[:]); err != nil {
		return GeneratedKey{}, fmt.Errorf("generate key prefix: %w", err)
	}
	if _, err := rand.Read(s[:]); err != nil {
		return GeneratedKey{}, fmt.Errorf("generate key secret: %w", err)
	}
	prefix := strings.ToLower(prefixEncoding.EncodeToString(p[:]))[:prefixLen]
	secret := base64.RawURLEncoding.EncodeToString(s[:])
	return GeneratedKey{
		Token:      tokenScheme + "_" + prefix + "_" + secret,
		Prefix:     prefix,
		SecretHash: hashSecret(secret),
	}, nil
}

// ParseToken splits a token into prefix and secret. It only checks the shape;
// use Matches against the stored hash to authenticate.
func ParseToken(token string) (prefix, secret string, err error) {
	// SplitN with 3: the secret may contain "_" itself.
	parts := strings.SplitN(token, "_", 3)
	if len(parts) != 3 || parts[0] != tokenScheme {
		return "", "", ErrMalformedKey
	}
	prefix, secret = parts[1], parts[2]
	if len(prefix) != prefixLen || len(secret) != secretLen {
		return "", "", ErrMalformedKey
	}
	for _, c := range prefix {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return "", "", ErrMalformedKey
		}
	}
	if _, err := base64.RawURLEncoding.DecodeString(secret); err != nil {
		return "", "", ErrMalformedKey
	}
	return prefix, secret, nil
}

// Matches reports whether secret hashes to stored, in constant time.
func Matches(secret string, stored []byte) bool {
	if len(stored) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(hashSecret(secret), stored) == 1
}

var dummyHash = func() []byte {
	h := sha256.Sum256([]byte("email-platform: no key has this hash"))
	return h[:]
}()

// DummyHash returns a hash no real secret matches. Comparing against it when
// a prefix is unknown keeps the timing of "unknown key" and "wrong secret"
// equal, so responses do not reveal which prefixes exist.
func DummyHash() []byte { return dummyHash }

func hashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// ValidateScopes rejects empty, unknown or duplicate scopes.
func ValidateScopes(scopes []string) error {
	if len(scopes) == 0 {
		return errors.New("at least one scope is required")
	}
	seen := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		if !slices.Contains(KnownScopes, s) {
			return fmt.Errorf("unknown scope %q", s)
		}
		if seen[s] {
			return fmt.Errorf("duplicate scope %q", s)
		}
		seen[s] = true
	}
	return nil
}
