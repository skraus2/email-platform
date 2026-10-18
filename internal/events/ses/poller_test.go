package ses

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/awsconfig"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/testaws"
)

const queueURL = "https://sqs.eu-central-1.amazonaws.com/123456789012/ses-events"

// fakeIngest records the events handed to it and answers with fn.
type fakeIngest struct {
	mu     sync.Mutex
	events []events.ProviderEvent
	fn     func(ctx context.Context, e events.ProviderEvent) (events.Outcome, error)
}

func (f *fakeIngest) ingest(ctx context.Context, e events.ProviderEvent) (events.Outcome, error) {
	f.mu.Lock()
	f.events = append(f.events, e)
	fn := f.fn
	f.mu.Unlock()
	if fn == nil {
		return events.Stored, nil
	}
	return fn(ctx, e)
}

func (f *fakeIngest) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type pollerSetup struct {
	poller *Poller
	stub   *testaws.SQS
	ingest *fakeIngest
	signer *signer
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// newTestPoller builds a poller against a stub queue with a fake ingest and
// a verifier that trusts the test signer; fetch may replace the certificate
// fetch.
func newTestPoller(t *testing.T, fetch Fetcher) *pollerSetup {
	t.Helper()
	s := validSigner(t)
	v := s.verifier(nil)
	if fetch != nil {
		v.fetch = fetch
	}
	stub := testaws.NewSQS(t)
	logs := &syncBuffer{}
	p, err := newPoller(context.Background(), PollerConfig{
		Region: region, QueueURL: queueURL, TopicARN: topicARN, Endpoint: stub.URL,
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, v)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIngest{}
	p.ingest = f.ingest
	p.backoff = func(int) time.Duration { return time.Millisecond }
	return &pollerSetup{poller: p, stub: stub, ingest: f, signer: s, logs: logs}
}

func (ps *pollerSetup) signed(t *testing.T, message string) string {
	t.Helper()
	return string(ps.signer.envelope(t, message, nil, nil))
}

func TestPollerOutcomes(t *testing.T) {
	delivery := fixture(t, "delivery")
	for _, tc := range []struct {
		name      string
		body      func(ps *pollerSetup) string
		ingest    func(context.Context, events.ProviderEvent) (events.Outcome, error)
		outcome   string
		ingested  bool
		deleted   bool
		transient bool
	}{
		{name: "stored", body: func(ps *pollerSetup) string { return ps.signed(t, delivery) },
			outcome: "stored", ingested: true, deleted: true},
		{name: "duplicate", body: func(ps *pollerSetup) string { return ps.signed(t, delivery) },
			ingest:  func(context.Context, events.ProviderEvent) (events.Outcome, error) { return events.Duplicate, nil },
			outcome: "duplicate", ingested: true, deleted: true},
		{name: "unknown email", body: func(ps *pollerSetup) string { return ps.signed(t, delivery) },
			ingest:  func(context.Context, events.ProviderEvent) (events.Outcome, error) { return events.UnknownEmail, nil },
			outcome: "unknown_email", ingested: true, deleted: true},
		{name: "unknown email suppressed", body: func(ps *pollerSetup) string { return ps.signed(t, delivery) },
			ingest: func(context.Context, events.ProviderEvent) (events.Outcome, error) {
				return events.UnknownEmailSuppressed, nil
			},
			outcome: "unknown_email_suppressed", ingested: true, deleted: true},
		{name: "provider mismatch", body: func(ps *pollerSetup) string { return ps.signed(t, delivery) },
			ingest: func(context.Context, events.ProviderEvent) (events.Outcome, error) {
				return events.ProviderMismatch, nil
			},
			outcome: "provider_mismatch", ingested: true, deleted: true},
		{name: "refused by ingest", body: func(ps *pollerSetup) string { return ps.signed(t, delivery) },
			ingest: func(context.Context, events.ProviderEvent) (events.Outcome, error) {
				return 0, errors.Join(errors.New("x"), events.ErrInvalidEvent)
			},
			outcome: "invalid", ingested: true, deleted: true},
		{name: "database error", body: func(ps *pollerSetup) string { return ps.signed(t, delivery) },
			ingest: func(context.Context, events.ProviderEvent) (events.Outcome, error) {
				return 0, errors.New("ingest event: connection refused")
			},
			outcome: "ingest_failed", ingested: true, transient: true},
		{name: "not sns", body: func(*pollerSetup) string { return delivery }, outcome: "unauthenticated", deleted: true},
		{name: "not json", body: func(*pollerSetup) string { return "{" }, outcome: "unauthenticated", deleted: true},
		{name: "bad signature", body: func(ps *pollerSetup) string {
			return string(ps.signer.envelope(t, delivery, nil, func(e map[string]string) { e["Message"] += " " }))
		}, outcome: "unauthenticated", deleted: true},
		{name: "subscription confirmation", body: func(ps *pollerSetup) string {
			return string(ps.signer.envelope(t, delivery, func(e map[string]string) { e["Type"] = "SubscriptionConfirmation" }, nil))
		}, outcome: "unauthenticated", deleted: true},
		{name: "foreign topic", body: func(ps *pollerSetup) string {
			return string(ps.signer.envelope(t, delivery, func(e map[string]string) {
				e["TopicArn"] = "arn:aws:sns:eu-central-1:123456789012:other"
			}, nil))
		}, outcome: "unauthenticated", deleted: true},
		{name: "engagement", body: func(ps *pollerSetup) string { return ps.signed(t, fixture(t, "open")) },
			outcome: "ignored", deleted: true},
		{name: "invalid event", body: func(ps *pollerSetup) string { return ps.signed(t, `{"eventType":"Delivery"}`) },
			outcome: "invalid", deleted: true},
		{name: "foreign account", body: func(ps *pollerSetup) string {
			return ps.signed(t, strings.Replace(delivery, `"sendingAccountId":"123456789012"`, `"sendingAccountId":"210987654321"`, 1))
		}, outcome: "foreign_account", deleted: true},
		{name: "no account", body: func(ps *pollerSetup) string {
			return ps.signed(t, strings.Replace(delivery, `"sendingAccountId":"123456789012",`, ``, 1))
		}, outcome: "foreign_account", deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := newTestPoller(t, nil)
			ps.ingest.fn = tc.ingest
			ps.stub.Add(tc.body(ps))
			err := ps.poller.poll(context.Background())
			if (err != nil) != tc.transient {
				t.Fatalf("poll error = %v, want transient %v", err, tc.transient)
			}
			if got := ps.ingest.calls() == 1; got != tc.ingested {
				t.Fatalf("ingested = %v, want %v", got, tc.ingested)
			}
			if got := len(ps.stub.Deleted()) == 1; got != tc.deleted {
				t.Fatalf("deleted = %v, want %v", got, tc.deleted)
			}
			if !strings.Contains(ps.logs.String(), `"outcome":"`+tc.outcome+`"`) {
				t.Fatalf("outcome %q not logged:\n%s", tc.outcome, ps.logs)
			}
			for _, secret := range []string{"customer@example.test", "noreply@example.test", "smtpResponse", "Signature"} {
				if strings.Contains(ps.logs.String(), secret) {
					t.Fatalf("log carries %q:\n%s", secret, ps.logs)
				}
			}
		})
	}
}

func TestPollerHandsOverTheParsedEvent(t *testing.T) {
	ps := newTestPoller(t, nil)
	ps.stub.Add(ps.signed(t, fixture(t, "bounce_permanent")))
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ps.ingest.calls() != 1 {
		t.Fatalf("ingest calls = %d", ps.ingest.calls())
	}
	e := ps.ingest.events[0]
	if e.Type != "bounced" || e.EmailID != fixtureEmail || e.ProjectID != fixtureProject || e.Details["bounce_type"] != "Permanent" {
		t.Fatalf("ingested %+v", e)
	}
}

// A transient failure leaves only that message on the queue (it comes back
// after the visibility timeout); the rest of the batch is still handled, so
// healthy events are not received again for nothing.
func TestPollerKeepsTransientFailures(t *testing.T) {
	t.Run("database", func(t *testing.T) {
		ps := newTestPoller(t, nil)
		ps.ingest.fn = func(_ context.Context, e events.ProviderEvent) (events.Outcome, error) {
			if e.Type == "delivered" {
				return 0, errors.New("ingest event: db down")
			}
			return events.Stored, nil
		}
		ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
		ps.stub.Add(ps.signed(t, fixture(t, "send")))
		if err := ps.poller.poll(context.Background()); err == nil {
			t.Fatal("poll reported no error")
		}
		if n := ps.ingest.calls(); n != 2 {
			t.Fatalf("ingest calls = %d, want 2", n)
		}
		if ps.stub.Pending() != 1 || len(ps.stub.Deleted()) != 1 || !strings.Contains(ps.stub.Deleted()[0], `\"eventType\":\"Send\"`) {
			t.Fatalf("pending %d, deleted %v; want only the send event deleted", ps.stub.Pending(), ps.stub.Deleted())
		}
	})
	t.Run("certificate fetch", func(t *testing.T) {
		ps := newTestPoller(t, func(context.Context, string) ([]byte, error) { return nil, errors.New("status 503") })
		ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
		if err := ps.poller.poll(context.Background()); err == nil {
			t.Fatal("poll reported no error")
		}
		if ps.ingest.calls() != 0 || len(ps.stub.Deleted()) != 0 || ps.stub.Pending() != 1 {
			t.Fatalf("ingest %d, deleted %d, pending %d", ps.ingest.calls(), len(ps.stub.Deleted()), ps.stub.Pending())
		}
		if !strings.Contains(ps.logs.String(), `"outcome":"certificate_unavailable"`) {
			t.Fatalf("outcome not logged:\n%s", ps.logs)
		}
	})
}

// The SDK checks every body against its MD5; a corrupted batch is a receive
// error, nothing of it is handled.
func TestPollerRefusesCorruptedBodies(t *testing.T) {
	ps := newTestPoller(t, nil)
	ps.stub.CorruptChecksums()
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	if err := ps.poller.poll(context.Background()); err == nil {
		t.Fatal("poll reported no error")
	}
	if ps.ingest.calls() != 0 || len(ps.stub.Deleted()) != 0 {
		t.Fatalf("ingest %d, deleted %d", ps.ingest.calls(), len(ps.stub.Deleted()))
	}
}

// A failed delete is only logged: the redelivered message is deduplicated
// by the ingest.
func TestPollerDeleteFailureKeepsGoing(t *testing.T) {
	ps := newTestPoller(t, nil)
	ps.stub.Fail(func(r testaws.SQSRequest) testaws.Response {
		if r.Operation == "DeleteMessage" {
			return testaws.Response{Status: http.StatusInternalServerError, ErrorType: "InternalError", Message: "secret detail"}
		}
		return testaws.Response{}
	})
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	ps.stub.Add(ps.signed(t, fixture(t, "send")))
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ps.ingest.calls() != 2 || ps.stub.Pending() != 2 {
		t.Fatalf("ingest %d, pending %d; want 2, 2", ps.ingest.calls(), ps.stub.Pending())
	}
	if !strings.Contains(ps.logs.String(), "delete") || strings.Contains(ps.logs.String(), "secret detail") {
		t.Fatalf("logs:\n%s", ps.logs)
	}
}

func TestPollerReceiveRequest(t *testing.T) {
	ps := newTestPoller(t, nil)
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := ps.stub.Requests()
	if len(reqs) != 2 || reqs[0].Operation != "ReceiveMessage" || reqs[1].Operation != "DeleteMessage" {
		t.Fatalf("requests %+v", reqs)
	}
	r := reqs[0]
	if r.QueueURL != queueURL || r.WaitTimeSeconds != 20 || r.MaxNumberOfMessages != 10 ||
		r.VisibilityTimeout < 2*maxMessages*int(maxMessageTimeout/time.Second) || len(r.SystemAttributes) != 1 || r.SystemAttributes[0] != "ApproximateReceiveCount" {
		t.Fatalf("receive request %+v", r)
	}
	for _, r := range reqs {
		if !strings.Contains(r.Authorization, "Credential="+awsconfig.StubAccessKeyID+"/") || !strings.Contains(r.Authorization, "/eu-central-1/sqs/aws4_request") {
			t.Fatalf("authorization %q", r.Authorization)
		}
		if r.QueueURL != queueURL {
			t.Fatalf("queue %q", r.QueueURL)
		}
	}
	if !strings.Contains(ps.logs.String(), `"receive_count":1`) {
		t.Fatalf("receive count not logged:\n%s", ps.logs)
	}
}

func TestPollerRetriesFailedReceives(t *testing.T) {
	ps := newTestPoller(t, nil)
	var failures atomic.Int32
	ps.stub.Fail(func(r testaws.SQSRequest) testaws.Response {
		if r.Operation == "ReceiveMessage" && failures.Add(1) <= 2 {
			return testaws.Response{Status: http.StatusInternalServerError, ErrorType: "InternalError", Message: "secret detail"}
		}
		return testaws.Response{}
	})
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	var delays []int
	ps.poller.backoff = func(n int) time.Duration { delays = append(delays, n); return time.Millisecond }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); ps.poller.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(ps.stub.Deleted()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if len(ps.stub.Deleted()) != 1 {
		t.Fatal("message not processed after receive errors")
	}
	if len(delays) != 2 || delays[0] != 1 || delays[1] != 2 {
		t.Fatalf("backoff steps %v, want [1 2]", delays)
	}
	if strings.Contains(ps.logs.String(), "secret detail") {
		t.Fatalf("AWS error text logged:\n%s", ps.logs)
	}
}

func TestPollerBackoffIsCapped(t *testing.T) {
	if d := receiveBackoff(1); d != time.Second {
		t.Fatalf("first backoff %v", d)
	}
	if d := receiveBackoff(3); d != 4*time.Second {
		t.Fatalf("third backoff %v", d)
	}
	if d := receiveBackoff(100); d != maxBackoff {
		t.Fatalf("backoff not capped: %v", d)
	}
}

// Shutdown interrupts the long poll at once.
func TestPollerStopsDuringLongPoll(t *testing.T) {
	ps := newTestPoller(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); ps.poller.Run(ctx) }()
	for deadline := time.Now().Add(5 * time.Second); len(ps.stub.Requests()) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("no receive request")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("stopped after %v", d)
	}
}

// A message already received is finished on shutdown: its ingest and delete
// are not cut off half-way.
func TestPollerFinishesMessageOnShutdown(t *testing.T) {
	ps := newTestPoller(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var ingestCtxErr error
	ps.ingest.fn = func(ictx context.Context, _ events.ProviderEvent) (events.Outcome, error) {
		cancel() // shutdown arrives during the ingest
		ingestCtxErr = ictx.Err()
		return events.Stored, nil
	}
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	ps.stub.Add(ps.signed(t, fixture(t, "send")))
	ps.poller.Run(ctx)
	if ingestCtxErr != nil {
		t.Fatalf("ingest context cancelled by shutdown: %v", ingestCtxErr)
	}
	if len(ps.stub.Deleted()) != 1 || ps.ingest.calls() != 1 {
		t.Fatalf("deleted %d, ingested %d; want the first finished and the second left", len(ps.stub.Deleted()), ps.ingest.calls())
	}
	if n := len(ps.stub.Requests()); n != 2 {
		t.Fatalf("%d requests, want one receive and one delete", n)
	}
}

// The message budget never exceeds the shutdown budget.
func TestPollerMessageBudget(t *testing.T) {
	ps := newTestPoller(t, nil)
	var left time.Duration
	ps.ingest.fn = func(ctx context.Context, _ events.ProviderEvent) (events.Outcome, error) {
		d, _ := ctx.Deadline()
		left = time.Until(d)
		return events.Stored, nil
	}
	ps.poller.messageTimeout = 3 * time.Second
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left <= 0 || left > 3*time.Second {
		t.Fatalf("ingest deadline in %v, want within 3s", left)
	}
}

// recordingClient records request hosts and never touches the network.
type recordingClient struct {
	mu    sync.Mutex
	hosts []string
}

func (c *recordingClient) Do(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hosts = append(c.hosts, r.URL.Host)
	return nil, errors.New("recording client: not sent")
}

// TestSQSIgnoresAmbientEndpointOverrides: the SDK reads endpoint overrides
// from the environment and the shared AWS config. None of them may redirect
// signed SQS requests; only the validated SQS_ENDPOINT can.
func TestSQSIgnoresAmbientEndpointOverrides(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "config")
	if err := os.WriteFile(profile, []byte("[default]\nendpoint_url = http://127.0.0.1:1\nservices = redirect\n\n[services redirect]\nsqs =\n  endpoint_url = http://127.0.0.1:2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"AWS_PROFILE", "AWS_SESSION_TOKEN", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ENDPOINT_URL",
		"AWS_ENDPOINT_URL_SQS", "AWS_USE_FIPS_ENDPOINT", "AWS_USE_DUALSTACK_ENDPOINT", "AWS_CA_BUNDLE",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Setenv("AWS_CONFIG_FILE", profile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", awsconfig.StubAccessKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local-stub-secret")
	t.Setenv(awsconfig.IgnoreEndpointsEnv, "true")

	for name, env := range map[string]map[string]string{
		"profile":          {},
		"global env":       {"AWS_ENDPOINT_URL": "http://127.0.0.1:3"},
		"service env":      {"AWS_ENDPOINT_URL_SQS": "http://127.0.0.1:4"},
		"global + service": {"AWS_ENDPOINT_URL": "http://127.0.0.1:3", "AWS_ENDPOINT_URL_SQS": "http://127.0.0.1:4"},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			rec := &recordingClient{}
			p, err := newPoller(context.Background(), PollerConfig{
				Region: region, QueueURL: queueURL, TopicARN: topicARN, Logger: slog.New(slog.DiscardHandler),
			}, validSigner(t).verifier(nil), func(o *sqs.Options) { o.HTTPClient = rec })
			if err != nil {
				t.Fatal(err)
			}
			_ = p.poll(context.Background())
			if len(rec.hosts) != 1 || rec.hosts[0] != "sqs.eu-central-1.amazonaws.com" {
				t.Fatalf("request hosts = %v, want only the regional SQS endpoint", rec.hosts)
			}
		})
	}

	t.Run("stub endpoint wins over the environment", func(t *testing.T) {
		t.Setenv("AWS_ENDPOINT_URL_SQS", "http://127.0.0.1:4")
		ps := newTestPoller(t, nil)
		ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
		if err := ps.poller.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(ps.stub.Deleted()) != 1 {
			t.Fatal("stub did not get the requests")
		}
	})
}

func TestNewPollerValidatesConfig(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	rec := events.NewRecorder(nil, logger) // never used: nothing is received
	for name, c := range map[string]PollerConfig{
		"bad region":      {Region: "nowhere", QueueURL: queueURL, TopicARN: topicARN, Endpoint: "http://127.0.0.1:1", Logger: logger, Recorder: rec},
		"topic elsewhere": {Region: region, QueueURL: queueURL, TopicARN: "arn:aws:sns:us-east-1:123456789012:x", Endpoint: "http://127.0.0.1:1", Logger: logger, Recorder: rec},
		"no queue":        {Region: region, TopicARN: topicARN, Endpoint: "http://127.0.0.1:1", Logger: logger, Recorder: rec},
		"remote endpoint": {Region: region, QueueURL: queueURL, TopicARN: topicARN, Endpoint: "http://10.0.0.1:1", Logger: logger, Recorder: rec},
		"no logger":       {Region: region, QueueURL: queueURL, TopicARN: topicARN, Endpoint: "http://127.0.0.1:1", Recorder: rec},
		"no recorder":     {Region: region, QueueURL: queueURL, TopicARN: topicARN, Endpoint: "http://127.0.0.1:1", Logger: logger},
	} {
		if _, err := NewPoller(context.Background(), c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p, err := NewPoller(context.Background(), PollerConfig{
		Region: region, QueueURL: queueURL, TopicARN: topicARN, Endpoint: "http://127.0.0.1:1", Logger: logger, Recorder: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	clamped, err := NewPoller(context.Background(), PollerConfig{
		Region: region, QueueURL: queueURL, TopicARN: topicARN, Endpoint: "http://127.0.0.1:1", Logger: logger, Recorder: rec,
		MessageTimeout: 30 * time.Second,
	})
	if err != nil || clamped.messageTimeout != maxMessageTimeout {
		t.Fatalf("MessageTimeout 30s: %v, %v; want clamped to %v", clamped, err, maxMessageTimeout)
	}
	if p.account != "123456789012" || p.verifier == nil || p.verifier.fetch == nil || p.messageTimeout != maxMessageTimeout || p.ingest == nil {
		t.Fatalf("poller %+v", p)
	}
}

// A transient store failure is logged with its SQLSTATE (never the server's
// detail text, which can quote row values); a message that keeps coming back
// is logged as an error so an operator sees it before the dead-letter queue.
func TestPollerReportsPersistentFailures(t *testing.T) {
	ps := newTestPoller(t, nil)
	ps.ingest.fn = func(context.Context, events.ProviderEvent) (events.Outcome, error) {
		return 0, fmt.Errorf("ingest event: %w", &pgconn.PgError{Code: "23514", Message: "check", Detail: "Failing row contains (customer@example.test)"})
	}
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	for range alertReceiveCount {
		if err := ps.poller.poll(context.Background()); err == nil {
			t.Fatal("poll reported no error")
		}
		ps.stub.ExpireVisibility()
	}
	logs := ps.logs.String()
	if !strings.Contains(logs, `"sqlstate":"23514"`) || strings.Contains(logs, "customer@example.test") {
		t.Fatalf("logs:\n%s", logs)
	}
	if n := strings.Count(logs, `"level":"ERROR"`); n != 1 || !strings.Contains(logs, `"receive_count":`+strconv.Itoa(alertReceiveCount)) {
		t.Fatalf("%d error lines, want one at receive %d:\n%s", n, alertReceiveCount, logs)
	}
}
