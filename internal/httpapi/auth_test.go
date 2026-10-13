package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
)

const testToken = "ep_abcdefghijk2_" + "0123456789012345678901234567890123456789abc"

func authServer(t *testing.T, authn Authenticator) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	s := New(Deps{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Authenticate: authn})
	return s, &logs
}

func callProtected(t *testing.T, s *Server, scope, authHeader string) (*httptest.ResponseRecorder, *auth.Principal) {
	t.Helper()
	var got *auth.Principal
	h := s.requireScope(scope, func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		got = &p
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	requestLogger(s.logger, h).ServeHTTP(rec, req)
	return rec, got
}

func TestRequireScopeAcceptsValidKey(t *testing.T) {
	want := auth.Principal{ProjectID: uuid.New(), KeyID: uuid.New(), Scopes: []string{auth.ScopeEmailsSend}}
	var seenToken string
	s, logs := authServer(t, func(_ context.Context, token string) (auth.Principal, error) {
		seenToken = token
		return want, nil
	})
	rec, got := callProtected(t, s, auth.ScopeEmailsSend, "Bearer "+testToken)
	if rec.Code != http.StatusNoContent || got == nil || got.ProjectID != want.ProjectID {
		t.Fatalf("code = %d, principal = %+v", rec.Code, got)
	}
	if seenToken != testToken {
		t.Errorf("authenticator got %q", seenToken)
	}
	if !strings.Contains(logs.String(), want.ProjectID.String()) {
		t.Errorf("request log lacks project_id: %s", logs)
	}
	if strings.Contains(logs.String(), testToken) || strings.Contains(logs.String(), "Bearer") {
		t.Errorf("request log contains credentials: %s", logs)
	}
}

func TestRequireScopeRejectsMissingOrMalformedHeader(t *testing.T) {
	called := false
	s, _ := authServer(t, func(context.Context, string) (auth.Principal, error) {
		called = true
		return auth.Principal{}, nil
	})
	for _, h := range []string{"", "Basic dXNlcjpwYXNz", "Bearer", "Bearer ", "Bearer  " + testToken, "bearer" + testToken} {
		rec, got := callProtected(t, s, auth.ScopeEmailsSend, h)
		if rec.Code != http.StatusUnauthorized || got != nil {
			t.Errorf("header %q: code = %d", h, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("header %q: missing WWW-Authenticate", h)
		}
		assertErrorCode(t, rec, "unauthorized")
	}
	if called {
		t.Error("authenticator called for a malformed Authorization header")
	}
}

func TestRequireScopeSchemeIsCaseInsensitive(t *testing.T) {
	s, _ := authServer(t, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{Scopes: []string{auth.ScopeEmailsSend}}, nil
	})
	if rec, _ := callProtected(t, s, auth.ScopeEmailsSend, "bearer "+testToken); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestRequireScopeUnauthorizedAndForbidden(t *testing.T) {
	s, _ := authServer(t, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{}, auth.ErrUnauthorized
	})
	rec, _ := callProtected(t, s, auth.ScopeEmailsSend, "Bearer "+testToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	assertErrorCode(t, rec, "unauthorized")

	s, _ = authServer(t, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{Scopes: []string{auth.ScopeEmailsRead}}, nil
	})
	rec, got := callProtected(t, s, auth.ScopeEmailsSend, "Bearer "+testToken)
	if rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	assertErrorCode(t, rec, "forbidden")
}

func TestRequireScopeLookupOutageIs503(t *testing.T) {
	s, logs := authServer(t, func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{}, errors.New("dial tcp 10.0.0.5:5432: connection refused")
	})
	rec, _ := callProtected(t, s, auth.ScopeEmailsSend, "Bearer "+testToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	assertErrorCode(t, rec, "service_unavailable")
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Error("internal error leaked to client")
	}
	if !strings.Contains(logs.String(), "authentication backend failed") {
		t.Error("outage not logged")
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	body := decode(t, rec)
	e, ok := body["error"].(map[string]any)
	if !ok || e["code"] != code {
		t.Fatalf("body = %v, want error code %q", body, code)
	}
}
