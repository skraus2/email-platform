//go:build integration

package emails_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testaws"
)

func sesDispatcher(t *testing.T, pool *pgxpool.Pool, stub *testaws.SESv2, timeout time.Duration, logs *bytes.Buffer) *emails.Dispatcher {
	t.Helper()
	p, err := delivery.NewSES(context.Background(), delivery.SESConfig{
		Region: "eu-central-1", ConfigurationSet: "platform-events", Endpoint: stub.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	return emails.NewDispatcher(pool, p, logger, timeout, 168*time.Hour)
}

func TestDispatchThroughSESStub(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	stub := testaws.NewSESv2(t)
	stub.Respond(func(testaws.SendEmail) testaws.Response { return testaws.Response{MessageID: "0107-ses-message"} })
	var logs bytes.Buffer

	if err := sesDispatcher(t, pool, stub, 5*time.Second, &logs).SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	s := state(t, pool, e.ID)
	if s.status != "submitted" || !s.finished || s.hasContent || s.code() != "" {
		t.Fatalf("state = %+v, want submitted, content deleted", s)
	}
	var provider, providerID string
	if err := pool.QueryRow(ctx, "SELECT provider, provider_id FROM emails WHERE id = $1", e.ID).Scan(&provider, &providerID); err != nil {
		t.Fatal(err)
	}
	if provider != "ses" || providerID != "0107-ses-message" {
		t.Fatalf("provider = %q %q", provider, providerID)
	}
	if n := count(t, pool, "SELECT count(*) FROM email_events WHERE email_id = '"+e.ID.String()+"' AND type = 'submitted' AND provider = 'ses'"); n != 1 {
		t.Fatalf("%d submitted events, want 1", n)
	}
	sends := stub.Sends()
	if len(sends) != 1 || sends[0].Tags["email_id"] != e.ID.String() || sends[0].Tags["project_id"] != projectID.String() ||
		sends[0].ConfigurationSet != "platform-events" || sends[0].From != input.From || sends[0].To[0] != input.To {
		t.Fatalf("SES requests = %+v", sends)
	}
	if strings.Contains(logs.String(), input.To) {
		t.Fatal("log contains the recipient address")
	}
}

func TestDispatchSESOutcomes(t *testing.T) {
	t.Parallel()
	const awsText = "diagnostic for customer@example.test"
	tests := []struct {
		name     string
		resp     testaws.Response
		status   string // after a non-final attempt
		code     string
		detail   string
		retried  bool // the job returns an error so the queue retries
		duplicit bool // the next attempt is flagged possible_duplicate_send
	}{
		{"throttled", testaws.Response{Status: 429, ErrorType: "TooManyRequestsException", Message: awsText}, "queued", "provider_throttled", "delivery failed: throttled (http 429)", true, false},
		{"daily quota", testaws.Response{Status: 400, ErrorType: "LimitExceededException", Message: awsText}, "queued", "provider_throttled", "delivery failed: throttled (http 400)", true, false},
		{"access denied", testaws.Response{Status: 403, ErrorType: "AccessDeniedException", Message: awsText}, "failed", "provider_auth_failed", "delivery failed: auth (http 403)", false, false},
		{"unverified sender", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Email address is not verified. The following identities failed the check: " + input.From}, "failed", "sender_not_verified", "delivery failed: sender_unverified (http 400)", false, false},
		{"rejected", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: awsText}, "failed", "provider_rejected", "delivery failed: api_rejected (http 400)", false, false},
		{"sandbox recipient", testaws.Response{Status: 400, ErrorType: "MessageRejected", Message: "Email address is not verified: " + awsText}, "failed", "provider_rejected", "delivery failed: api_rejected (http 400)", false, false},
		{"server error", testaws.Response{Status: 500, ErrorType: "InternalServiceErrorException", Message: awsText}, "sending", "provider_unreachable", "delivery failed: api_unavailable (http 500)", true, true},
		{"timeout", testaws.Response{Delay: 3 * time.Second}, "sending", "provider_timeout", "delivery failed: timeout", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool, projectID := setup(t)
			e := queued(t, pool, projectID)
			stub := testaws.NewSESv2(t)
			stub.Respond(func(testaws.SendEmail) testaws.Response { return tt.resp })
			var logs bytes.Buffer
			d := sesDispatcher(t, pool, stub, 300*time.Millisecond, &logs)

			err := d.SendEmail(ctx, args(e), attempt(1))
			if (err != nil) != tt.retried {
				t.Fatalf("SendEmail err = %v, want retry %v", err, tt.retried)
			}
			s := state(t, pool, e.ID)
			if s.status != tt.status || s.code() != tt.code || s.lastError == nil || *s.lastError != tt.detail {
				t.Fatalf("state = %+v (last_error %v), want %s / %s / %q", s, s.lastError, tt.status, tt.code, tt.detail)
			}
			if s.status == "failed" && s.hasContent {
				t.Fatal("content kept after a permanent failure")
			}
			// A refused request must not look like a possible duplicate on
			// the retry; an unknown outcome must.
			stub.Respond(func(testaws.SendEmail) testaws.Response { return testaws.Response{} })
			if s.status != "failed" {
				if err := d.SendEmail(ctx, args(e), attempt(2)); err != nil {
					t.Fatal(err)
				}
				if got := strings.Contains(logs.String(), "possible_duplicate_send"); got != tt.duplicit {
					t.Fatalf("possible_duplicate_send logged = %v, want %v", got, tt.duplicit)
				}
				if s := state(t, pool, e.ID); s.status != "submitted" {
					t.Fatalf("retry state = %+v, want submitted", s)
				}
			}
			for _, leak := range []string{awsText, "customer@example.test", "smtp"} {
				if strings.Contains(logs.String(), leak) || strings.Contains(stateErrors(t, pool, e.ID), leak) {
					t.Fatalf("%q stored or logged", leak)
				}
			}
		})
	}
}

func stateErrors(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), "SELECT coalesce(last_error, '') FROM emails WHERE id = $1", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDispatchRequiresVerifiedSenderAtClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	domain := input.From[strings.LastIndex(input.From, "@")+1:]
	tests := []struct {
		name     string
		identity func(t *testing.T, q *store.Queries, projectID, otherID uuid.UUID)
		sent     bool
	}{
		{"no identity", func(*testing.T, *store.Queries, uuid.UUID, uuid.UUID) {}, false},
		{"verified address", func(t *testing.T, q *store.Queries, p, _ uuid.UUID) {
			verifiedSender(t, q, p, emails.SenderAddress, strings.ToUpper(input.From))
		}, true},
		{"verified domain", func(t *testing.T, q *store.Queries, p, _ uuid.UUID) {
			verifiedSender(t, q, p, emails.SenderDomain, domain)
		}, true},
		{"subdomain only", func(t *testing.T, q *store.Queries, p, _ uuid.UUID) {
			verifiedSender(t, q, p, emails.SenderDomain, "mail."+domain)
		}, false},
		{"pending address", func(t *testing.T, q *store.Queries, p, _ uuid.UUID) {
			if _, err := emails.AddSender(ctx, q, p, emails.SenderAddress, input.From); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"other project's identity", func(t *testing.T, q *store.Queries, _, other uuid.UUID) {
			verifiedSender(t, q, other, emails.SenderAddress, input.From)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, projectID := setup(t)
			other, err := auth.CreateProject(ctx, store.New(pool), "other")
			if err != nil {
				t.Fatal(err)
			}
			e := queued(t, pool, projectID)
			tt.identity(t, store.New(pool), projectID, other.ID)
			p := &fakeProvider{send: captured}
			d := dispatcher(pool, p, nil)
			d.RequireVerifiedSenders()

			if err := d.SendEmail(ctx, args(e), attempt(1)); err != nil {
				t.Fatal(err)
			}
			s := state(t, pool, e.ID)
			if tt.sent {
				if p.calls != 1 || s.status != "captured" {
					t.Fatalf("calls = %d, state = %+v; want sent", p.calls, s)
				}
				return
			}
			if p.calls != 0 || s.status != "failed" || s.code() != "sender_not_verified" || s.hasContent {
				t.Fatalf("calls = %d, state = %+v; want failed sender_not_verified, nothing sent", p.calls, s)
			}
		})
	}
}

func TestClaimRechecksSenderIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	q := store.New(pool)
	id := verifiedSender(t, q, projectID, emails.SenderAddress, input.From).ID
	// The identity is removed after the email was loaded: the claim itself
	// must refuse it.
	if ok, err := emails.RemoveSender(ctx, q, projectID, id); err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	n, err := q.MarkSending(ctx, store.MarkSendingParams{ID: e.ID, ProjectID: projectID, RequireVerifiedSender: true})
	if err != nil || n != 0 {
		t.Fatalf("claim without identity = %d, %v; want 0", n, err)
	}
	// Without the requirement the claim is unchanged.
	if n, err := q.MarkSending(ctx, store.MarkSendingParams{ID: e.ID, ProjectID: projectID}); err != nil || n != 1 {
		t.Fatalf("claim without requirement = %d, %v; want 1", n, err)
	}
}

func TestDispatchWithoutRequirementIgnoresIdentities(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{send: captured}
	if err := dispatcher(pool, p, nil).SendEmail(context.Background(), args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); p.calls != 1 || s.status != "captured" {
		t.Fatalf("calls = %d, state = %+v", p.calls, s)
	}
}

func TestDailyLimitSnoozesHandoffs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	// Three hand-offs within the last 24 hours (one of them to another
	// provider), one older.
	for _, row := range []struct{ provider, age string }{
		{"fake", "23 hours"}, {"fake", "1 hour"}, {"other", "1 hour"}, {"fake", "25 hours"},
	} {
		old := queued(t, pool, projectID)
		if _, err := pool.Exec(ctx, `UPDATE emails SET status = 'captured', provider = $2, provider_id = 'x',
			finished_at = now() - $3::interval WHERE id = $1`, old.ID, row.provider, row.age); err != nil {
			t.Fatal(err)
		}
	}
	e := queued(t, pool, projectID)
	p := &fakeProvider{send: captured}
	d := dispatcher(pool, p, nil)
	if err := d.LimitDailyHandoffs("fake", 2); err != nil {
		t.Fatal(err)
	}

	err := d.SendEmail(ctx, args(e), attempt(1))
	wait, ok := jobs.SnoozeDelay(err)
	if !ok || p.calls != 0 {
		t.Fatalf("err = %v, calls = %d; want a snooze and no send", err, p.calls)
	}
	// The oldest counted hand-off (23 h ago) leaves the window in about an
	// hour; the snooze lasts until then (plus jitter), not a full day.
	if wait < 59*time.Minute || wait > 2*time.Hour {
		t.Fatalf("snooze = %v, want about 1h", wait)
	}
	if s := state(t, pool, e.ID); s.status != "queued" || s.attempts != 0 {
		t.Fatalf("state = %+v, want still queued without an attempt", s)
	}

	// Raising the cap lets it through.
	if err := d.LimitDailyHandoffs("fake", 3); err != nil {
		t.Fatal(err)
	}
	if err := d.SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); p.calls != 1 || s.status != "captured" {
		t.Fatalf("calls = %d, state = %+v; want sent", p.calls, s)
	}
}

func TestDailyLimitRejectsInvalid(t *testing.T) {
	t.Parallel()
	d := dispatcher(nil, &fakeProvider{send: captured}, nil)
	for _, n := range []int{-1, 0} {
		if err := d.LimitDailyHandoffs("ses", n); err == nil {
			t.Errorf("LimitDailyHandoffs(%d) accepted", n)
		}
	}
	if err := d.LimitDailyHandoffs("", 10); err == nil {
		t.Error("empty provider accepted")
	}
}
