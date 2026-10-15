package testaws

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
	"testing"
	"time"

	"github.com/google/uuid"
)

// SNSSigner signs SNS Notification envelopes (signature version 2) with a
// throwaway certificate for sns.amazonaws.com, the way SNS delivers them to
// an SQS queue with raw delivery off.
type SNSSigner struct {
	TopicARN string
	CertURL  string

	key     *rsa.PrivateKey
	certPEM []byte
}

// NewSNSSigner creates a signer for topicARN; its certificate is served by
// Fetch under CertURL on the region's pinned SNS host.
func NewSNSSigner(t testing.TB, region, topicARN string) *SNSSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sns.amazonaws.com"},
		DNSNames: []string{"sns.amazonaws.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &SNSSigner{
		TopicARN: topicARN,
		CertURL:  "https://sns." + region + ".amazonaws.com/SimpleNotificationService-local-stub.pem",
		key:      key,
		certPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// Fetch serves the signing certificate for CertURL, without any network.
func (s *SNSSigner) Fetch(_ context.Context, url string) ([]byte, error) {
	if url != s.CertURL {
		return nil, errors.New("testaws: unexpected certificate URL")
	}
	return s.certPEM, nil
}

// Envelope wraps message in a signed Notification envelope.
func (s *SNSSigner) Envelope(t testing.TB, message string) string {
	t.Helper()
	env := map[string]string{
		"Type": "Notification", "MessageId": uuid.NewString(), "TopicArn": s.TopicARN,
		"Message": message, "Timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"SignatureVersion": "2", "SigningCertURL": s.CertURL,
	}
	// The documented canonical form: these fields in this order, each as
	// "Name\nValue\n".
	var b strings.Builder
	for _, k := range []string{"Message", "MessageId", "Timestamp", "TopicArn", "Type"} {
		b.WriteString(k + "\n" + env[k] + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	env["Signature"] = base64.StdEncoding.EncodeToString(sig)
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
