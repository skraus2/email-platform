package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/metrics"
)

func TestAdminServesMetricsWithoutSession(t *testing.T) {
	reg := metrics.NewRegistry()
	s := NewAdmin(AdminDeps{Logger: slog.New(slog.DiscardHandler), Sessions: nil, ListenHost: "127.0.0.1", Metrics: metrics.Handler(reg)})
	for _, tc := range []struct {
		method, host string
		code         int
	}{
		{http.MethodGet, "127.0.0.1:8081", http.StatusOK},
		{http.MethodGet, "evil.example.test", http.StatusMisdirectedRequest},
		{http.MethodPost, "127.0.0.1:8081", http.StatusMethodNotAllowed},
	} {
		req := httptest.NewRequest(tc.method, "/metrics", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Errorf("%s /metrics Host %s = %d, want %d", tc.method, tc.host, rec.Code, tc.code)
		}
		if tc.code == http.StatusOK {
			body, _ := io.ReadAll(rec.Body)
			if !strings.Contains(string(body), "go_goroutines") {
				t.Error("no metrics in the body")
			}
		}
	}

	// Without a metrics handler there is no route.
	s = NewAdmin(AdminDeps{Logger: slog.New(slog.DiscardHandler), ListenHost: "127.0.0.1"})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Host = "127.0.0.1:8081"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("admin without metrics: %d, want 404", rec.Code)
	}
}

func TestPublicListenerMeasuresRoutesButHasNoMetricsEndpoint(t *testing.T) {
	reg := metrics.NewRegistry()
	p := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsRead}}
	s := New(Deps{
		Logger:       slog.New(slog.DiscardHandler),
		Authenticate: func(context.Context, string) (auth.Principal, error) { return p, nil },
		Usage:        &fakeUsage{},
		HTTPMetrics:  metrics.NewHTTP(reg),
	})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("public /metrics = %d, want 404", rec.Code)
	}
	for _, method := range []string{"GET", "FOO1", "FOO2"} {
		req := httptest.NewRequest(method, "/v1/usage", nil)
		req.Header.Set("Authorization", "Bearer ep_test_x")
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, f := range families {
		if f.GetName() != "email_platform_http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "route" {
					routes = append(routes, l.GetValue()+"="+m.GetLabel()[0].GetValue())
				}
			}
		}
	}
	// Labels are sorted by name: code first, then route.
	want := map[string]bool{"/=404": true, "GET /v1/usage=200": true, "/v1/usage=405": true}
	if len(routes) != len(want) {
		t.Errorf("routes %q, want %v", routes, want)
	}
	for _, r := range routes {
		if !want[r] {
			t.Errorf("unexpected series route=code %q (all: %q)", r, routes)
		}
	}
}
