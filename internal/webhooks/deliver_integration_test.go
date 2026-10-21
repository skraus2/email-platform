//go:build integration

package webhooks

import (
	"bytes"
	"context"
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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
)

// receiver records requests and answers with the next queued status.
type receiver struct {
	mu       sync.Mutex
	statuses []int
	delay    time.Duration
	requests []*http.Request
	bodies   [][]byte
	srv      *httptest.Server
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	t.Helper()
	r := &receiver{statuses: statuses}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, req)
		r.bodies = append(r.bodies, body)
		status := http.StatusOK
		if len(r.statuses) > 0 {
			status, r.statuses = r.statuses[0], r.statuses[1:]
		}
		delay := r.delay
		r.mu.Unlock()
		time.Sleep(delay)
		if status == http.StatusFound {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096)) // larger than what is read
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

type deliveryFixture struct {
	pool      *pgxpool.Pool
	q         *store.Queries
	projectID uuid.UUID
	emailID   uuid.UUID
	keys      *Keyring
	logs      *bytes.Buffer
	enq       *jobs.Enqueuer
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	pool, q, projectID := setup(t)
	f := &deliveryFixture{pool: pool, q: q, projectID: projectID, keys: keyring(t, "k1:"+key(1)), logs: &bytes.Buffer{}}
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO emails (project_id, sender, recipient, subject, status, provider, provider_id, finished_at)
		VALUES ($1, 'noreply@example.test', 'customer@example.test', 'Secret subject', 'captured', 'mailpit', 'x', now())
		RETURNING id`, projectID).Scan(&f.emailID); err != nil {
		t.Fatal(err)
	}
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), 5)
	if err != nil {
		t.Fatal(err)
	}
	f.enq = enq
	return f
}

func (f *deliveryFixture) deliverer(t *testing.T, policy TargetPolicy, maxAttempts int, timeout time.Duration) *Deliverer {
	t.Helper()
	d, err := NewDeliverer(DelivererConfig{
		Pool: f.pool, Logger: slog.New(slog.NewJSONHandler(f.logs, nil)), Keys: f.keys, Targets: policy,
		Timeout: timeout, MaxAttempts: maxAttempts, Retry: jobs.RetryPolicy{Base: time.Second, Max: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// change records a platform event and runs the outbox for it, like a status
// change in the worker; it returns the created deliveries.
func (f *deliveryFixture) change(t *testing.T, status string) (uuid.UUID, []jobs.DeliverWebhookArgs) {
	t.Helper()
	ctx := context.Background()
	var eventID uuid.UUID
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		eventID, err = store.New(tx).InsertPlatformEvent(ctx, store.InsertPlatformEventParams{
			ProjectID: f.projectID, EmailID: f.emailID, Type: status, Details: []byte(`{}`),
		})
		if err != nil {
			return err
		}
		return NewOutbox(f.enq).OnStatusChange(ctx, tx, events.Change{ProjectID: f.projectID, EmailID: f.emailID, EventID: eventID, Status: status})
	})
	if err != nil {
		t.Fatal(err)
	}
	return eventID, f.pending(t, eventID)
}

func (f *deliveryFixture) pending(t *testing.T, eventID uuid.UUID) []jobs.DeliverWebhookArgs {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT id FROM webhook_deliveries WHERE event_id = $1 ORDER BY id", eventID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatal(err)
	}
	var out []jobs.DeliverWebhookArgs
	for _, id := range ids {
		out = append(out, jobs.DeliverWebhookArgs{DeliveryID: id, ProjectID: f.projectID})
	}
	return out
}

type deliveryState struct {
	status   string
	attempts int
	code     *int32
	errCode  *string
	next     *time.Time
}

func (f *deliveryFixture) state(t *testing.T, id uuid.UUID) deliveryState {
	t.Helper()
	var s deliveryState
	if err := f.pool.QueryRow(context.Background(),
		"SELECT status, attempts, last_status_code, last_error_code, next_attempt_at FROM webhook_deliveries WHERE id = $1", id,
	).Scan(&s.status, &s.attempts, &s.code, &s.errCode, &s.next); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *deliveryFixture) endpoint(t *testing.T, url string, types ...string) (Endpoint, string) {
	t.Helper()
	e, secret, err := registrar(f.pool, f.keys, lax).Create(context.Background(), f.projectID, url, types)
	if err != nil {
		t.Fatal(err)
	}
	return e, secret
}

func attempt(n int) jobs.Attempt { return jobs.Attempt{JobID: 1, Number: n, MaxAttempts: 25} }

func TestOutboxCreatesDeliveries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	all, _ := f.endpoint(t, "https://all.example.test/")
	f.endpoint(t, "https://bounces.example.test/", "email.bounced")
	off, _ := f.endpoint(t, "https://off.example.test/")
	if _, err := SetEndpointActive(ctx, f.pool, f.projectID, off.ID, false); err != nil {
		t.Fatal(err)
	}
	other, err := auth.CreateProject(ctx, f.q, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registrar(f.pool, f.keys, lax).Create(ctx, other.ID, "https://other.example.test/", nil); err != nil {
		t.Fatal(err)
	}

	eventID, deliveries := f.change(t, "captured")
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1 (subscribed, active, own project)", len(deliveries))
	}
	var endpointID uuid.UUID
	if err := f.pool.QueryRow(ctx, "SELECT endpoint_id FROM webhook_deliveries WHERE id = $1", deliveries[0].DeliveryID).Scan(&endpointID); err != nil || endpointID != all.ID {
		t.Fatalf("delivery endpoint = %s, %v", endpointID, err)
	}
	var jobsN int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'deliver_webhook' AND queue = 'webhooks'").Scan(&jobsN); err != nil || jobsN != 1 {
		t.Fatalf("jobs = %d, %v", jobsN, err)
	}
	// The same change again creates nothing new.
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		return NewOutbox(f.enq).OnStatusChange(ctx, tx, events.Change{ProjectID: f.projectID, EmailID: f.emailID, EventID: eventID, Status: "captured"})
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.pending(t, eventID)); n != 1 {
		t.Fatalf("deliveries after a repeat = %d", n)
	}
}

func TestDeliverSignsAndCompletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	r := newReceiver(t, http.StatusNoContent)
	_, secret := f.endpoint(t, r.srv.URL+"/hook")
	eventID, deliveries := f.change(t, "captured")

	if err := f.deliverer(t, lax, 3, 5*time.Second).Deliver(ctx, deliveries[0], attempt(1)); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Fatalf("requests = %d", r.count())
	}
	req, body := r.requests[0], r.bodies[0]
	if err := Verify(secret, req.Header.Get(HeaderTimestamp), req.Header.Get(HeaderSignature), body, DefaultTolerance, time.Now()); err != nil {
		t.Fatalf("receiver-side verification: %v", err)
	}
	if req.Header.Get(HeaderID) != eventID.String() || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", req.Header)
	}
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	data, _ := p["data"].(map[string]any)
	if p["id"] != eventID.String() || p["type"] != "email.captured" || len(p) != 4 || len(data) != 2 ||
		data["email_id"] != f.emailID.String() || data["project_id"] != f.projectID.String() {
		t.Fatalf("payload = %s", body)
	}
	for _, pii := range []string{"customer@example.test", "Secret subject", "noreply@"} {
		if strings.Contains(string(body), pii) {
			t.Fatalf("payload contains %q", pii)
		}
	}
	s := f.state(t, deliveries[0].DeliveryID)
	if s.status != "delivered" || s.attempts != 1 || s.code == nil || *s.code != 204 || s.errCode != nil || s.next != nil {
		t.Fatalf("state = %+v", s)
	}
	// A late duplicate job does nothing.
	if err := f.deliverer(t, lax, 3, 5*time.Second).Deliver(ctx, deliveries[0], attempt(2)); err != nil || r.count() != 1 {
		t.Fatalf("second run: %v, %d requests", err, r.count())
	}
}

func TestDeliverRetriesThenFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	r := newReceiver(t, http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusServiceUnavailable)
	f.endpoint(t, r.srv.URL+"/hook")
	_, deliveries := f.change(t, "failed")
	d := f.deliverer(t, lax, 3, 5*time.Second)
	id := deliveries[0].DeliveryID

	for n := 1; n <= 2; n++ {
		before := time.Now()
		err := d.Deliver(ctx, deliveries[0], attempt(n))
		delay, ok := jobs.SnoozeDelay(err)
		if !ok || delay <= 0 {
			t.Fatalf("attempt %d: err = %v, want a planned retry", n, err)
		}
		s := f.state(t, id)
		if s.status != "pending" || s.attempts != n || s.next == nil || s.next.Before(before.Add(delay-time.Second)) || s.errCode == nil {
			t.Fatalf("after attempt %d: %+v", n, s)
		}
	}
	if err := d.Deliver(ctx, deliveries[0], attempt(3)); err != nil {
		t.Fatalf("final attempt: %v", err)
	}
	s := f.state(t, id)
	if s.status != "failed" || s.attempts != 3 || *s.errCode != "http_503" || *s.code != 503 || s.next != nil {
		t.Fatalf("final state = %+v", s)
	}
	if !bytes.Equal(r.bodies[0], r.bodies[1]) || !bytes.Equal(r.bodies[1], r.bodies[2]) {
		t.Fatal("retries did not repeat the identical body")
	}
	if r.requests[0].Header.Get(HeaderID) != r.requests[2].Header.Get(HeaderID) {
		t.Fatal("event id changed between retries")
	}
}

func TestDeliverFailsWithoutRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, status := range []int{http.StatusBadRequest, http.StatusGone, http.StatusFound, 600} {
		f := newDeliveryFixture(t)
		r := newReceiver(t, status)
		f.endpoint(t, r.srv.URL+"/hook")
		_, deliveries := f.change(t, "captured")
		if err := f.deliverer(t, lax, 5, 5*time.Second).Deliver(ctx, deliveries[0], attempt(1)); err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		s := f.state(t, deliveries[0].DeliveryID)
		if status == 600 {
			if s.status != "failed" || s.attempts != 1 || s.code != nil || *s.errCode != CodeInvalidStatus {
				t.Fatalf("600: state %+v", s)
			}
			continue
		}
		if s.status != "failed" || s.attempts != 1 || *s.code != int32(status) || r.count() != 1 {
			t.Fatalf("%d: state %+v, %d requests (redirect must not be followed)", status, s, r.count())
		}
	}
}

func TestDeliverTimeoutIsRetried(t *testing.T) {
	t.Parallel()
	f := newDeliveryFixture(t)
	r := newReceiver(t)
	r.delay = 500 * time.Millisecond
	f.endpoint(t, r.srv.URL+"/hook")
	_, deliveries := f.change(t, "captured")
	err := f.deliverer(t, lax, 5, 100*time.Millisecond).Deliver(context.Background(), deliveries[0], attempt(1))
	if _, ok := jobs.SnoozeDelay(err); !ok {
		t.Fatalf("err = %v, want a planned retry", err)
	}
	if s := f.state(t, deliveries[0].DeliveryID); *s.errCode != CodeTimeout || s.code != nil {
		t.Fatalf("state = %+v", s)
	}
}

func TestDeliverRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Disabled after the delivery was created: no request.
	f := newDeliveryFixture(t)
	r := newReceiver(t)
	e, _ := f.endpoint(t, r.srv.URL+"/hook")
	_, deliveries := f.change(t, "captured")
	if _, err := SetEndpointActive(ctx, f.pool, f.projectID, e.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.deliverer(t, lax, 3, time.Second).Deliver(ctx, deliveries[0], attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t, deliveries[0].DeliveryID); s.status != "failed" || *s.errCode != CodeEndpointDisabled || s.attempts != 0 || r.count() != 0 {
		t.Fatalf("disabled: %+v, %d requests", s, r.count())
	}

	// A loopback target under a strict policy: refused before any request.
	f2 := newDeliveryFixture(t)
	r2 := newReceiver(t)
	f2.endpoint(t, strings.Replace(r2.srv.URL, "127.0.0.1", "localhost", 1)+"/hook")
	_, d2 := f2.change(t, "captured")
	if err := f2.deliverer(t, TargetPolicy{AllowHTTP: true}, 3, time.Second).Deliver(ctx, d2[0], attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := f2.state(t, d2[0].DeliveryID); s.status != "failed" || *s.errCode != CodeForbiddenTarget || r2.count() != 0 {
		t.Fatalf("forbidden: %+v, %d requests", s, r2.count())
	}

	// Another project's id never reaches a pending delivery.
	f3 := newDeliveryFixture(t)
	r3 := newReceiver(t)
	f3.endpoint(t, r3.srv.URL+"/hook")
	_, d3 := f3.change(t, "captured")
	if err := f3.deliverer(t, lax, 3, time.Second).Deliver(ctx, jobs.DeliverWebhookArgs{DeliveryID: d3[0].DeliveryID, ProjectID: uuid.New()}, attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := f3.state(t, d3[0].DeliveryID); s.status != "pending" || s.attempts != 0 || r3.count() != 0 {
		t.Fatalf("foreign project: %+v, %d requests", s, r3.count())
	}
}

// URLs may carry tokens: they never reach logs or stored error codes.
func TestDeliverNeverLeaksTheURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	r := newReceiver(t)
	r.srv.Close() // connection refused
	f.endpoint(t, r.srv.URL+"/hook?token=s3cr3t-t0ken")
	_, deliveries := f.change(t, "captured")
	err := f.deliverer(t, lax, 3, time.Second).Deliver(ctx, deliveries[0], attempt(1))
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("err = %v", err)
	}
	s := f.state(t, deliveries[0].DeliveryID)
	if *s.errCode != CodeConnect {
		t.Fatalf("state = %+v", s)
	}
	if strings.Contains(f.logs.String(), "s3cr3t") || !strings.Contains(f.logs.String(), `"host":"127.0.0.1"`) {
		t.Fatalf("logs leak the URL or miss the host: %s", f.logs.String())
	}
}

// Internal failures are retried by the queue; on its final attempt the
// delivery fails instead of staying pending.
func TestDeliverInternalErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	r := newReceiver(t)
	f.endpoint(t, r.srv.URL+"/hook")
	_, deliveries := f.change(t, "captured")
	f.keys = keyring(t, "k9:"+key(9)) // cannot decrypt the stored secret
	d := f.deliverer(t, lax, 3, time.Second)
	if err := d.Deliver(ctx, deliveries[0], attempt(1)); err == nil {
		t.Fatal("non-final internal error must be returned for a queue retry")
	} else if _, snoozed := jobs.SnoozeDelay(err); snoozed {
		t.Fatal("internal error must not be a planned retry")
	}
	if s := f.state(t, deliveries[0].DeliveryID); s.status != "pending" {
		t.Fatalf("after a non-final internal error: %+v", s)
	}
	if err := d.Deliver(ctx, deliveries[0], jobs.Attempt{JobID: 1, Number: 25, MaxAttempts: 25}); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t, deliveries[0].DeliveryID); s.status != "failed" || *s.errCode != CodeSecret || r.count() != 0 {
		t.Fatalf("final: %+v", s)
	}
}

func TestCleanupExpiresStalePending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	f.endpoint(t, "https://hooks.example.test/")
	_, stale := f.change(t, "captured")
	_, fresh := f.change(t, "failed")
	// Stale: no activity for 30 days. Retried: created long ago but attempted
	// recently (a backlog), so it is kept.
	if _, err := f.pool.Exec(ctx, "UPDATE webhook_deliveries SET created_at = now() - interval '30 days', updated_at = now() - interval '30 days' WHERE id = $1", stale[0].DeliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE webhook_deliveries SET created_at = now() - interval '30 days' WHERE id = $1", fresh[0].DeliveryID); err != nil {
		t.Fatal(err)
	}
	if err := f.deliverer(t, lax, 12, 10*time.Second).Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t, stale[0].DeliveryID); s.status != "failed" || *s.errCode != CodeExpired {
		t.Fatalf("stale = %+v", s)
	}
	if s := f.state(t, fresh[0].DeliveryID); s.status != "pending" {
		t.Fatalf("fresh = %+v", s)
	}
}

func TestListDeliveries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	r := newReceiver(t, http.StatusBadRequest)
	f.endpoint(t, r.srv.URL+"/hook")
	_, failed := f.change(t, "captured")
	f.change(t, "failed")
	if err := f.deliverer(t, lax, 3, time.Second).Deliver(ctx, failed[0], attempt(1)); err != nil {
		t.Fatal(err)
	}
	all, err := ListDeliveries(ctx, f.q, f.projectID, "", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	only, err := ListDeliveries(ctx, f.q, f.projectID, "failed", 10)
	if err != nil || len(only) != 1 || only[0].EventType != "captured" || *only[0].LastErrorCode != "http_400" || only[0].EmailID != f.emailID {
		t.Fatalf("failed = %+v, %v", only, err)
	}
	if _, err := ListDeliveries(ctx, f.q, f.projectID, "lost", 10); err == nil {
		t.Fatal("unknown status accepted")
	}
	if other, _ := ListDeliveries(ctx, f.q, uuid.New(), "", 10); !slices.Equal(other, []Delivery{}) && len(other) != 0 {
		t.Fatalf("foreign project sees %d deliveries", len(other))
	}
}

// A provider event stored while the email was being sent and applied at
// finish time yields exactly two deliveries: the submission and the event
// holding the final status (never intermediate ranks).
func TestDelayedProjectionCreatesTwoDeliveries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	f.endpoint(t, "https://hooks.example.test/")
	var sending uuid.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO emails (project_id, sender, recipient, subject, status) VALUES ($1, 'a@example.test', 'b@example.test', 'Hi', 'sending')
		RETURNING id`, f.projectID).Scan(&sending); err != nil {
		t.Fatal(err)
	}
	rec := events.NewRecorder(f.pool, slog.New(slog.DiscardHandler))
	rec.OnStatusChange(NewOutbox(f.enq).OnStatusChange)
	at := time.Now().Add(-time.Minute)
	for _, ev := range []events.ProviderEvent{
		{Provider: "ses", ProviderEventID: "delivery:m", EmailID: sending, ProjectID: f.projectID, Type: "delivered", OccurredAt: at},
		{Provider: "ses", ProviderEventID: "fb-1", EmailID: sending, ProjectID: f.projectID, Type: "bounced", OccurredAt: at,
			Details: map[string]string{"bounce_type": "Permanent"}},
	} {
		if _, err := rec.Ingest(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := store.New(tx).MarkDispatched(ctx, store.MarkDispatchedParams{
			ID: sending, ProjectID: f.projectID, Status: "submitted", Provider: new("ses"), ProviderID: new("m"),
		}); err != nil {
			return err
		}
		return rec.Dispatched(ctx, tx, f.projectID, sending, "submitted", "ses")
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.pool.Query(ctx, `
		SELECT ev.type FROM webhook_deliveries d JOIN email_events ev ON ev.id = d.event_id
		WHERE ev.email_id = $1 ORDER BY d.id`, sending)
	if err != nil {
		t.Fatal(err)
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(types, []string{"submitted", "bounced"}) {
		t.Fatalf("deliveries for %v, want [submitted bounced]", types)
	}
}

func TestCanceledEventReachesSubscribedEndpoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	r := newReceiver(t)
	all, _ := f.endpoint(t, r.srv.URL+"/all")
	if !slices.Contains(all.EventTypes, "email.canceled") {
		t.Fatalf("an endpoint without a type list gets %v", all.EventTypes)
	}
	f.endpoint(t, r.srv.URL+"/canceled", "email.canceled")
	f.endpoint(t, r.srv.URL+"/bounces", "email.bounced")

	_, deliveries := f.change(t, "canceled")
	if len(deliveries) != 2 {
		t.Fatalf("deliveries = %d, want 2", len(deliveries))
	}
	d := f.deliverer(t, lax, 3, 5*time.Second)
	for _, a := range deliveries {
		if err := d.Deliver(ctx, a, attempt(1)); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) != 2 {
		t.Fatalf("requests = %d", len(r.bodies))
	}
	for _, body := range r.bodies {
		var ev struct{ Type string }
		if err := json.Unmarshal(body, &ev); err != nil || ev.Type != "email.canceled" {
			t.Fatalf("payload type %q (%v)", ev.Type, err)
		}
	}
}
