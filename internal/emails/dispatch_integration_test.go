//go:build integration

package emails_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testmailpit"
)

type fakeProvider struct {
	mu    sync.Mutex
	calls int
	send  func(ctx context.Context, m delivery.Message) (delivery.Result, error)
}

func (f *fakeProvider) Send(ctx context.Context, m delivery.Message) (delivery.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.send(ctx, m)
}

func captured(context.Context, delivery.Message) (delivery.Result, error) {
	return delivery.Result{Provider: "fake", ProviderID: "<x@y>", Status: delivery.StatusCaptured}, nil
}

type emailState struct {
	status     string
	attempts   int
	lastError  *string
	errorCode  *string
	finished   bool
	hasContent bool
}

func state(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) emailState {
	t.Helper()
	var s emailState
	if err := pool.QueryRow(context.Background(), `
		SELECT e.status, e.attempts, e.last_error, e.error_code, e.finished_at IS NOT NULL,
		       EXISTS (SELECT 1 FROM email_contents c WHERE c.email_id = e.id)
		FROM emails e WHERE e.id = $1`, id).Scan(&s.status, &s.attempts, &s.lastError, &s.errorCode, &s.finished, &s.hasContent); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s emailState) code() string {
	if s.errorCode == nil {
		return ""
	}
	return *s.errorCode
}

// queued creates an email through the real service (job enqueue stubbed).
func queued(t *testing.T, pool *pgxpool.Pool, projectID uuid.UUID) emails.Email {
	t.Helper()
	svc := emails.NewService(pool, noopEnqueuer{}, slog.New(slog.DiscardHandler))
	e, err := svc.Create(context.Background(), projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func dispatcher(pool *pgxpool.Pool, p delivery.Provider, logs *bytes.Buffer) *emails.Dispatcher {
	logger := slog.New(slog.DiscardHandler)
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	return emails.NewDispatcher(pool, p, logger, 5*time.Second, 168*time.Hour)
}

func attempt(n int) jobs.Attempt {
	return jobs.Attempt{JobID: 1, Number: n, MaxAttempts: testMaxAttempts}
}

func args(e emails.Email) jobs.SendEmailArgs {
	return jobs.SendEmailArgs{EmailID: e.ID, ProjectID: e.ProjectID}
}

func TestDispatchToMailpitCapturesAndDeletesContent(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	mp, err := delivery.NewMailpit(testmailpit.SMTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	d := dispatcher(pool, mp, &logs)

	if err := d.SendEmail(context.Background(), args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	s := state(t, pool, e.ID)
	if s.status != "captured" || !s.finished || s.hasContent || s.attempts != 1 {
		t.Fatalf("state = %+v, want captured, finished, content deleted", s)
	}
	var provider, providerID string
	_ = pool.QueryRow(context.Background(), "SELECT provider, provider_id FROM emails WHERE id = $1", e.ID).Scan(&provider, &providerID)
	if provider != "mailpit" || providerID != "<"+e.ID.String()+"@email-platform.local>" {
		t.Fatalf("provider = %q %q", provider, providerID)
	}
	got := testmailpit.FindByMessageID(t, e.ID.String()+"@email-platform.local", 5*time.Second)
	if len(got) != 1 {
		t.Fatalf("Mailpit has %d copies, want exactly 1", len(got))
	}
	m := got[0]
	if m.From.Address != input.From || len(m.To) != 1 || m.To[0].Address != input.To || m.Subject != input.Subject ||
		strings.TrimSpace(m.Text) != input.Text || strings.TrimSpace(m.HTML) != input.HTML {
		t.Fatalf("captured message differs from the request: %+v", m)
	}
	for _, leak := range []string{"customer@example.test", "Hello"} {
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("dispatch log contains %q: %s", leak, logs.String())
		}
	}
	if s := state(t, pool, e.ID); s.status == "delivered" {
		t.Fatal("Mailpit send reported as delivered")
	}
}

func TestDispatchRecordsResultEvenIfShutdownCancelsDuringSend(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakeProvider{send: func(c context.Context, m delivery.Message) (delivery.Result, error) {
		cancel() // shutdown arrives while the provider accepts the message
		return captured(c, m)
	}}
	if err := dispatcher(pool, p, nil).SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatalf("SendEmail = %v; the accepted send must still be recorded", err)
	}
	if s := state(t, pool, e.ID); s.status != "captured" || s.hasContent {
		t.Fatalf("state = %+v, want captured with content deleted", s)
	}
}

func TestDispatchTemporaryErrorRetriesThenFailsOnFinalAttempt(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{send: func(context.Context, delivery.Message) (delivery.Result, error) {
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassConnect}
	}}
	d := dispatcher(pool, p, nil)

	err := d.SendEmail(context.Background(), args(e), attempt(1))
	if err == nil {
		t.Fatal("temporary failure must return an error so the queue retries")
	}
	if strings.Contains(err.Error(), "customer@example.test") {
		t.Fatalf("returned error leaks recipient: %v", err)
	}
	s := state(t, pool, e.ID)
	if s.status != "queued" || !s.hasContent || s.lastError == nil || *s.lastError != "delivery failed: connect" ||
		s.code() != emails.CodeProviderUnreachable {
		t.Fatalf("after attempt 1: %+v (code %q)", s, s.code())
	}

	if err := d.SendEmail(context.Background(), args(e), attempt(testMaxAttempts)); err != nil {
		t.Fatalf("final attempt must end the job, got %v", err)
	}
	if s := state(t, pool, e.ID); s.status != "failed" || s.hasContent || !s.finished || s.code() != emails.CodeProviderUnreachable {
		t.Fatalf("after final attempt: %+v (code %q)", s, s.code())
	}
}

func TestDispatchPermanentErrorFailsImmediately(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{send: func(context.Context, delivery.Message) (delivery.Result, error) {
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassRejected, Code: 550}
	}}
	if err := dispatcher(pool, p, nil).SendEmail(context.Background(), args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	s := state(t, pool, e.ID)
	if s.status != "failed" || s.hasContent || *s.lastError != "delivery failed: rejected (smtp 550)" ||
		s.code() != emails.CodeProviderRejected {
		t.Fatalf("state = %+v (code %q)", s, s.code())
	}
}

func TestDispatchLogsPossibleDuplicateAfterInterruptedAttempt(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	if _, err := pool.Exec(context.Background(), "UPDATE emails SET status = 'sending', attempts = 1 WHERE id = $1", e.ID); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	if err := dispatcher(pool, &fakeProvider{send: captured}, &logs).SendEmail(context.Background(), args(e), attempt(2)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Fatalf("interrupted attempt not logged: %s", logs.String())
	}
	if s := state(t, pool, e.ID); s.status != "captured" || s.attempts != 2 {
		t.Fatalf("state = %+v", s)
	}
}

func TestDispatchIsScopedByProject(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{send: captured}
	wrong := jobs.SendEmailArgs{EmailID: e.ID, ProjectID: uuid.New()}
	if err := dispatcher(pool, p, nil).SendEmail(context.Background(), wrong, attempt(1)); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || state(t, pool, e.ID).status != "queued" {
		t.Fatal("job with a foreign project id touched the email")
	}
}

func TestCleanupExpiresStaleEmailsAndContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	stale := queued(t, pool, projectID)
	fresh := queued(t, pool, projectID)
	if _, err := pool.Exec(ctx, "UPDATE emails SET created_at = now() - interval '8 days' WHERE id = $1", stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE email_contents SET created_at = now() - interval '8 days' WHERE email_id = $1", stale.ID); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher(pool, &fakeProvider{send: captured}, nil).Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, stale.ID); s.status != "failed" || s.hasContent || *s.lastError != "expired" || s.code() != emails.CodeExpired {
		t.Fatalf("stale = %+v", s)
	}
	if s := state(t, pool, fresh.ID); s.status != "queued" || !s.hasContent {
		t.Fatalf("fresh email touched by cleanup: %+v", s)
	}
}

func TestDispatchSkipsDisabledProject(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	if _, err := pool.Exec(context.Background(), "UPDATE projects SET status = 'disabled' WHERE id = $1", projectID); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{send: captured}
	if err := dispatcher(pool, p, nil).SendEmail(context.Background(), args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	s := state(t, pool, e.ID)
	if p.calls != 0 || s.status != "failed" || s.hasContent || *s.lastError != "project_disabled" || s.code() != emails.CodeProjectDisabled {
		t.Fatalf("disabled project still sent or kept content: calls=%d state=%+v", p.calls, s)
	}
}

func TestTemporaryFailureDoesNotTriggerDuplicateWarning(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	fail := true
	p := &fakeProvider{send: func(c context.Context, m delivery.Message) (delivery.Result, error) {
		if fail {
			fail = false
			return delivery.Result{}, &delivery.Error{Class: delivery.ClassConnect}
		}
		return captured(c, m)
	}}
	var logs bytes.Buffer
	d := dispatcher(pool, p, &logs)
	_ = d.SendEmail(context.Background(), args(e), attempt(1))
	if err := d.SendEmail(context.Background(), args(e), attempt(2)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Fatal("a cleanly failed attempt was reported as a possible duplicate")
	}
	if s := state(t, pool, e.ID); s.status != "captured" {
		t.Fatalf("state = %+v", s)
	}
}

func TestLateRetryAfterSuccessDoesNotResend(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{send: captured}
	d := dispatcher(pool, p, nil)
	for n := 1; n <= 2; n++ {
		if err := d.SendEmail(context.Background(), args(e), attempt(n)); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls != 1 {
		t.Fatalf("provider called %d times, want 1", p.calls)
	}
}

func TestTimeoutKeepsSendingAndRetryWarnsAboutDuplicate(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	first := true
	p := &fakeProvider{send: func(c context.Context, m delivery.Message) (delivery.Result, error) {
		if first {
			first = false
			return delivery.Result{}, &delivery.Error{Class: delivery.ClassTimeout}
		}
		return captured(c, m)
	}}
	var logs bytes.Buffer
	d := dispatcher(pool, p, &logs)
	if err := d.SendEmail(context.Background(), args(e), attempt(1)); err == nil {
		t.Fatal("timeout must be retried")
	}
	if s := state(t, pool, e.ID); s.status != "sending" || s.code() != emails.CodeProviderTimeout {
		t.Fatalf("after timeout: %+v (code %q), want sending with provider_timeout (outcome unknown)", s, s.code())
	}
	if err := d.SendEmail(context.Background(), args(e), attempt(2)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Fatal("retry after an unknown outcome must warn about a possible duplicate")
	}
	if s := state(t, pool, e.ID); s.status != "captured" || s.lastError != nil || s.errorCode != nil {
		t.Fatalf("success must clear the retry reason: %+v", s)
	}
}

func TestCleanupDuringSendWinsOverLateSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{}
	d := dispatcher(pool, p, nil)
	p.send = func(c context.Context, m delivery.Message) (delivery.Result, error) {
		// The safety net expires the email while the provider call runs.
		if _, err := pool.Exec(ctx, "UPDATE emails SET created_at = now() - interval '8 days' WHERE id = $1", e.ID); err != nil {
			t.Error(err)
		}
		if err := d.Cleanup(ctx); err != nil {
			t.Error(err)
		}
		return captured(c, m)
	}
	if err := d.SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); s.status != "failed" || *s.lastError != "expired" || s.code() != emails.CodeExpired {
		t.Fatalf("late success overwrote the terminal state: %+v", s)
	}
}

func TestMissingContentFailsWithoutSending(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	if _, err := pool.Exec(context.Background(), "DELETE FROM email_contents WHERE email_id = $1", e.ID); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{send: captured}
	if err := dispatcher(pool, p, nil).SendEmail(context.Background(), args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); p.calls != 0 || s.status != "failed" || *s.lastError != "content_missing" || s.code() != emails.CodeContentMissing {
		t.Fatalf("calls=%d state=%+v", p.calls, s)
	}
}

func TestStateUpdatesNeverLeaveTerminalStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	if err := dispatcher(pool, &fakeProvider{send: captured}, nil).SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	q := store.New(pool)
	reason, code := "late failure", emails.CodeInternalError
	key := struct{ id, project uuid.UUID }{e.ID, projectID}
	if n, _ := q.MarkFailed(ctx, store.MarkFailedParams{ID: key.id, ProjectID: key.project, LastError: &reason, ErrorCode: &code}); n != 0 {
		t.Fatal("MarkFailed changed a captured email")
	}
	if n, _ := q.MarkSending(ctx, store.MarkSendingParams{ID: key.id, ProjectID: key.project}); n != 0 {
		t.Fatal("MarkSending changed a captured email")
	}
	// A slow rescued attempt reporting an error after another attempt captured
	// the email must not reopen it.
	if err := q.RequeueAfterError(ctx, store.RequeueAfterErrorParams{ID: key.id, ProjectID: key.project, LastError: &reason, ErrorCode: &code}); err != nil {
		t.Fatal(err)
	}
	if err := q.RecordAttemptError(ctx, store.RecordAttemptErrorParams{ID: key.id, ProjectID: key.project, LastError: &reason, ErrorCode: &code}); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); s.status != "captured" || s.lastError != nil || s.errorCode != nil {
		t.Fatalf("terminal email changed: %+v", s)
	}
}

// Errors outside the provider call (here: the database is unreachable for
// this attempt) are retried, and on the final attempt the email fails with
// internal_error instead of staying open until the expiry safety net.
func TestInternalErrorOnFinalAttemptFailsEmail(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{send: captured}
	d := dispatcher(pool, p, nil)
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	if err := d.SendEmail(gone, args(e), attempt(1)); err == nil {
		t.Fatal("an internal error before the final attempt must be retried")
	}
	if s := state(t, pool, e.ID); s.status != "queued" || !s.hasContent {
		t.Fatalf("after a non-final internal error: %+v", s)
	}

	if err := d.SendEmail(gone, args(e), attempt(testMaxAttempts)); err != nil {
		t.Fatalf("final attempt must end the job, got %v", err)
	}
	if s := state(t, pool, e.ID); p.calls != 0 || s.status != "failed" || s.hasContent || s.code() != emails.CodeInternalError {
		t.Fatalf("calls=%d state=%+v (code %q), want failed/internal_error", p.calls, s, s.code())
	}
}

// The duplicate warning is only for an attempt that actually sends again,
// not for one that stops before the provider.
func TestNoDuplicateWarningWhenNothingIsSent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	if _, err := pool.Exec(ctx, "UPDATE emails SET status = 'sending', attempts = 1 WHERE id = $1", e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM email_contents WHERE email_id = $1", e.ID); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	p := &fakeProvider{send: captured}
	if err := dispatcher(pool, p, &logs).SendEmail(ctx, args(e), attempt(2)); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Fatalf("calls=%d; logs: %s", p.calls, logs.String())
	}
}
