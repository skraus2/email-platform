//go:build integration

package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/awsconfig"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events/ses"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/testaws"
	"github.com/skraus2/email-platform/internal/webhooks"
)

const (
	e2eRegion   = "eu-central-1"
	e2eAccount  = "123456789012"
	e2eTopicARN = "arn:aws:sns:" + e2eRegion + ":" + e2eAccount + ":ses-events"
	e2eQueueURL = "https://sqs." + e2eRegion + ".amazonaws.com/" + e2eAccount + "/ses-events"
)

// receivedHook is one webhook request as the receiving app saw it.
type receivedHook struct {
	Type    string
	EmailID string
	Valid   bool
}

// hookReceiver is the internal app: it verifies every request with the
// endpoint secret like a real receiver would.
type hookReceiver struct {
	srv    *httptest.Server
	secret string
	mu     sync.Mutex
	got    []receivedHook
}

func newHookReceiver(t *testing.T) *hookReceiver {
	r := &hookReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var p struct {
			Type string `json:"type"`
			Data struct {
				EmailID string `json:"email_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &p)
		r.mu.Lock()
		valid := webhooks.Verify(r.secret, req.Header.Get("Webhook-Timestamp"), req.Header.Get("Webhook-Signature"),
			body, 5*time.Minute, time.Now()) == nil
		r.got = append(r.got, receivedHook{Type: p.Type, EmailID: p.Data.EmailID, Valid: valid})
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// types returns the webhook types received for an email, in arrival order;
// it fails on any request with a bad signature.
func (r *hookReceiver) types(t *testing.T, emailID string) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, h := range r.got {
		if !h.Valid {
			t.Fatalf("webhook %s for %s with an invalid signature", h.Type, h.EmailID)
		}
		if h.EmailID == emailID {
			out = append(out, h.Type)
		}
	}
	return out
}

// sesEvent builds an SES event-publishing message for an email the platform
// sent (tags carry its ids), as the configuration set publishes it.
func sesEvent(t *testing.T, eventType, emailID, projectID, messageID string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e := map[string]any{
		"eventType": eventType,
		"mail": map[string]any{
			"timestamp": now, "messageId": messageID, "sendingAccountId": e2eAccount,
			"source": "noreply@example.test", "destination": []string{"customer@example.test"},
			"tags": map[string][]string{"email_id": {emailID}, "project_id": {projectID}},
		},
	}
	switch eventType {
	case "Delivery":
		e["delivery"] = map[string]any{"timestamp": now, "recipients": []string{"customer@example.test"}}
	case "Bounce":
		e["bounce"] = map[string]any{"bounceType": "Permanent", "bounceSubType": "General", "timestamp": now, "feedbackId": uuid.NewString()}
	case "Complaint":
		e["complaint"] = map[string]any{"complaintFeedbackType": "abuse", "timestamp": now, "feedbackId": uuid.NewString()}
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSESEndToEnd is the full send-to-webhook path with every AWS service
// stubbed on loopback: POST → worker → SES (stub) → submitted → SES event via
// SNS (signed) and SQS (stub) → poller → projected status, suppression and
// signed webhooks to the internal app.
func TestSESEndToEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStackWith(t, 1_000_000, true)
	projectID, key := s.project(t)
	logger := slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelWarn}))

	// The sender identity and the internal app's webhook endpoint (admin CLI
	// operations).
	identity, err := emails.AddSender(ctx, s.q, projectID, emails.SenderDomain, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := webhooks.ParseKeyring("k1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	targets := webhooks.TargetPolicy{AllowHTTP: true, AllowPrivate: true}
	receiver := newHookReceiver(t)
	if _, receiver.secret, err = webhooks.CreateEndpoint(ctx, s.q, keys, targets, projectID, receiver.srv.URL, nil); err != nil {
		t.Fatal(err)
	}

	// The worker: SES adapter on the stub, webhook outbox and delivery,
	// event poller on the SQS stub.
	sesStub := testaws.NewSESv2(t)
	provider, err := delivery.NewSES(ctx, delivery.SESConfig{Region: e2eRegion, ConfigurationSet: "platform-events", Endpoint: sesStub.URL})
	if err != nil {
		t.Fatal(err)
	}
	enq, err := jobs.NewEnqueuer(s.pool, logger, 5)
	if err != nil {
		t.Fatal(err)
	}
	d := emails.NewDispatcher(s.pool, provider, logger, 5*time.Second, 168*time.Hour)
	d.RequireVerifiedSenders()
	d.Events().OnStatusChange(webhooks.NewOutbox(enq).OnStatusChange)
	deliverer, err := webhooks.NewDeliverer(webhooks.DelivererConfig{
		Pool: s.pool, Logger: logger, Keys: keys, Targets: targets, Timeout: 5 * time.Second, MaxAttempts: 3,
		Retry: jobs.RetryPolicy{Base: 100 * time.Millisecond, Max: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: s.pool, Logger: logger, Concurrency: 2, JobTimeout: 30 * time.Second,
		Retry:              jobs.RetryPolicy{Base: time.Second, Max: 10 * time.Second},
		Handlers:           jobs.Handlers{SendEmail: d.SendEmail, DeliverWebhook: deliverer.Deliver, Cleanup: d.Cleanup},
		WebhookConcurrency: 2, WebhookTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })

	sqsStub := testaws.NewSQS(t)
	signer := testaws.NewSNSSigner(t, e2eRegion, e2eTopicARN)
	poller, err := ses.NewPollerWithCertFetch(ctx, ses.PollerConfig{
		Region: e2eRegion, QueueURL: e2eQueueURL, TopicARN: e2eTopicARN, Endpoint: sqsStub.URL,
		Recorder: d.Events(), Logger: logger,
	}, signer.Fetch)
	if err != nil {
		t.Fatal(err)
	}
	pollCtx, stopPoll := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	go func() { defer close(pollDone); poller.Run(pollCtx) }()
	t.Cleanup(func() { stopPoll(); <-pollDone })

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	status := func(id string) string {
		_, e := s.do(t, http.MethodGet, "/v1/emails/"+id, key, "")
		st, _ := e["status"].(string)
		return st
	}
	hooks := func(id string, want ...string) {
		t.Helper()
		waitFor(id+" webhooks", func() bool { return len(receiver.types(t, id)) >= len(want) })
		if got := receiver.types(t, id); !slices.Equal(got, want) {
			t.Fatalf("webhooks for %s = %v, want %v", id, got, want)
		}
	}
	// send posts an email, waits until SES accepted it and its submitted
	// webhook arrived (webhooks carry no ordering guarantee, so the next
	// event is only published after it), and returns its id and the SES
	// message id.
	send := func(to string) (string, string) {
		t.Helper()
		code, created := s.do(t, http.MethodPost, "/v1/emails", key,
			`{"from":"noreply@example.test","to":"`+to+`","subject":"Hi","text":"Hello"}`)
		if code != http.StatusAccepted {
			t.Fatalf("POST to %s = %d %v", to, code, created)
		}
		id := created["id"].(string)
		waitFor(id+" submitted", func() bool { return status(id) == "submitted" })
		hooks(id, "email.submitted")
		var messageID string
		if err := s.pool.QueryRow(ctx, "SELECT provider_id FROM emails WHERE id = $1", id).Scan(&messageID); err != nil {
			t.Fatal(err)
		}
		return id, messageID
	}
	publish := func(eventType, id, messageID string) {
		sqsStub.Add(signer.Envelope(t, sesEvent(t, eventType, id, projectID.String(), messageID)))
	}

	// A sender without a verified identity is refused before anything is
	// stored; once verified it may send.
	if code, body := s.do(t, http.MethodPost, "/v1/emails", key, sendBody); code != http.StatusUnprocessableEntity || errorCode(body) != "sender_not_verified" {
		t.Fatalf("unverified sender: %d %v", code, body)
	}
	if _, err := emails.VerifySender(ctx, s.q, projectID, identity.ID); err != nil {
		t.Fatal(err)
	}

	// Delivery: submitted → delivered, a signed webhook for each.
	delivered, msgA := send("customer@example.test")
	publish("Delivery", delivered, msgA)
	waitFor("delivered", func() bool { return status(delivered) == "delivered" })
	hooks(delivered, "email.submitted", "email.delivered")

	// Permanent bounce: bounced, the recipient is suppressed, the next send
	// to it is refused.
	bounced, msgB := send("gone@example.test")
	publish("Bounce", bounced, msgB)
	waitFor("bounced", func() bool { return status(bounced) == "bounced" })
	hooks(bounced, "email.submitted", "email.bounced")
	if code, body := s.do(t, http.MethodPost, "/v1/emails", key,
		`{"from":"noreply@example.test","to":"gone@example.test","subject":"Hi","text":"Hello"}`); code != http.StatusUnprocessableEntity || errorCode(body) != "recipient_suppressed" {
		t.Fatalf("send to bounced recipient: %d %v", code, body)
	}

	// Out of order and duplicated: complaint first, then a late delivery
	// twice. The late delivery is kept in the history but neither moves the
	// status back nor notifies.
	complained, msgC := send("angry@example.test")
	publish("Complaint", complained, msgC)
	waitFor("complained", func() bool { return status(complained) == "complained" })
	publish("Delivery", complained, msgC)
	publish("Delivery", complained, msgC)
	waitFor("queue drained", func() bool { return sqsStub.Pending() == 0 })
	var types []string
	waitFor("event history", func() bool {
		_, page := s.do(t, http.MethodGet, "/v1/emails/"+complained+"/events", key, "")
		data, _ := page["data"].([]any)
		types = types[:0]
		for _, e := range data {
			types = append(types, e.(map[string]any)["type"].(string))
		}
		return len(types) >= 3
	})
	if !slices.Equal(types, []string{"submitted", "complained", "delivered"}) || status(complained) != "complained" {
		t.Fatalf("history %v, status %s; want submitted, complained, delivered and complained", types, status(complained))
	}
	// Every ingest has committed (the queue only drains after it), and with
	// it any outbox row: two deliveries, none for the late delivery.
	var outbox int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries d JOIN email_events e ON e.id = d.event_id
		WHERE e.email_id = $1`, complained).Scan(&outbox); err != nil || outbox != 2 {
		t.Fatalf("%d webhook deliveries for the complained email (%v), want 2", outbox, err)
	}
	hooks(complained, "email.submitted", "email.complained")

	// Every send went to the loopback stub, signed with the fixed fake key.
	sends := sesStub.Sends()
	if len(sends) != 3 {
		t.Fatalf("SES stub got %d sends, want 3", len(sends))
	}
	for _, r := range sends {
		if !strings.Contains(r.Authorization, "Credential="+awsconfig.StubAccessKeyID+"/") {
			t.Fatalf("SES request signed with %q, want the stub key", r.Authorization)
		}
	}
	for _, r := range sqsStub.Requests() {
		if !strings.Contains(r.Authorization, "Credential="+awsconfig.StubAccessKeyID+"/") {
			t.Fatalf("SQS request signed with %q, want the stub key", r.Authorization)
		}
	}
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}
