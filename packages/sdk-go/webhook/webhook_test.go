package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	emailplatform "github.com/skraus2/email-platform/packages/sdk-go"
)

// Published test vector of the platform's signer (computed independently
// with Python's hmac module); the same constants as the server's tests.
const (
	secret    = "whsec_7Xq2t9Lm4Rk8Vp1Wn6Ys3Zb5Hc0Jd2Fg4Ae6Ui8Oo"
	timestamp = "1760428800"
	eventID   = "0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0c"
	body      = `{"id":"` + eventID + `","type":"email.delivered","created_at":"2026-10-14T08:00:00Z","data":{"email_id":"0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b","project_id":"0199e39f-0000-7000-8000-000000000001"}}`
	signature = "v1=a3828bc3f00cff5d3a0d2e5d9e16472c13b89cdfabf73dbbca8de569f559a7a4"
	mac       = "a3828bc3f00cff5d3a0d2e5d9e16472c13b89cdfabf73dbbca8de569f559a7a4"
)

var (
	signedAt = time.Unix(1760428800, 0)
	now      = signedAt.Add(2 * time.Minute)
)

func sign(t *testing.T, payload, ts, key string) string {
	t.Helper()
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(ts + "." + payload))
	return "v1=" + hex.EncodeToString(h.Sum(nil))
}

// headers returns the vector's headers with overrides; a nil value removes
// the header.
func headers(overrides map[string][]string) http.Header {
	h := http.Header{
		"Webhook-Id":        {eventID},
		"Webhook-Timestamp": {timestamp},
		"Webhook-Signature": {signature},
	}
	for k, v := range overrides {
		if v == nil {
			delete(h, k)
			continue
		}
		h[k] = v
	}
	return h
}

func TestTheSignerProducesTheVector(t *testing.T) {
	if got := sign(t, body, timestamp, secret); got != signature {
		t.Fatalf("test signer = %s, want the vector", got)
	}
}

func TestThePublishedVectorVerifies(t *testing.T) {
	for name, at := range map[string]time.Time{
		"2 min later":       now,
		"exactly 5 min ago": signedAt.Add(5 * time.Minute),
		"exactly 5 min on":  signedAt.Add(-5 * time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			v, err := Verify(secret, headers(nil), []byte(body), at)
			if err != nil {
				t.Fatal(err)
			}
			e := v.Event
			if v.ID != eventID || e.Id != eventID || e.Type != emailplatform.EmailDelivered ||
				!e.CreatedAt.Equal(time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)) ||
				e.Data.EmailId != "0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b" ||
				e.Data.ProjectId != "0199e39f-0000-7000-8000-000000000001" {
				t.Fatalf("%+v", v)
			}
		})
	}
}

func TestHeaderNamesInAnyCase(t *testing.T) {
	for name, h := range map[string]http.Header{
		"lower case": {"webhook-id": {eventID}, "webhook-timestamp": {timestamp}, "webhook-signature": {signature}},
		"mixed case": {"WEBHOOK-ID": {eventID}, "webhook-Timestamp": {timestamp}, "Webhook-signature": {signature}},
	} {
		if _, err := Verify(secret, h, []byte(body), now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Built the way net/http builds request headers.
	h := http.Header{}
	h.Set("webhook-id", eventID)
	h.Set("WEBHOOK-TIMESTAMP", timestamp)
	h.Add("webhook-signature", signature)
	if _, err := Verify(secret, h, []byte(body), now); err != nil {
		t.Errorf("canonical: %v", err)
	}
}

func TestForwardCompatibility(t *testing.T) {
	other := "v1=" + strings.Repeat("0", 64)
	for name, sig := range map[string]string{
		"valid first":              signature + " " + other,
		"valid last":               other + " " + other + " " + signature,
		"v2 before v1":             "v2=" + mac + " " + signature,
		"spaces and tabs":          "  " + other + " \t\t" + signature + "\t ",
		"eight values at most":     strings.Repeat(other+" ", 7) + signature,
		"whitespace up to the cap": strings.Repeat(" \t", (1024-len(signature))/2) + signature,
	} {
		if _, err := Verify(secret, headers(map[string][]string{"Webhook-Signature": {sig}}), []byte(body), now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	future := `{"id":"` + eventID + `","type":"contact.updated","created_at":"2027-01-01T00:00:00Z","data":{"contact_id":"c1","email_id":"e1"},"extra":[1,2]}`
	v, err := Verify(secret, headers(map[string][]string{"Webhook-Signature": {sign(t, future, timestamp, secret)}}), []byte(future), now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Event.Type != "contact.updated" || v.Event.Type.Valid() || v.Event.Data.EmailId != "e1" {
		t.Fatalf("%+v", v)
	}
}

type rejection struct {
	name   string
	secret string
	header http.Header
	body   string
	now    time.Time
	opts   []Option
	reason Reason
	skew   Skew
	// Kept as given; otherwise a zero value takes the valid request's.
	keepHeader, keepBody, keepNow bool
}

func rejections(t *testing.T) []rejection {
	t.Helper()
	signed := func(payload string) http.Header {
		return headers(map[string][]string{"Webhook-Signature": {sign(t, payload, timestamp, secret)}})
	}
	sig := func(s string) http.Header { return headers(map[string][]string{"Webhook-Signature": {s}}) }
	ts := func(s string) http.Header { return headers(map[string][]string{"Webhook-Timestamp": {s}}) }
	other := "v1=" + strings.Repeat("0", 64)
	cases := []rejection{
		// The secret is checked before anything else.
		{name: "empty secret", secret: "", reason: ReasonMissingSecret},
		{name: "prefix only", secret: "whsec_", reason: ReasonMissingSecret},
		{name: "no prefix", secret: strings.TrimPrefix(secret, "whsec_"), reason: ReasonMissingSecret},
		{name: "bad secret first", secret: "", keepHeader: true, keepNow: true, opts: []Option{WithTolerance(-1)}, reason: ReasonMissingSecret},

		{name: "nil header", keepHeader: true, reason: ReasonMissingHeader},
		{name: "no id", header: headers(map[string][]string{"Webhook-Id": nil}), reason: ReasonMissingHeader},
		{name: "no timestamp", header: headers(map[string][]string{"Webhook-Timestamp": nil}), reason: ReasonMissingHeader},
		{name: "no signature", header: headers(map[string][]string{"Webhook-Signature": nil}), reason: ReasonMissingHeader},
		{name: "empty id", header: headers(map[string][]string{"Webhook-Id": {""}}), reason: ReasonMissingHeader},
		{name: "empty timestamp", header: ts(""), reason: ReasonMissingHeader},
		{name: "empty signature", header: sig(""), reason: ReasonMissingHeader},
		{name: "empty id slice", header: headers(map[string][]string{"Webhook-Id": {}}), reason: ReasonMissingHeader},
		{name: "empty timestamp slice", header: headers(map[string][]string{"Webhook-Timestamp": {}}), reason: ReasonMissingHeader},
		{name: "empty signature slice", header: headers(map[string][]string{"Webhook-Signature": {}}), reason: ReasonMissingHeader},
		{name: "joined id without signature", header: headers(map[string][]string{"Webhook-Id": {"a,b"}, "Webhook-Signature": nil}), reason: ReasonMissingHeader},

		{name: "two ids", header: headers(map[string][]string{"Webhook-Id": {eventID, eventID}}), reason: ReasonBadHeader},
		{name: "two timestamps", header: headers(map[string][]string{"Webhook-Timestamp": {timestamp, timestamp}}), reason: ReasonBadHeader},
		{name: "two signatures", header: headers(map[string][]string{"Webhook-Signature": {signature, signature}}), reason: ReasonBadHeader},
		{name: "id under two cases", header: headers(map[string][]string{"webhook-id": {eventID}}), reason: ReasonBadHeader},
		{name: "signature under two cases", header: headers(map[string][]string{"WEBHOOK-SIGNATURE": {signature}}), reason: ReasonBadHeader},
		{name: "joined id", header: headers(map[string][]string{"Webhook-Id": {eventID + ", " + eventID}}), reason: ReasonBadHeader},
		{name: "joined timestamp", header: ts(timestamp + ", " + timestamp), reason: ReasonBadHeader},

		{name: "plus sign", header: ts("+" + timestamp), reason: ReasonBadTimestamp},
		{name: "leading space", header: ts(" " + timestamp), reason: ReasonBadTimestamp},
		{name: "trailing space", header: ts(timestamp + " "), reason: ReasonBadTimestamp},
		{name: "decimal", header: ts(timestamp + ".0"), reason: ReasonBadTimestamp},
		{name: "negative", header: ts("-1"), reason: ReasonBadTimestamp},
		{name: "word", header: ts("soon"), reason: ReasonBadTimestamp},
		{name: "hex", header: ts("0x68ed"), reason: ReasonBadTimestamp},
		{name: "13 digits", header: ts("1760428800000"), reason: ReasonBadTimestamp},
		{name: "1 s too new", now: signedAt.Add(-5*time.Minute - time.Second), reason: ReasonBadTimestamp, skew: SkewFuture},
		{name: "1 s too old", now: signedAt.Add(5*time.Minute + time.Second), reason: ReasonBadTimestamp, skew: SkewPast},
		{name: "sub-second too old", now: signedAt.Add(5*time.Minute + 900*time.Millisecond), reason: ReasonBadTimestamp, skew: SkewPast},
		{name: "far future", header: ts("999999999999"), reason: ReasonBadTimestamp, skew: SkewFuture},
		{name: "older than the epoch window", header: ts("0"), reason: ReasonBadTimestamp, skew: SkewPast},
		{name: "1 h tolerance still bounded", now: signedAt.Add(time.Hour + time.Second), opts: []Option{WithTolerance(time.Hour)}, reason: ReasonBadTimestamp, skew: SkewPast},

		{name: "other timestamp", header: ts("1760428801"), reason: ReasonSignatureMismatch},
		{name: "tampered body", body: strings.Replace(body, "delivered", "bounced", 1), reason: ReasonSignatureMismatch},
		{name: "wrong secret", secret: secret + "x", reason: ReasonSignatureMismatch},
		{name: "only v2", header: sig("v2=" + mac), reason: ReasonSignatureMismatch},
		{name: "63 hex", header: sig(signature[:len(signature)-1]), reason: ReasonSignatureMismatch},
		{name: "65 hex", header: sig(signature + "0"), reason: ReasonSignatureMismatch},
		{name: "not hex", header: sig("v1=" + strings.Repeat("z", 64)), reason: ReasonSignatureMismatch},
		{name: "upper-case hex", header: sig("v1=" + strings.ToUpper(mac)), reason: ReasonSignatureMismatch},
		{name: "nine values", header: sig(strings.Repeat(other+" ", 8) + signature), reason: ReasonSignatureMismatch},
		{name: "over 1024 bytes", header: sig(strings.Repeat(" ", 1025-len(signature)) + signature), reason: ReasonSignatureMismatch},
		{name: "64 KiB whitespace", header: sig(strings.Repeat(" \t", 32*1024) + signature), reason: ReasonSignatureMismatch},
		{name: "invalid JSON, wrong signature", body: "{", reason: ReasonSignatureMismatch},

		{name: "invalid UTF-8", body: "{\"id\":\"" + eventID + "\xff\",\"type\":\"email.delivered\"}", reason: ReasonBadBody},
		{name: "not JSON", body: "{", reason: ReasonBadBody},
		{name: "array", body: "[]", reason: ReasonBadBody},
		{name: "null", body: "null", reason: ReasonBadBody},
		{name: "empty", keepBody: true, reason: ReasonBadBody},
		{name: "byte order mark", body: "\xef\xbb\xbf" + body, reason: ReasonBadBody},
		{name: "id absent", body: `{"type":"email.delivered"}`, reason: ReasonBadBody},
		{name: "id null", body: `{"id":null,"type":"email.delivered"}`, reason: ReasonBadBody},
		{name: "id number", body: `{"id":1,"type":"email.delivered"}`, reason: ReasonBadBody},
		{name: "id empty", body: `{"id":"","type":"email.delivered"}`, reason: ReasonBadBody},
		{name: "type absent", body: `{"id":"` + eventID + `"}`, reason: ReasonBadBody},
		{name: "type number", body: `{"id":"` + eventID + `","type":1}`, reason: ReasonBadBody},
		{name: "type empty", body: `{"id":"` + eventID + `","type":""}`, reason: ReasonBadBody},
		{name: "created_at not a time", body: `{"id":"` + eventID + `","type":"email.delivered","created_at":"yesterday"}`, reason: ReasonBadBody},
		{name: "data a string", body: `{"id":"` + eventID + `","type":"email.delivered","data":"x"}`, reason: ReasonBadBody},

		{name: "header id differs from the signed id", body: strings.Replace(body, eventID, "0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0d", 1), reason: ReasonIDMismatch},
	}
	for i := range cases {
		c := &cases[i]
		if c.secret == "" && c.reason != ReasonMissingSecret {
			c.secret = secret
		}
		if c.body == "" && !c.keepBody {
			c.body = body
		}
		if c.now.IsZero() && !c.keepNow {
			c.now = now
		}
		if c.header == nil && !c.keepHeader {
			// Bodies that differ from the vector are signed with the vector
			// secret, so the MAC matches and the parse is what fails.
			if c.reason == ReasonBadBody || c.reason == ReasonIDMismatch {
				c.header = signed(c.body)
			} else {
				c.header = headers(nil)
			}
		}
	}
	return cases
}

func TestRejections(t *testing.T) {
	for _, c := range rejections(t) {
		t.Run(c.name, func(t *testing.T) {
			_, err := Verify(c.secret, c.header, []byte(c.body), c.now, c.opts...)
			e, ok := errors.AsType[*Error](err)
			if !ok {
				t.Fatalf("err = %v, want *Error", err)
			}
			if e.Reason != c.reason || e.Skew != c.skew {
				t.Fatalf("got %s/%q, want %s/%q", e.Reason, e.Skew, c.reason, c.skew)
			}
			if errors.Is(err, emailplatform.ErrInvalidArgument) {
				t.Fatal("a request rejection must not be an argument error")
			}
		})
	}
}

// The length cap comes before the split, so a huge header costs nothing.
func TestLongSignatureRejectedBeforeSplit(t *testing.T) {
	h := headers(map[string][]string{"Webhook-Signature": {strings.Repeat(" ", 1<<20)}})
	start := time.Now()
	for range 100 {
		if _, err := Verify(secret, h, []byte(body), now); err == nil {
			t.Fatal("accepted")
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("100 rejections of a 1 MiB header took %s", d)
	}
}

func TestErrorsLeakNothing(t *testing.T) {
	for _, c := range rejections(t) {
		_, err := Verify(c.secret, c.header, []byte(c.body), c.now, c.opts...)
		h := hmac.New(sha256.New, []byte(c.secret))
		h.Write([]byte(strings.Join(c.header.Values("Webhook-Timestamp"), "") + "." + c.body))
		needles := []string{secret, mac, hex.EncodeToString(h.Sum(nil))}
		if c.secret != "" {
			needles = append(needles, c.secret)
		}
		if len(c.body) > 2 {
			needles = append(needles, c.body)
		}
		for _, v := range c.header.Values("Webhook-Signature") {
			if strings.TrimSpace(v) != "" {
				needles = append(needles, strings.TrimSpace(v))
			}
		}
		out := fmt.Sprintf("%v|%+v|%#v|%s", err, err, err, err.Error())
		for _, n := range needles {
			if strings.Contains(out, n) {
				t.Errorf("%s: error shows %q: %s", c.name, n, out)
			}
		}
		want := "webhook verification failed: " + string(c.reason)
		if c.skew != "" {
			want += " (timestamp in the " + string(c.skew) + ")"
		}
		if err.Error() != want {
			t.Errorf("%s: message %q, want %q", c.name, err.Error(), want)
		}
	}
}

func TestOptions(t *testing.T) {
	for name, opts := range map[string][]Option{
		"zero tolerance":     {WithTolerance(0)},
		"negative tolerance": {WithTolerance(-time.Minute)},
		"over 1 h":           {WithTolerance(time.Hour + time.Nanosecond)},
	} {
		_, err := Verify(secret, nil, nil, now, opts...)
		if _, isRejection := errors.AsType[*Error](err); !errors.Is(err, emailplatform.ErrInvalidArgument) || isRejection {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && err.Error() != "webhook: tolerance must be in (0, 1h]: emailplatform: invalid argument" {
			t.Errorf("%s: message %q", name, err)
		}
	}
	_, err := Verify(secret, nil, nil, time.Time{})
	if _, isRejection := errors.AsType[*Error](err); !errors.Is(err, emailplatform.ErrInvalidArgument) || isRejection ||
		err.Error() != "webhook: now must be set: emailplatform: invalid argument" {
		t.Errorf("zero now: %v", err)
	}

	late := signedAt.Add(59 * time.Minute)
	if _, err := Verify(secret, headers(nil), []byte(body), late); err == nil {
		t.Fatal("59 min late accepted with the default tolerance")
	}
	for name, opts := range map[string][]Option{
		"1 h":         {WithTolerance(time.Hour)},
		"nil skipped": {nil, WithTolerance(time.Hour), nil},
	} {
		if _, err := Verify(secret, headers(nil), []byte(body), late, opts...); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Verify(secret, headers(nil), []byte(body), signedAt.Add(time.Hour), WithTolerance(time.Hour)); err != nil {
		t.Errorf("exactly 1 h: %v", err)
	}
}
