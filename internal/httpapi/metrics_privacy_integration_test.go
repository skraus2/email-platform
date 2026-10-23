//go:build integration

package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skraus2/email-platform/internal/metrics"
)

// scrape renders the stack's metrics as Prometheus serves them.
func (s *stack) scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(s.metrics).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(rec.Body)
	if err != nil || rec.Code != http.StatusOK {
		t.Fatalf("scrape: %d %v", rec.Code, err)
	}
	return string(body)
}

func TestMetricsHoldNoPersonalDataOrSecrets(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	project, token := s.project(t, "emails:send", "emails:read", "templates:read", "templates:write")
	code, created := s.send(t, token, request{method: "POST", path: "/v1/emails",
		body: `{"from":"sender-x@example.test","to":"private-recipient@example.test","subject":"Secret subject line","text":"secret body text"}`})
	if code != http.StatusAccepted {
		t.Fatalf("send: %d %s", code, created)
	}
	emailID := created[strings.Index(created, `"id":"`)+6:][:36]
	code, tmpl := s.send(t, token, request{method: "POST", path: "/v1/templates", body: `{"slug":"private-slug"}`})
	if code != http.StatusCreated {
		t.Fatalf("template: %d %s", code, tmpl)
	}
	tmplID := tmpl[strings.Index(tmpl, `"id":"`)+6:][:36]
	s.send(t, token, request{method: "POST", path: "/v1/templates/" + tmplID + "/versions", body: `{"subject":"Hi {{.name}}","text":"Hello {{.name}}","variables":{"name":{"type":"string"}}}`})
	s.send(t, token, request{method: "POST", path: "/v1/emails",
		body: `{"from":"sender-x@example.test","to":"private-two@example.test","template_id":"` + tmplID + `","template_version":1,"variables":{"name":"Private Name"}}`})
	s.send(t, token, request{method: "GET", path: "/v1/emails/" + emailID})
	s.send(t, token, request{method: "GET", path: "/v1/emails?recipient=private-recipient@example.test"})
	s.send(t, "ep_wrong_key", request{method: "GET", path: "/v1/emails"})

	out := s.scrape(t)
	if !strings.Contains(out, `route="POST /v1/emails"`) || !strings.Contains(out, `route="GET /v1/emails/{id}"`) {
		t.Fatalf("expected route series missing:\n%s", out)
	}
	for _, needle := range []string{
		"private-recipient", "private-two", "sender-x", "example.test", "Secret subject", "secret body", "Private Name",
		"private-slug", token, strings.Split(token, "_")[1], "ep_wrong_key", project.String(), emailID, tmplID,
		"email_platform:email_platform", "postgres://",
	} {
		if strings.Contains(out, needle) {
			t.Errorf("metrics contain %q", needle)
		}
	}
}
