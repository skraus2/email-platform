package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Header names of a webhook request.
const (
	HeaderID        = "Webhook-Id"
	HeaderTimestamp = "Webhook-Timestamp"
	HeaderSignature = "Webhook-Signature"
)

// DefaultTolerance is how far a receiver accepts the signed timestamp from
// its own clock.
const DefaultTolerance = 5 * time.Minute

type payloadData struct {
	EmailID   string `json:"email_id"`
	ProjectID string `json:"project_id"`
}

type payload struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	CreatedAt string      `json:"created_at"`
	Data      payloadData `json:"data"`
}

// Payload builds the request body for an event. The same inputs always give
// the same bytes, so a retry repeats the identical body.
func Payload(eventID uuid.UUID, eventType string, receivedAt time.Time, emailID, projectID uuid.UUID) ([]byte, error) {
	b, _ := json.Marshal(payload{
		ID: eventID.String(), Type: "email." + eventType,
		CreatedAt: receivedAt.UTC().Format(time.RFC3339Nano),
		Data:      payloadData{EmailID: emailID.String(), ProjectID: projectID.String()},
	})
	return b, nil
}

func mac(secret string, timestamp string, body []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(timestamp))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// Sign returns the Webhook-Signature value: "v1=" and the hex HMAC-SHA256,
// keyed with the full secret string, of "<timestamp>.<body>".
func Sign(secret string, timestamp int64, body []byte) string {
	return "v1=" + hex.EncodeToString(mac(secret, strconv.FormatInt(timestamp, 10), body))
}

// ErrInvalidSignature means a webhook request must be rejected.
var ErrInvalidSignature = errors.New("webhook signature invalid")

// Verify is the receiver-side check: the timestamp lies within tolerance of
// now and one of the v1 values in the signature header matches. Receivers
// should additionally drop repeated Webhook-Id values.
func Verify(secret, timestamp, signature string, body []byte, tolerance time.Duration, now time.Time) error {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || secret == "" {
		return ErrInvalidSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrInvalidSignature
	}
	want := mac(secret, timestamp, body)
	for part := range strings.FieldsSeq(signature) {
		v, ok := strings.CutPrefix(part, "v1=")
		if !ok {
			continue
		}
		got, err := hex.DecodeString(v)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrInvalidSignature
}
