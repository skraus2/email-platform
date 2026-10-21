package emails

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
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

// templateFingerprintVersion prefixes template requests, so they never
// collide with raw ones.
const templateFingerprintVersion = "v2t"

// TemplateFingerprint is the SHA-256 of a template request: from, to,
// template, the version as requested (0 = "published", so a retry after a
// newer version was published still replays) and the variables in a
// canonical encoding (sorted keys, values with their JSON spelling).
func (in CreateInput) TemplateFingerprint() ([]byte, error) {
	vars := map[string]any{}
	if len(bytes.TrimSpace(in.Variables)) > 0 {
		if err := decodeUniqueObject(in.Variables, vars); err != nil {
			return nil, err
		}
	}
	canonical, err := json.Marshal(vars) // map keys are sorted
	if err != nil {
		return nil, fmt.Errorf("encode variables: %w", err)
	}
	version := "published"
	if in.TemplateVersion > 0 {
		version = strconv.Itoa(in.TemplateVersion)
	}
	h := sha256.New()
	h.Write([]byte(templateFingerprintVersion))
	for _, f := range in.withSchedule(in.From, in.To, in.TemplateID.String(), version, string(canonical)) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(f))) //nolint:gosec // bounded by the request limit
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return h.Sum(nil), nil
}

// withSchedule appends the requested send time (UTC, microseconds, as
// stored) when there is one. Fields are length-prefixed, so requests
// without a time keep the fingerprint they always had.
func (in CreateInput) withSchedule(fields ...string) []string {
	if in.ScheduledAt != nil {
		fields = append(fields, in.ScheduledAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano))
	}
	return fields
}

// errVariablesNotObject: the variables are not one JSON object with unique
// keys.
var errVariablesNotObject = errors.New("variables must be a JSON object with unique names")

func decodeUniqueObject(raw []byte, into map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errVariablesNotObject
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errVariablesNotObject
		}
		key, _ := tok.(string)
		if _, dup := into[key]; dup {
			return errVariablesNotObject
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			return errVariablesNotObject
		}
		into[key] = v
	}
	if _, err := dec.Token(); err != nil {
		return errVariablesNotObject
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errVariablesNotObject
	}
	return nil
}

// Fingerprint is the SHA-256 of the request payload. Each field is
// length-prefixed, so the encoding is unambiguous and independent of how
// the client formatted its JSON.
func (in CreateInput) Fingerprint() []byte {
	h := sha256.New()
	h.Write([]byte(fingerprintVersion))
	for _, f := range in.withSchedule(in.From, in.To, in.Subject, in.Text, in.HTML) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(f))) //nolint:gosec // fields are bounded far below 4 GiB by the request limit
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return h.Sum(nil)
}
