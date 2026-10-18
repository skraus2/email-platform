//go:build integration

package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/httpapi"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
	"github.com/skraus2/email-platform/internal/webhooks"
)

// newWebhookStack is the API wired like cmd/api with registration enabled
// (keys set) or disabled (keys nil).
func newWebhookStack(t *testing.T, keys *webhooks.Keyring, maxEndpoints int) *stack {
	t.Helper()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	var reg httpapi.WebhookRegistrar
	if keys != nil {
		reg = webhooks.Registrar{Pool: pool, Keys: keys, Policy: webhooks.TargetPolicy{}, MaxEndpoints: maxEndpoints}
	}
	api := httpapi.New(httpapi.Deps{
		Logger:   slog.New(slog.DiscardHandler),
		Webhooks: reg,
		Authenticate: func(ctx context.Context, token string) (auth.Principal, error) {
			return auth.Authenticate(ctx, q.GetActiveKeyByPrefix, token)
		},
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &stack{pool: pool, q: q, srv: srv}
}

func testKeys(t *testing.T) *webhooks.Keyring {
	t.Helper()
	k, err := webhooks.ParseKeyring("k1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCreateEndpointScope(t *testing.T) {
	t.Parallel()
	s := newWebhookStack(t, testKeys(t), 10)
	ctx := context.Background()

	// Keys created without the scope (the default set, or every other scope)
	// cannot register.
	_, plain := s.project(t)
	_, others := s.project(t, auth.ScopeEmailsSend, auth.ScopeEmailsRead, auth.ScopeTemplatesRead, auth.ScopeTemplatesWrite)
	for _, tok := range []string{plain, others} {
		if code, body := s.do(t, "POST", "/v1/webhook-endpoints", tok, `{"url":"https://hooks.example.test/"}`); code != 403 || errorCode(body) != "forbidden" {
			t.Fatalf("without scope: %d %v", code, body)
		}
	}
	if code, body := s.do(t, "POST", "/v1/webhook-endpoints", "", `{"url":"https://hooks.example.test/"}`); code != 401 {
		t.Fatalf("without key: %d %v", code, body)
	}
	var n int
	if _, err := s.pool.Exec(ctx, "UPDATE api_keys SET scopes = ARRAY['webhooks:read']"); err == nil {
		t.Fatal("the scope CHECK accepts an unknown scope")
	}

	// Registration off: authenticated and scoped, still 503.
	off := newWebhookStack(t, nil, 10)
	_, tok := off.project(t, auth.ScopeWebhooksWrite)
	code, body := off.do(t, "POST", "/v1/webhook-endpoints", tok, `{"url":"https://hooks.example.test/"}`)
	if code != 503 || errorCode(body) != "webhook_registration_disabled" {
		t.Fatalf("disabled: %d %v", code, body)
	}
	if err := off.pool.QueryRow(ctx, "SELECT count(*) FROM webhook_endpoints").Scan(&n); err != nil || n != 0 {
		t.Fatalf("endpoints while disabled = %d, %v", n, err)
	}
}

func TestCreateEndpointSecretDecrypts(t *testing.T) {
	t.Parallel()
	keys := testKeys(t)
	s := newWebhookStack(t, keys, 10)
	ctx := context.Background()
	projectID, tok := s.project(t, auth.ScopeWebhooksWrite)
	otherID, _ := s.project(t, auth.ScopeWebhooksWrite)

	code, body := s.do(t, "POST", "/v1/webhook-endpoints", tok,
		`{"url":"https://Hooks.Example.test/in?token=t0k3n","event_types":["email.bounced","email.delivered"]}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, body)
	}
	assertTime(t, body, "created_at")
	secret, _ := body["secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") || body["status"] != "active" ||
		body["url"] != "https://Hooks.Example.test/in?token=t0k3n" || len(body) != 6 {
		t.Fatalf("body = %v", body)
	}
	id := uuid.MustParse(body["id"].(string))

	rows, err := s.q.ListWebhookEndpoints(ctx, projectID)
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("stored = %+v, %v", rows, err)
	}
	r := rows[0]
	if !slices.Equal(r.EventTypes, []string{"email.bounced", "email.delivered"}) || r.SecretKeyID != "k1" {
		t.Fatalf("stored row = %+v", r)
	}
	got, err := keys.Decrypt(r.SecretCiphertext, r.SecretKeyID, r.ID, projectID)
	if err != nil || string(got) != secret {
		t.Fatalf("stored secret decrypts to %q, %v; want the returned one", got, err)
	}
	// Bound to its project: the same ciphertext does not open under another.
	if _, err := keys.Decrypt(r.SecretCiphertext, r.SecretKeyID, r.ID, otherID); err == nil {
		t.Fatal("secret decrypts with another project id")
	}
	if list, _ := webhooks.ListEndpoints(ctx, s.q, otherID); len(list) != 0 {
		t.Fatalf("other project sees %d endpoints", len(list))
	}

	// All types by default; policy refusals and bad types store nothing.
	code, body = s.do(t, "POST", "/v1/webhook-endpoints", tok, `{"url":"https://hooks.example.test/all"}`)
	if code != 201 || len(body["event_types"].([]any)) != len(webhooks.EventTypes) {
		t.Fatalf("default types: %d %v", code, body)
	}
	for _, tc := range []struct{ body, code string }{
		{`{"url":"http://hooks.example.test/"}`, "invalid_url"},
		{`{"url":"https://10.0.0.1/"}`, "invalid_url"},
		{`{"url":"https://169.254.169.254/latest"}`, "invalid_url"},
		{`{"url":"https://localhost/"}`, "invalid_url"},
		{`{}`, "invalid_url"},
		{`{"url":"https://hooks.example.test/","event_types":["email.opened"]}`, "validation_failed"},
	} {
		if code, body := s.do(t, "POST", "/v1/webhook-endpoints", tok, tc.body); code != 422 || errorCode(body) != tc.code {
			t.Errorf("%s: %d %v, want 422 %s", tc.body, code, body, tc.code)
		}
	}
	if rows, _ := s.q.ListWebhookEndpoints(ctx, projectID); len(rows) != 2 {
		t.Fatalf("endpoints = %d, want 2", len(rows))
	}
}

// Ten concurrent creates with one slot left: exactly one wins, and another
// project's endpoints do not count.
func TestCreateEndpointCapConcurrent(t *testing.T) {
	t.Parallel()
	keys := testKeys(t)
	s := newWebhookStack(t, keys, 10)
	ctx := context.Background()
	projectID, tok := s.project(t, auth.ScopeWebhooksWrite)
	_, otherTok := s.project(t, auth.ScopeWebhooksWrite)

	reg := webhooks.Registrar{Pool: s.pool, Keys: keys, MaxEndpoints: 10}
	for range 9 {
		if _, _, err := reg.Create(ctx, projectID, "https://hooks.example.test/pre", nil); err != nil {
			t.Fatal(err)
		}
	}
	// Open the connections up front, so the creates really race.
	conns := make([]*pgxpool.Conn, 0, s.pool.Config().MaxConns)
	for range s.pool.Config().MaxConns {
		c, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Release()
	}
	codes := make(chan string, 10)
	start := make(chan struct{})
	for range 10 {
		go func() {
			<-start
			// Not s.do: t.Fatal outside the test goroutine would leave the
			// receive loop below waiting.
			code, body, err := post(s.srv.URL+"/v1/webhook-endpoints", tok, `{"url":"https://hooks.example.test/race"}`)
			switch {
			case err != nil:
				codes <- "request failed: " + err.Error()
			case code == 201:
				codes <- "created"
			default:
				codes <- errorCode(body)
			}
		}()
	}
	close(start)
	var created, limited int
	for range 10 {
		switch c := <-codes; c {
		case "created":
			created++
		case "endpoint_limit_reached":
			limited++
		default:
			t.Errorf("unexpected outcome %q", c)
		}
	}
	if created != 1 || limited != 9 {
		t.Fatalf("created = %d, limited = %d; want 1 and 9", created, limited)
	}
	if rows, _ := s.q.ListWebhookEndpoints(ctx, projectID); len(rows) != 10 {
		t.Fatalf("endpoints = %d, want 10", len(rows))
	}
	if code, body := s.do(t, "POST", "/v1/webhook-endpoints", otherTok, `{"url":"https://hooks.example.test/"}`); code != 201 {
		t.Fatalf("other project: %d %v", code, body)
	}
}

// post sends a JSON POST with a bearer token and returns status and body.
func post(url, token, body string) (int, map[string]any, error) {
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, m, nil
}
