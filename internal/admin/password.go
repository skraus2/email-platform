// Package admin holds the local operator accounts of the dashboard: argon2id
// password hashes, login throttling and server-side sessions.
package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id cost parameters. Memory is in KiB.
type Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
}

// DefaultParams: 64 MiB, two passes, one lane.
var DefaultParams = Params{Memory: 64 * 1024, Time: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// Upper bounds for parameters read from a stored hash, so a corrupted
	// row cannot make one login allocate gigabytes or spin for minutes.
	maxMemory = 1024 * 1024
	maxTime   = 10
)

// Password length limits (characters).
const (
	MinPasswordLength = 12
	MaxPasswordLength = 128
)

// ValidatePassword checks a new password: 12–128 characters of valid UTF-8,
// no NUL. No composition rules.
func ValidatePassword(pw string) error {
	switch n := utf8.RuneCountInString(pw); {
	case !utf8.ValidString(pw):
		return errors.New("password must be valid UTF-8")
	case n < MinPasswordLength:
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	case n > MaxPasswordLength:
		return fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
	case strings.ContainsRune(pw, 0):
		return errors.New("password must not contain NUL")
	}
	return nil
}

var b64 = base64.RawStdEncoding

// HashPassword returns the PHC string
// $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>.
func HashPassword(pw string, p Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errMalformedHash = errors.New("malformed password hash")

// VerifyPassword compares pw with a PHC string in constant time. outdated
// reports that the hash uses parameters other than current, so it should
// be replaced after a successful login.
func VerifyPassword(encoded, pw string, current Params) (ok, outdated bool, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, false, errMalformedHash
	}
	var p Params
	var threads uint32
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &threads); err != nil || n != 3 ||
		fmt.Sprintf("m=%d,t=%d,p=%d", p.Memory, p.Time, threads) != parts[3] {
		return false, false, errMalformedHash
	}
	if p.Memory < 8 || p.Memory > maxMemory || p.Time < 1 || p.Time > maxTime || threads < 1 || threads > 255 {
		return false, false, errMalformedHash
	}
	p.Threads = uint8(threads)
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, false, errMalformedHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false, false, errMalformedHash
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(want))) //nolint:gosec // bounded by decoding
	return subtle.ConstantTimeCompare(got, want) == 1, p != current, nil
}
