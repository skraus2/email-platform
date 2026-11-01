// Package webhook verifies the webhook requests the email platform sends to
// an app.
//
// A request is accepted only when its timestamp is within the tolerance of
// the app's clock and one v1= value of Webhook-Signature is the HMAC-SHA256,
// keyed with the endpoint secret, of "<Webhook-Timestamp>.<raw body>". The
// body is parsed only after the signature matched, and the event id to
// deduplicate on is the signed body id, never the unsigned header alone.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	emailplatform "github.com/skraus2/email-platform/packages/sdk-go"
)

// Reason says why a request was rejected.
type Reason string

const (
	// ReasonMissingSecret: no usable whsec_… secret was passed, a
	// configuration fault.
	ReasonMissingSecret Reason = "missing_secret"
	// ReasonBadBody: the signed body is not an event.
	ReasonBadBody Reason = "bad_body"
	// ReasonMissingHeader: Webhook-Id, Webhook-Timestamp or
	// Webhook-Signature is absent or empty.
	ReasonMissingHeader Reason = "missing_header"
	// ReasonBadHeader: a webhook header is repeated or joined.
	ReasonBadHeader Reason = "bad_header"
	// ReasonBadTimestamp: the timestamp is malformed or outside the
	// tolerance (see Skew).
	ReasonBadTimestamp Reason = "bad_timestamp"
	// ReasonSignatureMismatch: no v1= value matches.
	ReasonSignatureMismatch Reason = "signature_mismatch"
	// ReasonIDMismatch: Webhook-Id differs from the signed body id.
	ReasonIDMismatch Reason = "id_mismatch"
)

// Skew says on which side of the app's clock a timestamp outside the
// tolerance lies.
type Skew string

const (
	// SkewPast: the timestamp is older than the tolerance allows.
	SkewPast Skew = "past"
	// SkewFuture: the timestamp is ahead of the app's clock.
	SkewFuture Skew = "future"
)

// Error means the request must be rejected. It carries no input values.
type Error struct {
	Reason Reason
	// Skew is set only for a timestamp outside the tolerance.
	Skew Skew
}

func (e *Error) Error() string {
	if e.Skew == "" {
		return "webhook verification failed: " + string(e.Reason)
	}
	return "webhook verification failed: " + string(e.Reason) + " (timestamp in the " + string(e.Skew) + ")"
}

func reject(r Reason) error { return &Error{Reason: r} }

// Verified is an accepted request.
type Verified struct {
	// ID is the signed event id: deduplicate on it.
	ID string
	// Event is the decoded body. Event types added by later platform
	// versions are passed through, so Type may be a value this version does
	// not know; fields it does not know stay in the raw body.
	Event emailplatform.WebhookEvent
}

// Option changes how Verify checks a request.
type Option func(*options)

type options struct {
	tolerance time.Duration
}

// WithTolerance sets the accepted difference between the request's
// timestamp and now, in both directions (default 5 minutes, at most 1 hour).
func WithTolerance(d time.Duration) Option {
	return func(o *options) { o.tolerance = d }
}

const (
	defaultTolerance   = 5 * time.Minute
	maxTolerance       = time.Hour
	maxSignatureValues = 8
	// Eight v1= values with separators fit with room to spare; anything
	// longer is rejected before it is split.
	maxSignatureLength = 1024
	maxTimestampDigits = 12
	secretPrefix       = "whsec_"
)

// Verify checks a webhook request: secret is the endpoint's signing secret
// (whsec_…) exactly as shown, header the request's headers, body the raw
// request body as received (never re-encoded JSON), now the current time.
// A request to reject gives an *Error; invalid options give an error
// wrapping emailplatform.ErrInvalidArgument.
func Verify(secret string, header http.Header, body []byte, now time.Time, opts ...Option) (Verified, error) {
	if len(secret) <= len(secretPrefix) || !strings.HasPrefix(secret, secretPrefix) {
		return Verified{}, reject(ReasonMissingSecret)
	}
	o := options{tolerance: defaultTolerance}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.tolerance <= 0 || o.tolerance > maxTolerance {
		return Verified{}, fmt.Errorf("webhook: tolerance must be in (0, 1h]: %w", emailplatform.ErrInvalidArgument)
	}
	if now.IsZero() {
		return Verified{}, fmt.Errorf("webhook: now must be set: %w", emailplatform.ErrInvalidArgument)
	}

	id, err := single(header, "Webhook-Id")
	if err != nil {
		return Verified{}, err
	}
	timestamp, err := single(header, "Webhook-Timestamp")
	if err != nil {
		return Verified{}, err
	}
	signature, err := single(header, "Webhook-Signature")
	if err != nil {
		return Verified{}, err
	}
	if strings.Contains(id, ",") || strings.Contains(timestamp, ",") {
		return Verified{}, reject(ReasonBadHeader)
	}

	if err := checkTimestamp(timestamp, now, o.tolerance); err != nil {
		return Verified{}, err
	}
	if !matches(secret, timestamp, signature, body) {
		return Verified{}, reject(ReasonSignatureMismatch)
	}

	var event emailplatform.WebhookEvent
	if !utf8.Valid(body) || json.Unmarshal(body, &event) != nil || event.Id == "" || event.Type == "" {
		return Verified{}, reject(ReasonBadBody)
	}
	if event.Id != id {
		return Verified{}, reject(ReasonIDMismatch)
	}
	return Verified{ID: event.Id, Event: event}, nil
}

// single returns the one value of a header, looked up under every key that
// equals name in any case: a header built by hand need not use canonical
// keys, and two keys differing in case are a repeated header.
func single(header http.Header, name string) (string, error) {
	var values []string
	for key, v := range header {
		if strings.EqualFold(key, name) {
			values = append(values, v...)
		}
	}
	switch {
	case len(values) > 1:
		return "", reject(ReasonBadHeader)
	case len(values) == 0 || values[0] == "":
		return "", reject(ReasonMissingHeader)
	}
	return values[0], nil
}

func checkTimestamp(timestamp string, now time.Time, tolerance time.Duration) error {
	// Digits only: strconv alone would also accept a sign.
	if len(timestamp) > maxTimestampDigits || strings.Trim(timestamp, "0123456789") != "" {
		return reject(ReasonBadTimestamp)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return reject(ReasonBadTimestamp)
	}
	// Sub saturates for far-off values, which then fall outside any
	// tolerance.
	switch age := now.Sub(time.Unix(seconds, 0)); {
	case age > tolerance:
		return &Error{Reason: ReasonBadTimestamp, Skew: SkewPast}
	case age < -tolerance:
		return &Error{Reason: ReasonBadTimestamp, Skew: SkewFuture}
	}
	return nil
}

// matches reports whether one v1= value of the signature header is the MAC
// of the request. Values in other versions are ignored; only the lower-case
// hex the platform emits is accepted.
func matches(secret, timestamp, signature string, body []byte) bool {
	if len(signature) > maxSignatureLength {
		return false
	}
	values := strings.FieldsFunc(signature, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(values) > maxSignatureValues {
		return false
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(timestamp))
	h.Write([]byte{'.'})
	h.Write(body)
	want := h.Sum(nil)
	matched := false
	for _, value := range values {
		v, ok := strings.CutPrefix(value, "v1=")
		if !ok || !lowerHex(v, sha256.Size) {
			continue
		}
		got, err := hex.DecodeString(v)
		if err == nil && hmac.Equal(got, want) {
			matched = true
		}
	}
	return matched
}

func lowerHex(s string, size int) bool {
	if len(s) != 2*size {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
