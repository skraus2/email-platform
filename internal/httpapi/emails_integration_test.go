//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/httpapi"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

// stack is the API wired exactly like cmd/api, on a throwaway database.
type stack struct {
	pool *pgxpool.Pool
	q    *store.Queries
	srv  *httptest.Server
}

func newStack(t *testing.T) *stack {
	t.Helper()
	return newLimitedStack(t, 1_000_000)
}

// newLimitedStack is newStack with a send rate limit per project and minute.
func newLimitedStack(t *testing.T, perMinute int) *stack {
	t.Helper()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	logger := slog.New(slog.DiscardHandler)
	enq, err := jobs.NewEnqueuer(pool, logger, 5)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(httpapi.Deps{
		Logger: logger,
		Emails: emails.NewService(pool, enq, logger, perMinute),
		Authenticate: func(ctx context.Context, token string) (auth.Principal, error) {
			return auth.Authenticate(ctx, q.GetActiveKeyByPrefix, token)
		},
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &stack{pool: pool, q: q, srv: srv}
}

// project creates a project with a key holding the given scopes (nil = all).
func (s *stack) project(t *testing.T, scopes ...string) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	p, err := auth.CreateProject(ctx, s.q, "app-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) == 0 {
		scopes = nil
	}
	token, _, err := auth.CreateKey(ctx, s.q, p.ID, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID, token
}

func (s *stack) do(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, s.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

const sendBody = `{"from":"noreply@example.test","to":"customer@example.test","subject":"Hi","text":"Hello"}`

func TestPostEmailPersistsAndQueues(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	projectID, token := s.project(t)

	code, body := s.do(t, http.MethodPost, "/v1/emails", token, sendBody)
	if code != http.StatusAccepted || body["status"] != "queued" {
		t.Fatalf("POST = %d %v", code, body)
	}
	id := uuid.MustParse(body["id"].(string))

	var status string
	var owner uuid.UUID
	if err := s.pool.QueryRow(context.Background(),
		"SELECT status, project_id FROM emails WHERE id = $1", id).Scan(&status, &owner); err != nil {
		t.Fatalf("email not persisted when 202 was returned: %v", err)
	}
	if status != "queued" || owner != projectID {
		t.Fatalf("row = %s / %s", status, owner)
	}
	var jobs int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE args->>'email_id' = $1", id.String()).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("jobs for email = %d, want 1", jobs)
	}
}

func TestPostEmailAuthFailures(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, readOnly := s.project(t, auth.ScopeEmailsRead)
	projectID, revoked := s.project(t)
	prefix, _, _ := auth.ParseToken(revoked)
	if err := auth.RevokeKey(context.Background(), s.q, prefix); err != nil {
		t.Fatal(err)
	}
	disabledID, disabledKey := s.project(t)
	if err := auth.SetProjectStatus(context.Background(), s.q, disabledID, false); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		token string
		want  int
	}{
		"no key":           {"", http.StatusUnauthorized},
		"garbage key":      {"ep_nope", http.StatusUnauthorized},
		"revoked key":      {revoked, http.StatusUnauthorized},
		"disabled project": {disabledKey, http.StatusUnauthorized},
		"missing scope":    {readOnly, http.StatusForbidden},
	} {
		if code, _ := s.do(t, http.MethodPost, "/v1/emails", tc.token, sendBody); code != tc.want {
			t.Errorf("%s: code = %d, want %d", name, code, tc.want)
		}
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM emails").Scan(&n)
	if n != 0 {
		t.Fatalf("%d emails created by rejected requests (project %s)", n, projectID)
	}
}

func TestGetEmailIsolationBetweenProjects(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, keyA := s.project(t)
	_, keyB := s.project(t)

	code, body := s.do(t, http.MethodPost, "/v1/emails", keyA, sendBody)
	if code != http.StatusAccepted {
		t.Fatalf("POST = %d", code)
	}
	id := body["id"].(string)

	code, own := s.do(t, http.MethodGet, "/v1/emails/"+id, keyA, "")
	if code != http.StatusOK || own["id"] != id || own["status"] != "queued" || own["to"] != "customer@example.test" {
		t.Fatalf("own GET = %d %v", code, own)
	}

	foreignCode, foreign := s.do(t, http.MethodGet, "/v1/emails/"+id, keyB, "")
	unknownCode, unknown := s.do(t, http.MethodGet, "/v1/emails/"+uuid.NewString(), keyB, "")
	if foreignCode != http.StatusNotFound || unknownCode != http.StatusNotFound {
		t.Fatalf("foreign = %d, unknown = %d, want 404 both", foreignCode, unknownCode)
	}
	fj, _ := json.Marshal(foreign)
	uj, _ := json.Marshal(unknown)
	if string(fj) != string(uj) {
		t.Fatalf("foreign 404 differs from unknown 404: %s vs %s", fj, uj)
	}
	if strings.Contains(string(fj), "customer@example.test") || strings.Contains(string(fj), id) {
		t.Fatalf("foreign response leaks metadata: %s", fj)
	}
}

func TestGetEmailShowsAttemptsAndErrorCode(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t)
	code, body := s.do(t, http.MethodPost, "/v1/emails", token, sendBody)
	if code != http.StatusAccepted {
		t.Fatalf("POST = %d", code)
	}
	id := body["id"].(string)

	_, fresh := s.do(t, http.MethodGet, "/v1/emails/"+id, token, "")
	if v, ok := fresh["error_code"]; !ok || v != nil || fresh["attempts"] != float64(0) {
		t.Fatalf("fresh email = %v, want attempts 0 and error_code null", fresh)
	}

	if _, err := s.pool.Exec(context.Background(), `UPDATE emails SET status = 'failed', attempts = 3,
		error_code = 'provider_rejected', last_error = 'delivery failed: rejected (smtp 550)', finished_at = now()
		WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	_, failed := s.do(t, http.MethodGet, "/v1/emails/"+id, token, "")
	if failed["status"] != "failed" || failed["error_code"] != "provider_rejected" || failed["attempts"] != float64(3) {
		t.Fatalf("failed email = %v", failed)
	}
	if strings.Contains(fmt.Sprint(failed), "smtp 550") {
		t.Fatal("internal detail exposed")
	}
}

func TestPostEmailRateLimitedOverHTTP(t *testing.T) {
	t.Parallel()
	if sec := time.Now().Second(); sec > 56 {
		time.Sleep(time.Duration(61-sec) * time.Second) // stay inside one window
	}
	s := newLimitedStack(t, 2)
	_, token := s.project(t)
	_, otherToken := s.project(t)
	for i := range 2 {
		if code, _ := s.do(t, http.MethodPost, "/v1/emails", token, sendBody); code != http.StatusAccepted {
			t.Fatalf("send %d = %d", i+1, code)
		}
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.srv.URL+"/v1/emails", strings.NewReader(sendBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	retryAfter, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if resp.StatusCode != http.StatusTooManyRequests || err != nil || retryAfter < 1 || retryAfter > 60 {
		t.Fatalf("3rd send = %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	var body map[string]map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["error"]["code"] != "rate_limited" {
		t.Fatalf("body = %v", body)
	}
	if code, _ := s.do(t, http.MethodPost, "/v1/emails", otherToken, sendBody); code != http.StatusAccepted {
		t.Fatalf("other project = %d, want 202", code)
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM emails").Scan(&n)
	if n != 3 {
		t.Fatalf("emails = %d, want 3", n)
	}
}

func TestPostEmailToSuppressedRecipientOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	projectID, token := s.project(t)
	if _, err := emails.AddSuppression(context.Background(), s.q, projectID, "customer@example.test", "bounce"); err != nil {
		t.Fatal(err)
	}
	code, body := s.do(t, http.MethodPost, "/v1/emails", token, sendBody)
	if e, _ := body["error"].(map[string]any); code != http.StatusUnprocessableEntity || e["code"] != "recipient_suppressed" {
		t.Fatalf("POST = %d %v, want 422 recipient_suppressed", code, body)
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM emails").Scan(&n)
	if n != 0 {
		t.Fatalf("emails = %d, want 0", n)
	}
}
