// Package webhooks manages outbound webhook endpoints of a project and
// delivers signed status-change notifications to them.
package webhooks

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Keyring holds the key-encryption keys for endpoint secrets: the active key
// encrypts, every listed key decrypts (so keys can be rotated).
type Keyring struct {
	active string
	aeads  map[string]cipher.AEAD
}

const (
	kekSize   = 32 // AES-256
	nonceSize = 12 // GCM standard nonce
)

var keyIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ParseKeyring parses "<id>:<base64 32 bytes>[,<id>:<base64>…]"; the first
// entry is the active key. Errors never contain key material.
func ParseKeyring(spec string) (*Keyring, error) {
	k := &Keyring{aeads: map[string]cipher.AEAD{}}
	if strings.TrimSpace(spec) == "" {
		return nil, errors.New("no keys given")
	}
	for i, entry := range strings.Split(spec, ",") {
		id, b64, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("entry %d: want <id>:<base64 key> with an id of 1-32 characters a-z, 0-9, _ or -", i+1)
		}
		if _, dup := k.aeads[id]; dup {
			return nil, fmt.Errorf("entry %d: key id %q listed twice", i+1, id)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != kekSize {
			return nil, fmt.Errorf("entry %d (key id %q): key must be %d bytes, standard base64", i+1, id, kekSize)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, fmt.Errorf("entry %d (key id %q): invalid key", i+1, id)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("entry %d (key id %q): invalid key", i+1, id)
		}
		k.aeads[id] = aead
		if i == 0 {
			k.active = id
		}
	}
	return k, nil
}

// ActiveKeyID is the id of the key new secrets are encrypted with.
func (k *Keyring) ActiveKeyID() string { return k.active }

// aad binds a ciphertext to its endpoint and project, so it cannot be moved
// to another row.
func aad(endpointID, projectID uuid.UUID) []byte {
	return []byte("webhook-secret:v1:" + endpointID.String() + ":" + projectID.String())
}

// Encrypt seals secret with the active key. The result is nonce ‖ ciphertext
// ‖ tag.
func (k *Keyring) Encrypt(secret []byte, endpointID, projectID uuid.UUID) ([]byte, string, error) {
	aead := k.aeads[k.active]
	nonce := make([]byte, nonceSize, nonceSize+len(secret)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("generate nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, secret, aad(endpointID, projectID)), k.active, nil
}

// ErrUndecryptable means a stored secret cannot be decrypted with the
// configured keys (unknown key id, wrong key, or a moved or damaged value).
var ErrUndecryptable = errors.New("webhook secret cannot be decrypted with the configured keys")

// Decrypt opens a secret sealed by Encrypt for the same endpoint and project.
func (k *Keyring) Decrypt(ciphertext []byte, keyID string, endpointID, projectID uuid.UUID) ([]byte, error) {
	aead, ok := k.aeads[keyID]
	if !ok || len(ciphertext) < nonceSize+aead.Overhead() {
		return nil, ErrUndecryptable
	}
	pt, err := aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], aad(endpointID, projectID))
	if err != nil {
		return nil, ErrUndecryptable
	}
	return pt, nil
}

// NewSecret returns a fresh endpoint secret: "whsec_" and 32 random bytes in
// unpadded base64url. Receivers use the whole string as the HMAC key.
func NewSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (it panics instead)
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b)
}
