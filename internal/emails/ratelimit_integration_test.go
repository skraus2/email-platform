//go:build integration

package emails_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
)

func limitedService(t *testing.T, pool *pgxpool.Pool, perMinute int) *emails.Service {
	t.Helper()
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	return emails.NewService(pool, enq, slog.New(slog.DiscardHandler), perMinute)
}

func rateLimited(t *testing.T, err error) bool {
	t.Helper()
	var rl *emails.RateLimitError
	if !errors.As(err, &rl) {
		return false
	}
	if rl.RetryAfter < time.Second || rl.RetryAfter > time.Minute {
		t.Errorf("RetryAfter = %v, want 1s–60s", rl.RetryAfter)
	}
	return true
}

// freshWindow waits until at least 3 s remain in the current minute, so a
// test's sends all fall into one window.
func freshWindow(t *testing.T) {
	t.Helper()
	if s := time.Now().Second(); s > 56 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
}

func TestRateLimitPerProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	other, err := auth.CreateProject(ctx, store.New(pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	svc := limitedService(t, pool, 3)
	freshWindow(t)
	for i := range 3 {
		if _, err := svc.Create(ctx, projectID, input); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	if _, err := svc.Create(ctx, projectID, input); !rateLimited(t, err) {
		t.Fatalf("4th send: err = %v, want RateLimitError", err)
	}
	if _, err := svc.Create(ctx, other.ID, input); err != nil {
		t.Fatalf("other project limited by this one: %v", err)
	}
	if got := counts(t, pool); got.emails != 4 || got.jobs != 4 {
		t.Errorf("rows = %+v, want 4 emails/jobs (refused send stores nothing)", got)
	}
}

func TestRateLimitUnderConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := limitedService(t, pool, 5)
	freshWindow(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, errs[i] = svc.Create(ctx, projectID, input)
		})
	}
	close(start)
	wg.Wait()
	ok, limited := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case rateLimited(t, err):
			limited++
		default:
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if ok != 5 || limited != 15 {
		t.Errorf("accepted %d, limited %d; want 5 and 15 (no over-admission)", ok, limited)
	}
	if got := counts(t, pool); got.emails != 5 {
		t.Errorf("emails = %d, want 5", got.emails)
	}
}

func TestRateLimitIgnoresRefusedRequestsAndReplays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := limitedService(t, pool, 2)
	freshWindow(t)

	bad := input
	bad.To = "not-an-address"
	for range 3 {
		if _, err := svc.Create(ctx, projectID, bad); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	first, err := svc.Create(ctx, projectID, keyed("k1"))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := svc.Create(ctx, projectID, keyed("k1"))
		if err != nil || !again.Replayed || again.ID != first.ID {
			t.Fatalf("replay = %+v, %v", again, err)
		}
	}
	conflict := keyed("k1")
	conflict.Subject = "Other"
	if _, err := svc.Create(ctx, projectID, conflict); !errors.Is(err, emails.ErrIdempotencyKeyReused) {
		t.Fatalf("conflict err = %v", err)
	}
	if _, err := svc.Create(ctx, projectID, input); err != nil {
		t.Fatalf("second accepted send refused: %v (refusals or replays were counted)", err)
	}
	if _, err := svc.Create(ctx, projectID, input); !rateLimited(t, err) {
		t.Fatalf("third accepted send: err = %v, want RateLimitError", err)
	}
}

// The loser of a same-key race must replay even when the winner used up the
// project's last slot.
func TestIdempotentRaceWithExhaustedLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := limitedService(t, pool, 1)
	freshWindow(t)

	const n = 20
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, n)
	replayed := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			var e emails.Email
			e, errs[i] = svc.Create(ctx, projectID, keyed("race"))
			ids[i], replayed[i] = e.ID, e.Replayed
		})
	}
	close(start)
	wg.Wait()
	fresh := 0
	for i := range n {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("request %d: id %s err %v; want every request to get the one email", i, ids[i], errs[i])
		}
		if !replayed[i] {
			fresh++
		}
	}
	if fresh != 1 {
		t.Errorf("%d requests created the email, want 1", fresh)
	}
	if got := counts(t, pool); got.emails != 1 {
		t.Errorf("emails = %d, want 1", got.emails)
	}
}

func TestCleanupDeletesOldRateLimitWindows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := limitedService(t, pool, 10)
	if _, err := svc.Create(ctx, projectID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rate_limit_windows (project_id, window_start, count)
		VALUES ($1, date_trunc('minute', now()) - interval '2 hours', 7)`, projectID); err != nil {
		t.Fatal(err)
	}
	d := emails.NewDispatcher(pool, nil, slog.New(slog.DiscardHandler), time.Second, time.Hour)
	if err := d.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "SELECT count(*) FROM rate_limit_windows"); n != 1 {
		t.Errorf("windows after cleanup = %d, want only the current one", n)
	}
}

// A refused request must not do write work or wait for the project's window
// row: with the window full and its row locked by another transaction, a new
// request is refused at once.
func TestRefusalDoesNotWaitForWindowLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := limitedService(t, pool, 1)
	freshWindow(t)
	if _, err := svc.Create(ctx, projectID, input); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT * FROM rate_limit_windows WHERE project_id = $1 FOR UPDATE", projectID); err != nil {
		t.Fatal(err)
	}
	quick, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := svc.Create(quick, projectID, input); !rateLimited(t, err) {
		t.Fatalf("err = %v, want an immediate RateLimitError", err)
	}
}
