package emails

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"
)

// Idempotency keys let clients retry a send without creating a second email.
// A key is scoped to the project and the operation; a repeated request with
// the same key and payload gets the original response, the same key with a
// different payload is refused.
const (
	MaxIdempotencyKeyLen = 255
	// IdempotencyKeyTTL is how long a key is remembered. Afterwards the same
	// key creates a new email.
	IdempotencyKeyTTL = 24 * time.Hour

	operationCreateEmail = "create_email"
)

var (
	// ErrInvalidIdempotencyKey means the key is empty, too long or contains
	// characters other than printable ASCII without space.
	ErrInvalidIdempotencyKey = errors.New("idempotency key must be 1-255 printable ASCII characters without spaces")
	// ErrIdempotencyKeyReused means the key was already used with a
	// different payload.
	ErrIdempotencyKeyReused = errors.New("idempotency key was already used with a different payload")
)

// ValidateIdempotencyKey checks the key format (bytes 0x21-0x7E only).
func ValidateIdempotencyKey(key string) error {
	if key == "" || len(key) > MaxIdempotencyKeyLen {
		return ErrInvalidIdempotencyKey
	}
	for i := range len(key) {
		if c := key[i]; c < 0x21 || c > 0x7e {
			return ErrInvalidIdempotencyKey
		}
	}
	return nil
}

// fingerprintVersion is hashed first so that the payload definition can grow
// (e.g. template fields) without old and new fingerprints colliding.
const fingerprintVersion = "v1"

// Fingerprint is the SHA-256 of the request payload. Each field is
// length-prefixed, so the encoding is unambiguous and independent of how
// the client formatted its JSON.
func (in CreateInput) Fingerprint() []byte {
	h := sha256.New()
	h.Write([]byte(fingerprintVersion))
	for _, f := range []string{in.From, in.To, in.Subject, in.Text, in.HTML} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(f))) //nolint:gosec // fields are bounded far below 4 GiB by the request limit
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return h.Sum(nil)
}
