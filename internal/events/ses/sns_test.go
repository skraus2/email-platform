package ses

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	region   = "eu-central-1"
	topicARN = "arn:aws:sns:eu-central-1:123456789012:ses-events"
	certURL  = "https://sns.eu-central-1.amazonaws.com/SimpleNotificationService-test.pem"
)

type signer struct {
	key     *rsa.PrivateKey
	certPEM []byte
}

func newSigner(t *testing.T, name string, notBefore, notAfter time.Time) *signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: notBefore, NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func validSigner(t *testing.T) *signer {
	t.Helper()
	return newSigner(t, "sns.amazonaws.com", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

// envelope builds a signed SNS notification; mutate runs before signing,
// tamper after.
func (s *signer) envelope(t *testing.T, message string, mutate, tamper func(map[string]string)) []byte {
	t.Helper()
	env := map[string]string{
		"Type": "Notification", "MessageId": "6b4a6b7c-0000-4000-8000-000000000001", "TopicArn": topicARN,
		"Message": message, "Timestamp": "2026-10-14T08:00:00.000Z", "SignatureVersion": "2",
		"SigningCertURL": certURL, "UnsubscribeURL": "https://sns.eu-central-1.amazonaws.com/?Action=Unsubscribe",
	}
	if mutate != nil {
		mutate(env)
	}
	sum := sha256.Sum256([]byte(stringToSign(env)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	env["Signature"] = base64.StdEncoding.EncodeToString(sig)
	if tamper != nil {
		tamper(env)
	}
	b, _ := json.Marshal(env)
	return b
}

func (s *signer) verifier(fetches *atomic.Int32) *Verifier {
	v, err := newVerifier(region, topicARN, func(_ context.Context, url string) ([]byte, error) {
		if fetches != nil {
			fetches.Add(1)
		}
		if url != certURL {
			return nil, errors.New("unexpected certificate URL " + url)
		}
		return s.certPEM, nil
	})
	if err != nil {
		panic(err)
	}
	return v
}

// The signed string, written out by hand from the documented SNS format for
// Notification messages: fields in this order, each "Name\nValue\n",
// Subject only when present.
func TestStringToSignFormat(t *testing.T) {
	base := map[string]string{
		"Type": "Notification", "MessageId": "m-1", "TopicArn": "arn:t", "Message": "hello\nworld",
		"Timestamp": "2026-10-14T08:00:00.000Z", "SignatureVersion": "2", "SigningCertURL": "https://x/y.pem",
	}
	want := "Message\nhello\nworld\nMessageId\nm-1\nTimestamp\n2026-10-14T08:00:00.000Z\nTopicArn\narn:t\nType\nNotification\n"
	if got := stringToSign(base); got != want {
		t.Fatalf("without subject:\n%q\nwant\n%q", got, want)
	}
	base["Subject"] = "Amazon SES"
	want = "Message\nhello\nworld\nMessageId\nm-1\nSubject\nAmazon SES\nTimestamp\n2026-10-14T08:00:00.000Z\nTopicArn\narn:t\nType\nNotification\n"
	if got := stringToSign(base); got != want {
		t.Fatalf("with subject:\n%q\nwant\n%q", got, want)
	}
}

func TestNewVerifierValidatesConfig(t *testing.T) {
	if _, err := NewVerifier(region, topicARN); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string][2]string{
		"empty region":          {"", topicARN},
		"bad region":            {"EU-central-1", topicARN},
		"topic in other region": {"us-east-1", topicARN},
		"not a topic":           {region, "arn:aws:sqs:eu-central-1:123456789012:q"},
		"short account":         {region, "arn:aws:sns:eu-central-1:1234:t"},
	} {
		if _, err := NewVerifier(tc[0], tc[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVerifyAcceptsValidNotification(t *testing.T) {
	s := validSigner(t)
	var fetches atomic.Int32
	v := s.verifier(&fetches)
	for range 3 {
		msg, err := v.Verify(context.Background(), s.envelope(t, `{"eventType":"Delivery"}`, nil, nil))
		if err != nil || msg != `{"eventType":"Delivery"}` {
			t.Fatalf("verify = %q, %v", msg, err)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("certificate fetched %d times, want once (cached)", fetches.Load())
	}
	// With a Subject the string to sign includes it.
	withSubject := s.envelope(t, "{}", func(e map[string]string) { e["Subject"] = "Amazon SES Email Event Notification" }, nil)
	if _, err := v.Verify(context.Background(), withSubject); err != nil {
		t.Fatalf("with subject: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	s := validSigner(t)
	other := validSigner(t)
	for name, tc := range map[string]struct {
		body []byte
		v    *Verifier
		want error
	}{
		"tampered message": {s.envelope(t, `{"eventType":"Delivery"}`, nil, func(e map[string]string) { e["Message"] = `{"eventType":"Bounce"}` }), s.verifier(nil), ErrBadSignature},
		"tampered subject": {s.envelope(t, "{}", nil, func(e map[string]string) { e["Subject"] = "x" }), s.verifier(nil), ErrBadSignature},
		"other signer":     {other.envelope(t, "{}", nil, nil), s.verifier(nil), ErrBadSignature},
		"bad base64":       {s.envelope(t, "{}", nil, func(e map[string]string) { e["Signature"] = "!!" }), s.verifier(nil), ErrBadSignature},
		"wrong topic": {s.envelope(t, "{}", func(e map[string]string) {
			e["TopicArn"] = "arn:aws:sns:eu-central-1:123456789012:other"
		}, nil), s.verifier(nil), ErrWrongTopic},
		"signature version 1": {s.envelope(t, "{}", func(e map[string]string) { e["SignatureVersion"] = "1" }, nil), s.verifier(nil), ErrSignatureVersion},
		"foreign cert host": {s.envelope(t, "{}", func(e map[string]string) {
			e["SigningCertURL"] = "https://sns.eu-central-1.amazonaws.com.evil.test/x.pem"
		}, nil), s.verifier(nil), ErrCertURL},
		"other region": {s.envelope(t, "{}", func(e map[string]string) {
			e["SigningCertURL"] = "https://sns.us-east-1.amazonaws.com/x.pem"
		}, nil), s.verifier(nil), ErrCertURL},
		"http cert url": {s.envelope(t, "{}", func(e map[string]string) {
			e["SigningCertURL"] = strings.Replace(certURL, "https://", "http://", 1)
		}, nil), s.verifier(nil), ErrCertURL},
		"cert url with port": {s.envelope(t, "{}", func(e map[string]string) {
			e["SigningCertURL"] = "https://sns.eu-central-1.amazonaws.com:8443/x.pem"
		}, nil), s.verifier(nil), ErrCertURL},
		"cert url userinfo": {s.envelope(t, "{}", func(e map[string]string) {
			e["SigningCertURL"] = "https://u@sns.eu-central-1.amazonaws.com/x.pem"
		}, nil), s.verifier(nil), ErrCertURL},
		"not a pem path": {s.envelope(t, "{}", func(e map[string]string) {
			e["SigningCertURL"] = "https://sns.eu-central-1.amazonaws.com/x.txt"
		}, nil), s.verifier(nil), ErrCertURL},
		"subscription confirmation": {s.envelope(t, "{}", func(e map[string]string) { e["Type"] = "SubscriptionConfirmation" }, nil), s.verifier(nil), ErrNotNotification},
		"not json":                  {[]byte("not json"), s.verifier(nil), ErrMalformed},
		"missing message":           {s.envelope(t, "", nil, func(e map[string]string) { delete(e, "Message") }), s.verifier(nil), ErrMalformed},
		"message id not uuid":       {s.envelope(t, "{}", func(e map[string]string) { e["MessageId"] = "x\nMessageId\ny" }, nil), s.verifier(nil), ErrMalformed},
		"bad timestamp":             {s.envelope(t, "{}", func(e map[string]string) { e["Timestamp"] = "yesterday" }, nil), s.verifier(nil), ErrMalformed},
		"subject line break":        {s.envelope(t, "{}", func(e map[string]string) { e["Subject"] = "a\nTimestamp" }, nil), s.verifier(nil), ErrMalformed},
		"china host": {s.envelope(t, "{}", func(e map[string]string) {
			e["SigningCertURL"] = "https://sns.eu-central-1.amazonaws.com.cn/x.pem"
		}, nil), s.verifier(nil), ErrCertURL},
	} {
		if _, err := tc.v.Verify(context.Background(), tc.body); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestVerifyRejectsBadCertificates(t *testing.T) {
	for name, s := range map[string]*signer{
		"expired":      newSigner(t, "sns.amazonaws.com", time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)),
		"not yet":      newSigner(t, "sns.amazonaws.com", time.Now().Add(time.Hour), time.Now().Add(48*time.Hour)),
		"another name": newSigner(t, "evil.example.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour)),
	} {
		if _, err := s.verifier(nil).Verify(context.Background(), s.envelope(t, "{}", nil, nil)); !errors.Is(err, ErrCertificate) {
			t.Errorf("%s: err = %v, want ErrCertificate", name, err)
		}
	}
	missing := &Verifier{region: region, topicARN: topicARN, fetch: func(context.Context, string) ([]byte, error) { return nil, errCertMissing }, certs: map[string]*x509.Certificate{}}
	if _, err := missing.Verify(context.Background(), validSigner(t).envelope(t, "{}", nil, nil)); !errors.Is(err, ErrCertificate) {
		t.Errorf("4xx from the pinned host: err = %v, want final ErrCertificate", err)
	}
	garbage := &Verifier{region: region, topicARN: topicARN, fetch: func(context.Context, string) ([]byte, error) { return []byte("no pem"), nil }, certs: map[string]*x509.Certificate{}}
	s := validSigner(t)
	if _, err := garbage.Verify(context.Background(), s.envelope(t, "{}", nil, nil)); !errors.Is(err, ErrCertificate) {
		t.Errorf("garbage certificate: err = %v", err)
	}
	failing := &Verifier{region: region, topicARN: topicARN, fetch: func(context.Context, string) ([]byte, error) { return nil, errors.New("down") }, certs: map[string]*x509.Certificate{}}
	if _, err := failing.Verify(context.Background(), s.envelope(t, "{}", nil, nil)); !errors.Is(err, ErrCertFetch) {
		t.Errorf("fetch failure: err = %v, want ErrCertFetch (retryable)", err)
	}
}

// The real fetcher refuses anything but the pinned host, even when called
// directly, and never contacts AWS in this test.
func TestDefaultFetcherRefusesOtherHosts(t *testing.T) {
	v, err := NewVerifier(region, topicARN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.fetch(context.Background(), "https://example.test/x.pem"); err == nil {
		t.Fatal("fetched from a foreign host")
	}
}
