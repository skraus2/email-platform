//go:build integration

package emails_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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

func newService(t *testing.T, pool *pgxpool.Pool, logs *bytes.Buffer) *emails.Service {
	t.Helper()
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	return emails.NewService(pool, enq, logger, unlimited)
}

func keyed(key string) emails.CreateInput {
	in := input
	in.IdempotencyKey = key
	return in
}

type rowCounts struct{ emails, contents, jobs, keys int }

func counts(t *testing.T, pool *pgxpool.Pool) rowCounts {
	t.Helper()
	return rowCounts{
		emails:   count(t, pool, "SELECT count(*) FROM emails"),
		contents: count(t, pool, "SELECT count(*) FROM email_contents"),
		jobs:     count(t, pool, "SELECT count(*) FROM river_job WHERE kind = 'send_email'"),
		keys:     count(t, pool, "SELECT count(*) FROM idempotency_keys"),
	}
}

func TestIdempotentConcurrentCreates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	var logs bytes.Buffer
	svc := newService(t, pool, &logs)

	const n = 20
	var wg sync.WaitGroup
	results := make([]emails.Email, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			results[i], errs[i] = svc.Create(ctx, projectID, keyed("welcome-user-123"))
		})
	}
	close(start)
	wg.Wait()

	created := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if results[i].ID != results[0].ID {
			t.Fatalf("request %d got id %s, request 0 got %s", i, results[i].ID, results[0].ID)
		}
		if !results[i].Replayed {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d requests reported a new email, want exactly 1", created)
	}
	if got, want := counts(t, pool), (rowCounts{1, 1, 1, 1}); got != want {
		t.Errorf("rows = %+v, want %+v", got, want)
	}
	if strings.Contains(logs.String(), "welcome-user-123") {
		t.Error("idempotency key value appears in the logs")
	}
}

func TestIdempotencyReplayReturnsOriginal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)

	first, err := svc.Create(ctx, projectID, keyed("k1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed {
		t.Error("first request reported as replay")
	}
	// The email moves on; the replay still names the same email.
	if _, err := pool.Exec(ctx, "UPDATE emails SET status = 'sending' WHERE id = $1", first.ID); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Create(ctx, projectID, keyed("k1"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.ID != first.ID || again.ProjectID != projectID {
		t.Errorf("replay = %+v, want replay of %s", again, first.ID)
	}
	if got := counts(t, pool); got.emails != 1 || got.jobs != 1 {
		t.Errorf("rows = %+v, want one email and one job", got)
	}
}

func TestIdempotencyConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	if _, err := svc.Create(ctx, projectID, keyed("k1")); err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*emails.CreateInput){
		"from":    func(in *emails.CreateInput) { in.From = "other@example.test" },
		"to":      func(in *emails.CreateInput) { in.To = "other@example.test" },
		"subject": func(in *emails.CreateInput) { in.Subject = "Other" },
		"text":    func(in *emails.CreateInput) { in.Text = "Other" },
		"html":    func(in *emails.CreateInput) { in.HTML = "" },
	}
	for field, change := range changes {
		in := keyed("k1")
		change(&in)
		if _, err := svc.Create(ctx, projectID, in); !errors.Is(err, emails.ErrIdempotencyKeyReused) {
			t.Errorf("changed %s: err = %v, want ErrIdempotencyKeyReused", field, err)
		}
	}
	if got, want := counts(t, pool), (rowCounts{1, 1, 1, 1}); got != want {
		t.Errorf("rows = %+v, want %+v (conflicts store nothing)", got, want)
	}
}

func TestIdempotencyConcurrentDifferentPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)

	const n = 20
	var wg sync.WaitGroup
	results := make([]emails.Email, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			in := keyed("shared")
			if i%2 == 1 {
				in.Subject = "Different"
			}
			<-start
			results[i], errs[i] = svc.Create(ctx, projectID, in)
		})
	}
	close(start)
	wg.Wait()

	var winner uuid.UUID
	ok, conflicts := 0, 0
	for i := range n {
		switch {
		case errs[i] == nil:
			if winner == uuid.Nil {
				winner = results[i].ID
			}
			if results[i].ID != winner {
				t.Fatalf("two different emails were accepted: %s and %s", winner, results[i].ID)
			}
			ok++
		case errors.Is(errs[i], emails.ErrIdempotencyKeyReused):
			conflicts++
		default:
			t.Fatalf("request %d: %v", i, errs[i])
		}
	}
	if ok != n/2 || conflicts != n/2 {
		t.Errorf("accepted %d, conflicts %d; want %d each (one payload wins)", ok, conflicts, n/2)
	}
	if got, want := counts(t, pool), (rowCounts{1, 1, 1, 1}); got != want {
		t.Errorf("rows = %+v, want %+v", got, want)
	}
}

func TestIdempotencyScopeAndAbsence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectA := setup(t)
	b, err := auth.CreateProject(ctx, store.New(pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, pool, nil)

	ea, err := svc.Create(ctx, projectA, keyed("same"))
	if err != nil {
		t.Fatal(err)
	}
	eb, err := svc.Create(ctx, b.ID, keyed("same"))
	if err != nil {
		t.Fatal(err)
	}
	if ea.ID == eb.ID || eb.Replayed || eb.ProjectID != b.ID {
		t.Errorf("same key in another project must create its own email: a=%+v b=%+v", ea, eb)
	}

	// Without a key nothing is deduplicated.
	n1, err := svc.Create(ctx, projectA, input)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := svc.Create(ctx, projectA, input)
	if err != nil {
		t.Fatal(err)
	}
	if n1.ID == n2.ID || n1.Replayed || n2.Replayed {
		t.Error("requests without a key were deduplicated")
	}
	if got := counts(t, pool); got.emails != 4 || got.keys != 2 {
		t.Errorf("rows = %+v, want 4 emails and 2 keys", got)
	}
}

func TestIdempotencyRejectsInvalidKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	for _, k := range []string{"a b", strings.Repeat("k", 256), "schlüssel"} {
		if _, err := svc.Create(ctx, projectID, keyed(k)); !errors.Is(err, emails.ErrInvalidIdempotencyKey) {
			t.Errorf("key %q: err = %v, want ErrInvalidIdempotencyKey", k, err)
		}
	}
	if got := counts(t, pool); got != (rowCounts{}) {
		t.Errorf("rows = %+v, want nothing stored", got)
	}
}

func TestIdempotencyKeyExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)

	first, err := svc.Create(ctx, projectID, keyed("old"))
	if err != nil {
		t.Fatal(err)
	}
	backdate := func(key string, age time.Duration) {
		t.Helper()
		if _, err := pool.Exec(ctx, "UPDATE idempotency_keys SET created_at = now() - make_interval(secs => $2) WHERE key = $1",
			key, age.Seconds()); err != nil {
			t.Fatal(err)
		}
	}

	// Just inside the lifetime: still a replay.
	backdate("old", emails.IdempotencyKeyTTL-time.Minute)
	if again, err := svc.Create(ctx, projectID, keyed("old")); err != nil || again.ID != first.ID {
		t.Fatalf("within TTL: %+v, %v; want replay of %s", again, err, first.ID)
	}

	// Past the lifetime: the key is free again, even with another payload.
	backdate("old", emails.IdempotencyKeyTTL+time.Minute)
	in := keyed("old")
	in.Subject = "New"
	second, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Replayed {
		t.Errorf("expired key replayed %s", second.ID)
	}
	var points uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT email_id FROM idempotency_keys WHERE key = 'old'").Scan(&points); err != nil || points != second.ID {
		t.Errorf("key points at %s (%v), want %s", points, err, second.ID)
	}

	// Cleanup removes expired keys and keeps live ones.
	if _, err := svc.Create(ctx, projectID, keyed("expired")); err != nil {
		t.Fatal(err)
	}
	backdate("expired", emails.IdempotencyKeyTTL+time.Minute)
	d := emails.NewDispatcher(pool, nil, slog.New(slog.DiscardHandler), time.Second, time.Hour)
	if err := d.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var keys []string
	rows, err := pool.Query(ctx, "SELECT key FROM idempotency_keys ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if strings.Join(keys, ",") != "old" {
		t.Errorf("keys after cleanup = %v, want [old]", keys)
	}
}
