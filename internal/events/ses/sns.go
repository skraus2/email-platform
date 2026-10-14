// Package ses authenticates and parses Amazon SES event notifications that
// arrive through an SNS topic in an SQS queue.
package ses

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Rejection reasons. All except ErrCertFetch are final: the message can
// never become valid. ErrCertFetch is transient (the certificate could not be
// loaded) and the message should be retried later.
var (
	ErrMalformed        = errors.New("sns: malformed message")
	ErrNotNotification  = errors.New("sns: not a notification")
	ErrSignatureVersion = errors.New("sns: unsupported signature version")
	ErrWrongTopic       = errors.New("sns: unexpected topic")
	ErrCertURL          = errors.New("sns: signing certificate URL not allowed")
	ErrCertificate      = errors.New("sns: signing certificate not acceptable")
	ErrCertFetch        = errors.New("sns: signing certificate could not be fetched")
	ErrBadSignature     = errors.New("sns: signature does not verify")
)

// Fetcher loads a signing certificate (PEM) from an already checked URL.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// Verifier checks SNS notifications of one topic in one region.
type Verifier struct {
	region   string
	topicARN string
	fetch    Fetcher

	mu    sync.Mutex
	certs map[string]*x509.Certificate
}

const (
	maxEnvelope  = 256 << 10 // SQS message size limit
	maxCertBytes = 16 << 10
	maxCached    = 16
	certTimeout  = 5 * time.Second
	certName     = "sns.amazonaws.com"
)

// NewVerifier builds a Verifier. fetch nil means fetching over HTTPS from the
// pinned SNS host; tests pass their own.
func NewVerifier(region, topicARN string, fetch Fetcher) *Verifier {
	v := &Verifier{region: region, topicARN: topicARN, fetch: fetch, certs: map[string]*x509.Certificate{}}
	if v.fetch == nil {
		v.fetch = v.fetchHTTPS
	}
	return v
}

type envelope struct {
	Type             string
	MessageID        string `json:"MessageId"`
	TopicArn         string
	Subject          *string
	Message          *string
	Timestamp        string
	SignatureVersion string
	Signature        string
	SigningCertURL   string `json:"SigningCertURL"`
}

// Verify authenticates an SNS notification and returns its message.
func (v *Verifier) Verify(ctx context.Context, body []byte) (string, error) {
	if len(body) > maxEnvelope {
		return "", ErrMalformed
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || env.Message == nil || env.MessageID == "" || env.Timestamp == "" || env.Signature == "" {
		return "", ErrMalformed
	}
	switch {
	case env.Type != "Notification":
		return "", ErrNotNotification
	case env.SignatureVersion != "2":
		return "", ErrSignatureVersion
	case env.TopicArn != v.topicARN:
		return "", ErrWrongTopic
	case !v.allowedCertURL(env.SigningCertURL):
		return "", ErrCertURL
	}
	cert, err := v.certificate(ctx, env.SigningCertURL)
	if err != nil {
		return "", err
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return "", ErrCertificate
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return "", ErrBadSignature
	}
	fields := map[string]string{
		"Message": *env.Message, "MessageId": env.MessageID, "Timestamp": env.Timestamp,
		"TopicArn": env.TopicArn, "Type": env.Type,
	}
	if env.Subject != nil {
		fields["Subject"] = *env.Subject
	}
	sum := sha256.Sum256([]byte(stringToSign(fields)))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return "", ErrBadSignature
	}
	return *env.Message, nil
}

// stringToSign is the canonical form SNS signs for a Notification: the
// fields in this order, each as "Name\nValue\n", Subject only if present.
func stringToSign(f map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"Message", "MessageId", "Subject", "Timestamp", "TopicArn", "Type"} {
		v, ok := f[k]
		if !ok {
			continue
		}
		b.WriteString(k + "\n" + v + "\n")
	}
	return b.String()
}

// allowedCertURL accepts only https://sns.<region>.amazonaws.com/….pem (or
// the .com.cn domain for China regions), without userinfo or port.
func (v *Verifier) allowedCertURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	hosts := []string{"sns." + v.region + ".amazonaws.com", "sns." + v.region + ".amazonaws.com.cn"}
	return slices.Contains(hosts, u.Host) && strings.HasSuffix(u.Path, ".pem")
}

func (v *Verifier) certificate(ctx context.Context, certURL string) (*x509.Certificate, error) {
	v.mu.Lock()
	cert := v.certs[certURL]
	v.mu.Unlock()
	if cert == nil {
		raw, err := v.fetch(ctx, certURL)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrCertFetch, err)
		}
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, ErrCertificate
		}
		if cert, err = x509.ParseCertificate(block.Bytes); err != nil {
			return nil, ErrCertificate
		}
		if cert.VerifyHostname(certName) != nil {
			return nil, ErrCertificate
		}
		v.mu.Lock()
		if len(v.certs) >= maxCached {
			clear(v.certs)
		}
		v.certs[certURL] = cert
		v.mu.Unlock()
	}
	// Checked on every use, so a cached certificate expires too.
	if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return nil, ErrCertificate
	}
	return cert, nil
}

// fetchHTTPS loads the certificate from the pinned SNS host. The TLS
// connection to that host (system roots) is what makes the leaf certificate
// trustworthy; the PEM itself carries no chain.
func (v *Verifier) fetchHTTPS(ctx context.Context, certURL string) ([]byte, error) {
	if !v.allowedCertURL(certURL) {
		return nil, ErrCertURL
	}
	ctx, cancel := context.WithTimeout(ctx, certTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout:       certTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCertBytes+1))
	if err != nil {
		return nil, errors.New("read failed")
	}
	if len(raw) > maxCertBytes {
		return nil, errors.New("certificate too large")
	}
	return raw, nil
}
