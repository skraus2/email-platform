package webhooks

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Published test vector (computed independently with Python's hmac module).
const (
	vectorSecret    = "whsec_7Xq2t9Lm4Rk8Vp1Wn6Ys3Zb5Hc0Jd2Fg4Ae6Ui8Oo"
	vectorTimestamp = int64(1760428800)
	vectorBody      = `{"id":"0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0c","type":"email.delivered","created_at":"2026-10-14T08:00:00Z","data":{"email_id":"0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b","project_id":"0199e39f-0000-7000-8000-000000000001"}}`
	vectorSignature = "v1=a3828bc3f00cff5d3a0d2e5d9e16472c13b89cdfabf73dbbca8de569f559a7a4"
)

func TestSignatureVector(t *testing.T) {
	if got := Sign(vectorSecret, vectorTimestamp, []byte(vectorBody)); got != vectorSignature {
		t.Fatalf("Sign = %s, want %s", got, vectorSignature)
	}
	body, err := Payload(
		uuid.MustParse("0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0c"), "delivered",
		time.Date(2026, 10, 14, 10, 0, 0, 0, time.FixedZone("CEST", 2*3600)),
		uuid.MustParse("0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b"), uuid.MustParse("0199e39f-0000-7000-8000-000000000001"),
	)
	if err != nil || string(body) != vectorBody {
		t.Fatalf("Payload = %s, %v; want the vector body", body, err)
	}
}

func TestVerify(t *testing.T) {
	now := time.Unix(vectorTimestamp, 0).Add(2 * time.Minute)
	ts := "1760428800"
	ok := func(sig, ts, body, secret string, at time.Time) error {
		return Verify(secret, ts, sig, []byte(body), DefaultTolerance, at)
	}
	if err := ok(vectorSignature, ts, vectorBody, vectorSecret, now); err != nil {
		t.Fatalf("valid: %v", err)
	}
	other := "v1=" + strings.Repeat("0", 64)
	if err := ok(other+" "+vectorSignature, ts, vectorBody, vectorSecret, now); err != nil {
		t.Fatalf("one of several values valid: %v", err)
	}
	if err := ok("v2=abc "+vectorSignature, ts, vectorBody, vectorSecret, now); err != nil {
		t.Fatalf("unknown versions are ignored: %v", err)
	}
	for name, err := range map[string]error{
		"tampered body":   ok(vectorSignature, ts, strings.Replace(vectorBody, "delivered", "bounced", 1), vectorSecret, now),
		"wrong secret":    ok(vectorSignature, ts, vectorBody, vectorSecret+"x", now),
		"other timestamp": ok(vectorSignature, "1760428801", vectorBody, vectorSecret, now),
		"stale":           ok(vectorSignature, ts, vectorBody, vectorSecret, now.Add(10*time.Minute)),
		"future":          ok(vectorSignature, ts, vectorBody, vectorSecret, time.Unix(vectorTimestamp, 0).Add(-6*time.Minute)),
		"bad timestamp":   ok(vectorSignature, "soon", vectorBody, vectorSecret, now),
		"missing":         ok("", ts, vectorBody, vectorSecret, now),
		"only v2":         ok("v2="+strings.TrimPrefix(vectorSignature, "v1="), ts, vectorBody, vectorSecret, now),
		"not hex":         ok("v1=zz", ts, vectorBody, vectorSecret, now),
	} {
		if !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s: err = %v, want ErrInvalidSignature", name, err)
		}
	}
}
