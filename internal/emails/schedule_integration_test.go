//go:build integration

package emails_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
)

func newScheduleService(t *testing.T) (*emails.Service, uuid.UUID, func(string) int) {
	t.Helper()
	pool, projectID := setup(t)
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	svc := emails.NewService(pool, enq, slog.New(slog.DiscardHandler), unlimited)
	return svc, projectID, func(q string) int { return count(t, pool, q) }
}

func TestScheduledSend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, projectID, n := newScheduleService(t)

	at := time.Now().Add(2 * time.Hour).Truncate(time.Microsecond)
	in := input
	in.ScheduledAt = &at
	e, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "queued" || e.ScheduledAt == nil || !e.ScheduledAt.Equal(at) {
		t.Fatalf("scheduled email: %+v", e)
	}
	// The dispatch job waits for the due time.
	if n("SELECT count(*) FROM river_job WHERE kind = 'send_email' AND state = 'scheduled' AND scheduled_at >= now() + interval '119 minutes'") != 1 {
		t.Fatal("job not scheduled for the due time")
	}
	got, err := svc.Get(ctx, projectID, e.ID)
	if err != nil || got.ScheduledAt == nil || !got.ScheduledAt.Equal(at) {
		t.Fatalf("get: %+v %v", got, err)
	}

	// A time at or before now sends immediately and is stored as null.
	past := time.Now().Add(-time.Hour)
	in.ScheduledAt = &past
	e, err = svc.Create(ctx, projectID, in)
	if err != nil || e.ScheduledAt != nil {
		t.Fatalf("past time: %+v %v", e, err)
	}
	if n("SELECT count(*) FROM river_job WHERE kind = 'send_email' AND state = 'available'") != 1 {
		t.Fatal("past time did not give an immediate job")
	}

	// Beyond the horizon: refused, nothing stored.
	far := time.Now().Add(emails.DefaultScheduleMaxAhead + time.Minute)
	in.ScheduledAt = &far
	var ve *emails.ValidationError
	if _, err := svc.Create(ctx, projectID, in); !errors.As(err, &ve) || ve.Field != "scheduled_at" {
		t.Fatalf("beyond the horizon: %v", err)
	}
	if n("SELECT count(*) FROM emails") != 2 {
		t.Fatal("refused request stored an email")
	}
	svc.LimitScheduleAhead(time.Hour)
	in.ScheduledAt = &at
	if _, err := svc.Create(ctx, projectID, in); !errors.As(err, &ve) || ve.Field != "scheduled_at" {
		t.Fatalf("beyond a shorter horizon: %v", err)
	}
}

func TestScheduledIdempotency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, projectID, n := newScheduleService(t)
	at := time.Now().Add(time.Hour)
	in := input
	in.IdempotencyKey = "sched-1"
	in.ScheduledAt = &at
	first, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Create(ctx, projectID, in)
	if err != nil || !again.Replayed || again.ID != first.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	later := at.Add(time.Minute)
	in.ScheduledAt = &later
	if _, err := svc.Create(ctx, projectID, in); !errors.Is(err, emails.ErrIdempotencyKeyReused) {
		t.Fatalf("other time, same key: %v", err)
	}
	in.ScheduledAt = nil
	if _, err := svc.Create(ctx, projectID, in); !errors.Is(err, emails.ErrIdempotencyKeyReused) {
		t.Fatalf("unscheduled, same key: %v", err)
	}
	if n("SELECT count(*) FROM emails") != 1 {
		t.Fatal("duplicates stored")
	}
}

func TestCancelEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	other, err := auth.CreateProject(ctx, store.New(pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	svc := emails.NewService(pool, enq, slog.New(slog.DiscardHandler), unlimited)
	var changes []events.Change
	svc.Recorder().OnStatusChange(func(_ context.Context, _ pgx.Tx, c events.Change) error {
		changes = append(changes, c)
		return nil
	})

	at := time.Now().Add(time.Hour)
	in := input
	in.ScheduledAt = &at
	e, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, other.ID, e.ID); !errors.Is(err, emails.ErrNotFound) {
		t.Fatalf("foreign cancel: %v", err)
	}
	c, err := svc.Cancel(ctx, projectID, e.ID)
	if err != nil || c.Status != "canceled" || c.FinishedAt == nil || c.ErrorCode != nil {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	if count(t, pool, "SELECT count(*) FROM email_contents") != 0 {
		t.Fatal("content kept after cancel")
	}
	if count(t, pool, "SELECT count(*) FROM email_events WHERE type = 'canceled' AND origin = 'platform'") != 1 ||
		len(changes) != 1 || changes[0].Status != "canceled" || changes[0].EmailID != e.ID {
		t.Fatalf("cancel event/hook: %+v", changes)
	}
	if _, err := svc.Cancel(ctx, projectID, e.ID); !errors.Is(err, emails.ErrNotCancelable) {
		t.Fatalf("second cancel: %v", err)
	}
	if _, err := svc.Cancel(ctx, projectID, uuid.New()); !errors.Is(err, emails.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	// An email a worker claimed cannot be cancelled.
	e2, err := svc.Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE emails SET status = 'sending' WHERE id = $1", e2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, projectID, e2.ID); !errors.Is(err, emails.ErrNotCancelable) {
		t.Fatalf("sending: %v", err)
	}
	// A queued email waiting for a retry carries the last error code; a
	// cancelled one never does.
	e3, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE emails SET error_code = 'provider_unreachable', last_error = 'delivery failed: connect' WHERE id = $1", e3.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := svc.Cancel(ctx, projectID, e3.ID); err != nil || c.Status != "canceled" || c.ErrorCode != nil {
		t.Fatalf("cancel after a failed attempt: %+v %v", c, err)
	}
	page, _, err := svc.List(ctx, projectID, emails.ListFilter{Statuses: []string{"canceled"}}, 10)
	if err != nil || len(page) != 2 || page[1].ID != e.ID {
		t.Fatalf("list canceled: %+v %v", page, err)
	}
}

func TestCleanupMeasuresFromDueTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	svc := emails.NewService(pool, enq, slog.New(slog.DiscardHandler), unlimited)
	at := time.Now().Add(48 * time.Hour)
	in := input
	in.ScheduledAt = &at
	sched, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	now, err := svc.Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	// Both were created three days ago; only the unscheduled one is overdue.
	if _, err := pool.Exec(ctx, "UPDATE emails SET created_at = now() - interval '72 hours'"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE email_contents SET created_at = now() - interval '72 hours'"); err != nil {
		t.Fatal(err)
	}
	d := emails.NewDispatcher(pool, nil, slog.New(slog.DiscardHandler), time.Second, 24*time.Hour)
	if err := d.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, projectID, sched.ID); got.Status != "queued" {
		t.Fatalf("scheduled email expired before it was due: %s", got.Status)
	}
	if got, _ := svc.Get(ctx, projectID, now.ID); got.Status != "failed" {
		t.Fatalf("overdue email: %s", got.Status)
	}
	if count(t, pool, "SELECT count(*) FROM email_contents WHERE email_id = '"+sched.ID.String()+"'") != 1 ||
		count(t, pool, "SELECT count(*) FROM email_contents WHERE email_id = '"+now.ID.String()+"'") != 0 {
		t.Fatal("content safety net ignored the due time")
	}
}

func TestScheduledOpenCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, projectID, n := newScheduleService(t)
	svc.LimitScheduledOpen(2)
	at := time.Now().Add(time.Hour)
	in := input
	in.ScheduledAt = &at
	var ids []uuid.UUID
	for range 2 {
		e, err := svc.Create(ctx, projectID, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	if _, err := svc.Create(ctx, projectID, in); !errors.Is(err, emails.ErrScheduleLimit) {
		t.Fatalf("over the cap: %v", err)
	}
	// Immediate sends are not planned sends.
	if _, err := svc.Create(ctx, projectID, input); err != nil {
		t.Fatalf("immediate send at the cap: %v", err)
	}
	// A retry of an accepted planned send still replays at the cap.
	keyed := in
	keyed.IdempotencyKey = "k"
	if _, err := svc.Cancel(ctx, projectID, ids[0]); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Create(ctx, projectID, keyed)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := svc.Create(ctx, projectID, keyed); err != nil || !again.Replayed || again.ID != first.ID {
		t.Fatalf("replay at the cap: %+v %v", again, err)
	}
	// So does one after the horizon was lowered.
	svc.LimitScheduleAhead(time.Minute)
	if again, err := svc.Create(ctx, projectID, keyed); err != nil || !again.Replayed {
		t.Fatalf("replay after a shorter horizon: %+v %v", again, err)
	}
	if n("SELECT count(*) FROM emails WHERE scheduled_at IS NOT NULL AND status = 'queued'") != 2 {
		t.Fatal("cap not held")
	}
}
