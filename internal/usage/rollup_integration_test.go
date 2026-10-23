//go:build integration

package usage_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
	"github.com/skraus2/email-platform/internal/usage"
)

const day = 24 * time.Hour

type fixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	today time.Time // start of the current UTC day
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	now := time.Now().UTC()
	return &fixture{t: t, pool: testdb.NewMigrated(t), now: now, today: now.Truncate(day)}
}

func (f *fixture) project(name string) uuid.UUID {
	f.t.Helper()
	p, err := auth.CreateProject(context.Background(), store.New(f.pool), name)
	if err != nil {
		f.t.Fatal(err)
	}
	return p.ID
}

// email inserts an email accepted at created.
func (f *fixture) email(project uuid.UUID, created time.Time) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	err := f.pool.QueryRow(context.Background(), `INSERT INTO emails (project_id, sender, recipient, subject, status, created_at)
		VALUES ($1, 'app@example.test', 'user@example.test', 'Hi', 'sending', $2) RETURNING id`, project, created).Scan(&id)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

// event inserts an event of typ received at received; SES events get a
// unique provider event id. bounceType is stored in the details when set.
func (f *fixture) event(project, email uuid.UUID, typ string, received time.Time, bounceType string) {
	f.t.Helper()
	origin, provider, providerEventID := "platform", (*string)(nil), (*string)(nil)
	switch typ {
	case "captured", "submitted", "failed", "canceled":
	default:
		ses, id := "ses", uuid.NewString()
		origin, provider, providerEventID = ses, &ses, &id
	}
	details := "{}"
	if bounceType != "" {
		details = fmt.Sprintf(`{"bounce_type": %q}`, bounceType)
	}
	_, err := f.pool.Exec(context.Background(), `INSERT INTO email_events (project_id, email_id, origin, type, provider, provider_event_id, occurred_at, received_at, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8::jsonb)`, project, email, origin, typ, provider, providerEventID, received, details)
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) rollup() usage.Result {
	f.t.Helper()
	res, err := usage.Rollup(context.Background(), f.pool, f.now)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

type counts struct{ Accepted, Sent, Failed, Canceled, Delivered, Bounced, Complained int64 }

// rows returns the stored usage of project by day offset from today.
func (f *fixture) rows(project uuid.UUID) map[int]counts {
	f.t.Helper()
	rs, err := f.pool.Query(context.Background(), `SELECT day, accepted, sent, failed, canceled, delivered, bounced, complained
		FROM usage_daily WHERE project_id = $1`, project)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rs.Close()
	out := map[int]counts{}
	for rs.Next() {
		var d time.Time
		var c counts
		if err := rs.Scan(&d, &c.Accepted, &c.Sent, &c.Failed, &c.Canceled, &c.Delivered, &c.Bounced, &c.Complained); err != nil {
			f.t.Fatal(err)
		}
		out[int(d.Sub(f.today)/day)] = c
	}
	if err := rs.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func assertRows(t *testing.T, got, want map[int]counts) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	for d, w := range want {
		if got[d] != w {
			t.Errorf("day %+d = %+v, want %+v", d, got[d], w)
		}
	}
}

func TestRollupCountsPerDay(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.project("a"), f.project("b")
	yesterday := f.today.Add(-day + time.Hour)
	today := f.today.Add(time.Minute)

	e1 := f.email(a, yesterday)
	e2 := f.email(a, yesterday.Add(time.Hour))
	e3 := f.email(a, today)
	f.event(a, e1, "submitted", yesterday.Add(time.Minute), "")
	f.event(a, e2, "failed", today, "")
	f.event(a, e1, "delivered", today, "")
	f.event(a, e1, "delivered", today.Add(time.Second), "") // a second SES event for the same email and day
	f.event(a, e1, "bounced", today, "Permanent")
	f.event(a, e3, "captured", today, "")
	f.event(a, e3, "bounced", today, "Transient") // not a hard bounce
	f.event(a, e3, "complained", today, "")
	f.event(a, e3, "rejected", today, "")         // not counted anywhere
	f.event(a, e3, "delivery_delayed", today, "") // nor this
	e4 := f.email(a, today)
	f.event(a, e4, "canceled", today, "")
	eb := f.email(b, today)
	f.event(b, eb, "captured", today, "")

	f.rollup()
	assertRows(t, f.rows(a), map[int]counts{
		-1: {Accepted: 2, Sent: 1},
		0:  {Accepted: 2, Sent: 1, Failed: 1, Canceled: 1, Delivered: 1, Bounced: 1, Complained: 1},
	})
	assertRows(t, f.rows(b), map[int]counts{0: {Accepted: 1, Sent: 1}})
}

func TestRollupIdempotentAndConcurrent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.project("a")
	for i := range 20 {
		e := f.email(a, f.today.Add(time.Duration(i)*time.Minute))
		f.event(a, e, "captured", f.today.Add(time.Duration(i)*time.Minute+time.Second), "")
	}
	f.email(a, f.today.Add(-day))
	want := map[int]counts{-1: {Accepted: 1}, 0: {Accepted: 20, Sent: 20}}
	// More projects and days, so concurrent runs upsert many keys.
	others := make([]uuid.UUID, 5)
	for i := range others {
		others[i] = f.project(fmt.Sprintf("other-%d", i))
		for d := range 3 {
			f.email(others[i], f.today.Add(-time.Duration(d)*day+time.Hour))
		}
	}
	wantOther := map[int]counts{-2: {Accepted: 1}, -1: {Accepted: 1}, 0: {Accepted: 1}}

	f.rollup()
	f.rollup()
	assertRows(t, f.rows(a), want)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			if _, err := usage.Rollup(context.Background(), f.pool, f.now); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent rollup: %v", err)
	}
	assertRows(t, f.rows(a), want)
	for _, o := range others {
		assertRows(t, f.rows(o), wantOther)
	}

	// New activity raises the counts of its day.
	e := f.email(a, f.today.Add(2*time.Hour))
	f.event(a, e, "failed", f.today.Add(2*time.Hour), "")
	f.rollup()
	assertRows(t, f.rows(a), map[int]counts{-1: {Accepted: 1}, 0: {Accepted: 21, Sent: 20, Failed: 1}})
}

func TestRollupNeverDecreases(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.project("a")
	// An old email whose complaint arrives today: retention deletes the
	// email (by its finish time) and, by cascade, today's complaint.
	old := f.email(a, f.today.Add(-40*day))
	f.event(a, old, "complained", f.today.Add(time.Minute), "")
	// Other activity of the same day, so every run writes the row again.
	e := f.email(a, f.today.Add(time.Minute))
	f.event(a, e, "captured", f.today.Add(2*time.Minute), "")
	f.rollup()
	assertRows(t, f.rows(a), map[int]counts{0: {Accepted: 1, Sent: 1, Complained: 1}})

	if _, err := f.pool.Exec(context.Background(), `DELETE FROM emails WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	f.rollup()
	assertRows(t, f.rows(a), map[int]counts{0: {Accepted: 1, Sent: 1, Complained: 1}})

	// A count above what the data shows (a fresher snapshot already stored)
	// is kept by a run that computes less.
	if _, err := f.pool.Exec(context.Background(), `UPDATE usage_daily SET sent = 7 WHERE project_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	f.rollup()
	assertRows(t, f.rows(a), map[int]counts{0: {Accepted: 1, Sent: 7, Complained: 1}})
}

func TestRollupSurvivesRetention(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.project("a")
	twoDaysAgo := f.today.Add(-2*day + time.Hour)
	e := f.email(a, twoDaysAgo)
	f.event(a, e, "captured", twoDaysAgo, "")
	f.rollup()

	if _, err := f.pool.Exec(context.Background(), `DELETE FROM emails WHERE project_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	// Another email accepted on the same day keeps the row in the rewrite.
	f.email(a, twoDaysAgo.Add(time.Minute))
	f.rollup()
	assertRows(t, f.rows(a), map[int]counts{-2: {Accepted: 1, Sent: 1}})
}

func TestRollupWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.project("a")
	f.email(a, f.today)
	// No rollup yet: the first run reaches back to the 14-day cap.
	if res := f.rollup(); !res.From.Equal(f.today.Add(-13 * day)) {
		t.Errorf("first run from %v, want the 14-day cap %v", res.From, f.today.Add(-13*day))
	}
	f.email(a, f.today.Add(-3*day+time.Hour)) // before the regular window

	res := f.rollup()
	if !res.From.Equal(f.today.Add(-2 * day)) {
		t.Errorf("from = %v, want %v", res.From, f.today.Add(-2*day))
	}
	assertRows(t, f.rows(a), map[int]counts{0: {Accepted: 1}})
}

func TestRollupCatchesUpAfterOutage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.project("a")
	f.email(a, f.today.Add(-6*day+time.Hour))
	f.rollup()
	// The last run was six days ago; emails accepted since were not counted.
	if _, err := f.pool.Exec(context.Background(), `UPDATE usage_daily SET computed_at = $1`, f.now.Add(-6*day)); err != nil {
		t.Fatal(err)
	}
	f.email(a, f.today.Add(-5*day+time.Hour))
	f.email(a, f.today.Add(-20*day)) // beyond the 14-day cap

	res := f.rollup()
	if !res.From.Equal(f.today.Add(-6 * day)) {
		t.Errorf("from = %v, want the day of the last run %v", res.From, f.today.Add(-6*day))
	}
	assertRows(t, f.rows(a), map[int]counts{-6: {Accepted: 1}, -5: {Accepted: 1}})

	if _, err := f.pool.Exec(context.Background(), `UPDATE usage_daily SET computed_at = $1`, f.now.Add(-30*day)); err != nil {
		t.Fatal(err)
	}
	if res := f.rollup(); !res.From.Equal(f.today.Add(-13 * day)) {
		t.Errorf("from = %v, want the 14-day cap %v", res.From, f.today.Add(-13*day))
	}
	assertRows(t, f.rows(a), map[int]counts{-6: {Accepted: 1}, -5: {Accepted: 1}})
}
