package webhooks

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestParseKeyring(t *testing.T) {
	k, err := ParseKeyring("k2:" + key(2) + ", k1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	if k.ActiveKeyID() != "k2" {
		t.Fatalf("active = %s, want the first entry", k.ActiveKeyID())
	}
	secretKey := key(9)
	for name, spec := range map[string]string{
		"empty":         "",
		"no id":         ":" + key(1),
		"no key":        "k1:",
		"no separator":  key(1),
		"short key":     "k1:" + base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"long key":      "k1:" + base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"not base64":    "k1:!!!!",
		"duplicate id":  "k1:" + key(1) + ",k1:" + key(2),
		"bad id chars":  "k 1:" + key(1),
		"long id":       strings.Repeat("k", 33) + ":" + key(1),
		"empty entry":   "k1:" + key(1) + ",,",
		"leaks the key": "k1:" + secretKey + "x",
	} {
		_, err := ParseKeyring(spec)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), secretKey) || strings.Contains(err.Error(), key(1)) {
			t.Errorf("%s: error %q contains key material", name, err)
		}
	}
}

func TestSecretEncryption(t *testing.T) {
	k, err := ParseKeyring("k1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, project := uuid.New(), uuid.New()
	secret := NewSecret()
	if !strings.HasPrefix(secret, "whsec_") || len(secret) != len("whsec_")+43 {
		t.Fatalf("secret = %q, want whsec_ + 43 base64url characters", secret)
	}
	if NewSecret() == secret {
		t.Fatal("secrets repeat")
	}

	ct, kid, err := k.Encrypt([]byte(secret), endpoint, project)
	if err != nil || kid != "k1" {
		t.Fatalf("encrypt = %s, %v", kid, err)
	}
	if bytes.Contains(ct, []byte(secret)) || len(ct) != nonceSize+len(secret)+16 {
		t.Fatalf("ciphertext layout wrong or plaintext visible (%d bytes)", len(ct))
	}
	again, _, _ := k.Encrypt([]byte(secret), endpoint, project)
	if bytes.Equal(again, ct) {
		t.Fatal("nonce reused: equal ciphertexts")
	}
	pt, err := k.Decrypt(ct, kid, endpoint, project)
	if err != nil || string(pt) != secret {
		t.Fatalf("decrypt = %q, %v", pt, err)
	}

	tampered := bytes.Clone(ct)
	tampered[len(tampered)-1] ^= 1
	for name, fn := range map[string]func() ([]byte, error){
		"other endpoint": func() ([]byte, error) { return k.Decrypt(ct, kid, uuid.New(), project) },
		"other project":  func() ([]byte, error) { return k.Decrypt(ct, kid, endpoint, uuid.New()) },
		"unknown key id": func() ([]byte, error) { return k.Decrypt(ct, "k9", endpoint, project) },
		"tampered":       func() ([]byte, error) { return k.Decrypt(tampered, kid, endpoint, project) },
		"truncated":      func() ([]byte, error) { return k.Decrypt(ct[:nonceSize], kid, endpoint, project) },
	} {
		if _, err := fn(); err == nil {
			t.Errorf("%s: decrypted", name)
		}
	}
}

// Secrets encrypted with an older listed key still decrypt after rotation;
// new secrets use the new active key.
func TestKeyRotation(t *testing.T) {
	old, _ := ParseKeyring("k1:" + key(1))
	endpoint, project := uuid.New(), uuid.New()
	ct, kid, err := old.Encrypt([]byte("whsec_x"), endpoint, project)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := ParseKeyring("k2:" + key(2) + ",k1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := rotated.Decrypt(ct, kid, endpoint, project); err != nil || string(pt) != "whsec_x" {
		t.Fatalf("old ciphertext after rotation = %q, %v", pt, err)
	}
	if _, kid, _ := rotated.Encrypt([]byte("whsec_y"), endpoint, project); kid != "k2" {
		t.Fatalf("new key id = %s, want k2", kid)
	}
	// A key with the same id but other bytes cannot decrypt.
	wrong, _ := ParseKeyring("k1:" + key(3))
	if _, err := wrong.Decrypt(ct, kid, endpoint, project); err == nil {
		t.Fatal("decrypted with the wrong key bytes")
	}
}
