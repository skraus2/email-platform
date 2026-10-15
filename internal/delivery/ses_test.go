package delivery

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/awsconfig"
	"github.com/skraus2/email-platform/internal/testaws"
)

func newTestSES(t *testing.T, endpoint string) *SES {
	t.Helper()
	s, err := NewSES(context.Background(), SESConfig{
		Region: "eu-central-1", ConfigurationSet: "platform-events", Endpoint: endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sesMessage() Message {
	return Message{
		ID: uuid.New(), ProjectID: uuid.New(),
		From: "sender@example.test", To: "customer@example.test",
		Subject: "Hello", Text: "Hi there",
	}
}

func TestSESSendsRawMessageWithTags(t *testing.T) {
	t.Parallel()
	stub := testaws.NewSESv2(t)
	stub.Respond(func(testaws.SendEmail) testaws.Response { return testaws.Response{MessageID: "0102-ses-id"} })
	s := newTestSES(t, stub.URL)
	msg := sesMessage()

	res, err := s.Send(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Provider: "ses", ProviderID: "0102-ses-id", Status: StatusSubmitted}) {
		t.Fatalf("result = %+v", res)
	}
	sends := stub.Sends()
	if len(sends) != 1 {
		t.Fatalf("stub got %d requests, want 1", len(sends))
	}
	got := sends[0]
	if got.ConfigurationSet != "platform-events" || got.From != msg.From || len(got.To) != 1 || got.To[0] != msg.To {
		t.Fatalf("request = %+v", got)
	}
	// Only the platform's own tags: events are correlated through them.
	if got.TagCount != 2 || got.Tags["email_id"] != msg.ID.String() || got.Tags["project_id"] != msg.ProjectID.String() {
		t.Fatalf("tags = %v (%d)", got.Tags, got.TagCount)
	}
	if !strings.Contains(got.Authorization, "Credential="+awsconfig.StubAccessKeyID+"/") {
		t.Fatalf("request not signed with the stub credentials: %q", got.Authorization)
	}
	// Same builder as Mailpit: same Message-ID, fixed headers only.
	want, err := buildMessage(msg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if headerBlock(got.Raw) == "" || !bytes.Contains(got.Raw, []byte("Message-ID: <"+msg.ID.String()+"@email-platform.local>\r\n")) {
		t.Fatalf("raw message lacks the platform Message-ID:\n%s", got.Raw)
	}
	if names(headerBlock(got.Raw)) != names(headerBlock(want)) {
		t.Fatalf("raw headers %q differ from the shared builder's %q", names(headerBlock(got.Raw)), names(headerBlock(want)))
	}
	if strings.Contains(strings.ToLower(headerBlock(got.Raw)), "x-ses-") {
		t.Fatal("raw message carries X-SES headers")
	}
}

func TestSESNoClientInputReachesTagsOrConfigurationSet(t *testing.T) {
	t.Parallel()
	stub := testaws.NewSESv2(t)
	s := newTestSES(t, stub.URL)
	msg := sesMessage()
	msg.Subject = "X-SES-CONFIGURATION-SET: other"
	msg.Text = "X-SES-MESSAGE-TAGS: email_id=forged\r\n"
	if _, err := s.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := stub.Sends()[0]
	if got.ConfigurationSet != "platform-events" || got.TagCount != 2 || got.Tags["email_id"] != msg.ID.String() {
		t.Fatalf("request = %+v", got)
	}
	for _, line := range strings.Split(headerBlock(got.Raw), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "x-ses-") {
			t.Fatalf("client text became a header: %q", line)
		}
	}
}

func TestSESErrorMapping(t *testing.T) {
	t.Parallel()
	const secret = "diagnostic text with customer@example.test"
	tests := []struct {
		name      string
		resp      testaws.Response
		class     string
		temporary bool
	}{
		{"too many requests", testaws.Response{Status: 429, ErrorType: "TooManyRequestsException", Message: secret}, ClassThrottled, true},
		{"daily quota", testaws.Response{Status: 400, ErrorType: "LimitExceededException", Message: "Daily message quota exceeded"}, ClassThrottled, true},
		{"throttling", testaws.Response{Status: 400, ErrorType: "Throttling", Message: secret}, ClassThrottled, true},
		{"access denied", testaws.Response{Status: 403, ErrorType: "AccessDeniedException", Message: secret}, ClassAuth, false},
		{"unknown key", testaws.Response{Status: 403, ErrorType: "UnrecognizedClientException", Message: secret}, ClassAuth, false},
		{"bad signature", testaws.Response{Status: 403, ErrorType: "InvalidSignatureException", Message: secret}, ClassAuth, false},
		{"expired token", testaws.Response{Status: 403, ErrorType: "ExpiredTokenException"}, ClassAuth, false},
		{"unverified sender", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Email address is not verified. The following identities failed the check in region EU-CENTRAL-1: sender@example.test"}, ClassSenderUnverified, false},
		{"sandbox recipient not verified", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Email address is not verified. The following identities failed the check in region EU-CENTRAL-1: customer@example.test"}, ClassAPIRejected, false},
		{"unverified sender, upper case", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Email address is not verified. The following identities failed the check in region EU-CENTRAL-1: SENDER@EXAMPLE.TEST"}, ClassSenderUnverified, false},
		{"recipient sharing the sender's tail", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Email address is not verified. The following identities failed the check in region EU-CENTRAL-1: other-sender@example.test"}, ClassAPIRejected, false},
		{"unverified sender among several", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Email address is not verified. The following identities failed the check in region EU-CENTRAL-1: customer@example.test, sender@example.test"}, ClassSenderUnverified, false},
		{"unverified mail from", testaws.Response{Status: 400, ErrorType: "MailFromDomainNotVerifiedException", Message: secret}, ClassSenderUnverified, false},
		{"other rejection", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Message contains a virus"}, ClassAPIRejected, false},
		{"account suspended", testaws.Response{Status: 400, ErrorType: "AccountSuspendedException", Message: secret}, ClassAPIRejected, false},
		{"bad request", testaws.Response{Status: 400, ErrorType: "BadRequestException", Message: secret}, ClassAPIRejected, false},
		{"configuration set missing", testaws.Response{Status: 404, ErrorType: "NotFoundException", Message: secret}, ClassAPIRejected, false},
		{"internal error", testaws.Response{Status: 500, ErrorType: "InternalServiceErrorException", Message: secret}, ClassAPIUnavailable, true},
		{"unavailable without type", testaws.Response{Status: 503, Body: "<html>busy</html>"}, ClassAPIUnavailable, true},
		{"accepted but unreadable", testaws.Response{Status: 200, Body: "{not json"}, ClassProtocol, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := testaws.NewSESv2(t)
			stub.Respond(func(testaws.SendEmail) testaws.Response { return tt.resp })
			_, err := newTestSES(t, stub.URL).Send(context.Background(), sesMessage())
			var de *Error
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want *Error", err)
			}
			wantStatus := tt.resp.Status
			if de.Class != tt.class || de.HTTPStatus != wantStatus || de.Code != 0 || de.Temporary() != tt.temporary {
				t.Fatalf("error = %+v (temporary %v), want class %q, http %d, temporary %v", de, de.Temporary(), tt.class, wantStatus, tt.temporary)
			}
			// The stored detail names the class and the HTTP status, never
			// SMTP or the provider's text.
			msg := err.Error()
			if !strings.Contains(msg, tt.class) || strings.Contains(msg, "smtp") || strings.Contains(msg, "customer@") ||
				strings.Contains(msg, "virus") || strings.Contains(msg, "quota") || strings.Contains(msg, "not verified") {
				t.Fatalf("error text %q leaks provider text or names smtp", msg)
			}
		})
	}
}

func TestSESTimeout(t *testing.T) {
	t.Parallel()
	stub := testaws.NewSESv2(t)
	stub.Respond(func(testaws.SendEmail) testaws.Response { return testaws.Response{Delay: 5 * time.Second} })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := newTestSES(t, stub.URL).Send(ctx, sesMessage())
	var de *Error
	if !errors.As(err, &de) || de.Class != ClassTimeout || !de.Temporary() {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestSESConnectionRefused(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	_, err = newTestSES(t, "http://"+addr).Send(context.Background(), sesMessage())
	var de *Error
	if !errors.As(err, &de) || de.Class != ClassConnect || !de.Temporary() {
		t.Fatalf("err = %v, want connect", err)
	}
}

func TestSESInvalidMessageIsNotSent(t *testing.T) {
	t.Parallel()
	stub := testaws.NewSESv2(t)
	msg := sesMessage()
	msg.Text = ""
	_, err := newTestSES(t, stub.URL).Send(context.Background(), msg)
	var de *Error
	if !errors.As(err, &de) || de.Class != ClassInvalid {
		t.Fatalf("err = %v, want invalid", err)
	}
	if n := len(stub.Sends()); n != 0 {
		t.Fatalf("stub got %d requests for an invalid message", n)
	}
}

func TestNewSESRefusesBadConfig(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]SESConfig{
		"no configuration set":  {Region: "eu-central-1", Endpoint: "http://127.0.0.1:4566"},
		"bad region":            {Region: "moon-1", ConfigurationSet: "x", Endpoint: "http://127.0.0.1:4566"},
		"non-loopback endpoint": {Region: "eu-central-1", ConfigurationSet: "x", Endpoint: "https://email.eu-central-1.amazonaws.com"},
	} {
		if _, err := NewSES(context.Background(), c); err == nil {
			t.Errorf("%s: NewSES accepted %+v", name, c)
		}
	}
}

func TestSESErrorText(t *testing.T) {
	t.Parallel()
	if got := (&Error{Class: ClassThrottled, HTTPStatus: http.StatusTooManyRequests}).Error(); got != "delivery failed: throttled (http 429)" {
		t.Errorf("Error() = %q", got)
	}
	if got := (&Error{Class: ClassRejected, Code: 550}).Error(); got != "delivery failed: rejected (smtp 550)" {
		t.Errorf("Error() = %q", got)
	}
}

// headerBlock returns the header section of a raw message.
func headerBlock(raw []byte) string {
	h, _, ok := strings.Cut(string(raw), "\r\n\r\n")
	if !ok {
		return ""
	}
	return h
}

// names lists the header field names in order.
func names(block string) string {
	var out []string
	for _, line := range strings.Split(block, "\r\n") {
		if name, _, ok := strings.Cut(line, ":"); ok && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			out = append(out, name)
		}
	}
	return strings.Join(out, ",")
}
