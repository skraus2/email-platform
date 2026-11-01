//go:build integration

// Runs the client against real API and worker processes. Started by
// scripts/sdk-integration.sh, which passes the API origin, a project key and
// the webhook endpoint's secret in the environment. This process also plays
// the app's webhook endpoint, so the verifier sees a real delivery the way an
// app handler does: net/http request headers and the raw body bytes.
package emailplatform_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	emailplatform "github.com/skraus2/email-platform/packages/sdk-go"
	"github.com/skraus2/email-platform/packages/sdk-go/webhook"
)

const maxWebhookBody = 64 << 10

// env fails the test, never skips it: a run without its environment must not
// pass as green.
func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s must be set (scripts/sdk-integration.sh sets it)", name)
	}
	return v
}

type delivery struct {
	header http.Header
	body   []byte
	// status is what this endpoint answered.
	status int
}

type receiver struct {
	mu         sync.Mutex
	deliveries []delivery
}

func (rc *receiver) all() []delivery {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return slices.Clone(rc.deliveries)
}

// handler is the app's webhook endpoint: the raw body with a size cap, then
// verification.
func (rc *receiver) handler(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		status := http.StatusNoContent
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			status = http.StatusRequestEntityTooLarge
		} else if err != nil {
			status = http.StatusBadRequest
		} else if _, err := webhook.Verify(secret, r.Header, body, time.Now()); err != nil {
			status = http.StatusInternalServerError
			if e, ok := errors.AsType[*webhook.Error](err); ok && e.Reason != webhook.ReasonMissingSecret {
				status = http.StatusBadRequest
			}
		}
		rc.mu.Lock()
		rc.deliveries = append(rc.deliveries, delivery{header: r.Header.Clone(), body: body, status: status})
		rc.mu.Unlock()
		w.WriteHeader(status)
	})
}

// eventually polls until probe returns true, or fails after 20 seconds.
func eventually(t *testing.T, what string, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !probe() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestClientAgainstTheRealAPIAndWorker(t *testing.T) {
	baseURL := env(t, "EP_BASE_URL")
	apiKey := env(t, "EP_API_KEY")
	secret := env(t, "EP_WEBHOOK_SECRET")
	receiverAddr := env(t, "EP_WEBHOOK_ADDR")
	run := env(t, "EP_RUN")

	// Bound before the first send; a taken port fails here, not later.
	ln, err := net.Listen("tcp", receiverAddr)
	if err != nil {
		t.Fatal(err)
	}
	rc := &receiver{}
	server := &http.Server{Handler: rc.handler(secret), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })

	cl, err := emailplatform.New(apiKey, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	recipient := "go.sdk-it-" + run + "@example.test"
	message := func(subject string) *emailplatform.CreateEmailRequest {
		return &emailplatform.CreateEmailRequest{
			From:    "sdk@example.test",
			To:      recipient,
			Subject: new("go sdk " + run + subject),
			Text:    new("go sdk integration body"),
		}
	}
	var id string

	t.Run("send with an app key, replay, get until captured", func(t *testing.T) {
		key := emailplatform.WithIdempotencyKey("go.sdk-it-" + run + ":welcome")
		sent, err := cl.Emails.Send(ctx, message(""), key)
		if err != nil {
			t.Fatal(err)
		}
		if sent.Status != emailplatform.CreateEmailResponseStatusQueued {
			t.Fatalf("status %s", sent.Status)
		}
		id = sent.Id

		replayed, err := cl.Emails.Send(ctx, message(""), key)
		if err != nil || replayed.Id != id {
			t.Fatalf("replay: %+v, %v; the same key must return the same email", replayed, err)
		}

		var email *emailplatform.Email
		eventually(t, "captured", func() bool {
			email, err = cl.Emails.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if email.Status == emailplatform.EmailStatusFailed {
				t.Fatalf("email failed: %+v", email.ErrorCode)
			}
			return email.Status == emailplatform.EmailStatusCaptured
		})
		if email.To != recipient {
			t.Fatalf("to = %s", email.To)
		}
	})
	if id == "" {
		t.FailNow()
	}

	t.Run("events through the iterator", func(t *testing.T) {
		var types []string
		found := false
		for event, err := range cl.Emails.ListAllEvents(ctx, id, nil) {
			if err != nil {
				t.Fatal(err)
			}
			types = append(types, string(event.Type))
			if event.Type == emailplatform.EmailEventTypeCaptured && event.Origin == emailplatform.Platform {
				found = true
			}
		}
		if !found {
			t.Fatalf("no captured platform event in %v", types)
		}
	})

	t.Run("list follows next_cursor across pages", func(t *testing.T) {
		want := []string{id}
		for _, subject := range []string{" 2", " 3"} {
			sent, err := cl.Emails.Send(ctx, message(subject))
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, sent.Id)
		}
		slices.Sort(want)
		if len(slices.Compact(slices.Clone(want))) != 3 {
			t.Fatalf("ids not distinct: %v", want)
		}

		params := &emailplatform.ListEmailsParams{Recipient: new(recipient), Limit: new(1)}
		first, err := cl.Emails.List(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Data) != 1 || first.NextCursor == nil {
			t.Fatalf("first page: %d items, cursor %v", len(first.Data), first.NextCursor)
		}

		var got []string
		for email, err := range cl.Emails.ListAll(ctx, params) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, email.Id)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("listed %v, want %v", got, want)
		}
	})

	t.Run("an API error carries the code and the request id", func(t *testing.T) {
		_, err := cl.Emails.Get(ctx, "0199e3a0-0000-7000-8000-00000000dead")
		e, ok := errors.AsType[*emailplatform.APIError](err)
		if !ok {
			t.Fatalf("err = %v, want *APIError", err)
		}
		if e.Status != http.StatusNotFound || e.Code != "not_found" || strings.TrimSpace(e.RequestID) == "" {
			t.Fatalf("%+v", e)
		}
	})

	t.Run("a real webhook delivery verifies; a changed one does not", func(t *testing.T) {
		var d delivery
		eventually(t, "the email.captured webhook", func() bool {
			for _, x := range rc.all() {
				// Only to find the delivery; whether it verified is asserted below.
				var event struct {
					Type string `json:"type"`
					Data struct {
						EmailID string `json:"email_id"`
					} `json:"data"`
				}
				if json.Unmarshal(x.body, &event) == nil && event.Type == "email.captured" && event.Data.EmailID == id {
					d = x
					return true
				}
			}
			return false
		})
		if d.status != http.StatusNoContent {
			t.Fatalf("the endpoint answered %d", d.status)
		}
		for _, x := range rc.all() {
			if x.status != http.StatusNoContent {
				t.Fatalf("a delivery was answered %d", x.status)
			}
		}

		v, err := webhook.Verify(secret, d.header, d.body, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if v.ID != d.header.Get("Webhook-Id") || v.Event.Id != v.ID ||
			v.Event.Type != emailplatform.EmailCaptured || v.Event.Data.EmailId != id {
			t.Fatalf("%+v", v)
		}

		changed := bytes.Clone(d.body)
		at := bytes.Index(changed, []byte(id))
		if changed[at] == '0' {
			changed[at] = '1'
		} else {
			changed[at] = '0'
		}
		_, err = webhook.Verify(secret, d.header, changed, time.Now())
		if e, ok := errors.AsType[*webhook.Error](err); !ok || e.Reason != webhook.ReasonSignatureMismatch {
			t.Fatalf("changed body: %v", err)
		}

		// The same changed delivery through the endpoint is refused.
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+receiverAddr+"/hooks", bytes.NewReader(changed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for _, name := range []string{"Webhook-Id", "Webhook-Timestamp", "Webhook-Signature"} {
			req.Header.Set(name, d.header.Get(name))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("changed delivery answered %d", resp.StatusCode)
		}
		// Found by its body: a platform delivery may be recorded after it.
		all := rc.all()
		i := slices.IndexFunc(all, func(x delivery) bool { return bytes.Equal(x.body, changed) })
		if i < 0 || all[i].status != http.StatusBadRequest {
			t.Fatalf("changed delivery not recorded as refused (index %d)", i)
		}
	})
}
