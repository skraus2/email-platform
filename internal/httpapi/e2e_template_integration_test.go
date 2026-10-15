//go:build integration

package httpapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/testmailpit"
)

// TestTemplateSendThroughWorkerToMailpit: a template version published via
// /v1, sent with "published" and with an explicit older version, lands in
// Mailpit rendered (escaped HTML part, text part) and is listed by template.
func TestTemplateSendThroughWorkerToMailpit(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, key := s.project(t, "emails:send", "emails:read", "templates:read", "templates:write")

	mp, err := delivery.NewMailpit(testmailpit.SMTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	d := emails.NewDispatcher(s.pool, mp, logger, 5*time.Second, 168*time.Hour)
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: s.pool, Logger: logger, Concurrency: 2, JobTimeout: 30 * time.Second,
		Retry:    jobs.RetryPolicy{Base: time.Second, Max: 10 * time.Second},
		Handlers: jobs.Handlers{SendEmail: d.SendEmail, Cleanup: d.Cleanup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })

	code, tmpl := s.do(t, http.MethodPost, "/v1/templates", key, `{"slug":"welcome"}`)
	if code != 201 {
		t.Fatal(code, tmpl)
	}
	tid := tmpl["id"].(string)
	marker := time.Now().Format(time.RFC3339Nano)
	v1 := `{"subject":"V1 {{.name}} ` + marker + `","text":"Hello {{.name}}","html":"<p>Hello {{.name}}</p><a href=\"{{.link}}\">go</a>",` +
		`"variables":{"name":{"type":"string","required":true},"link":{"type":"string"}}}`
	v2 := `{"subject":"V2 {{.name}} ` + marker + `","text":"Hi {{.name}}","variables":{"name":{"type":"string","required":true}}}`
	for _, v := range []string{v1, v2} {
		if code, body := s.do(t, http.MethodPost, "/v1/templates/"+tid+"/versions", key, v); code != 201 {
			t.Fatal(code, body)
		}
	}

	send := func(version string, vars string) string {
		t.Helper()
		code, created := s.do(t, http.MethodPost, "/v1/emails", key,
			`{"from":"noreply@example.test","to":"customer@example.test","template_id":"`+tid+`","template_version":`+version+`,"variables":`+vars+`}`)
		if code != http.StatusAccepted {
			t.Fatalf("send %s: %d %v", version, code, created)
		}
		return created["id"].(string)
	}
	latestID := send(`"published"`, `{"name":"Ada"}`)
	oldID := send(`1`, `{"name":"<Bob & Co>","link":"javascript:alert(1)"}`)
	if code, body := s.do(t, http.MethodPost, "/v1/emails", key,
		`{"from":"noreply@example.test","to":"customer@example.test","template_id":"`+tid+`","template_version":"published","variables":{}}`); code != 422 || errorCode(body) != "variables_invalid" {
		t.Fatalf("missing variable: %d %v", code, body)
	}

	for id, version := range map[string]float64{latestID: 2, oldID: 1} {
		var e map[string]any
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			_, e = s.do(t, http.MethodGet, "/v1/emails/"+id, key, "")
			if e["status"] == "captured" || e["status"] == "failed" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if e["status"] != "captured" || e["template_id"] != tid || e["template_version"] != version {
			t.Fatalf("email %s: %v", id, e)
		}
	}

	latest := testmailpit.FindByMessageID(t, latestID+"@email-platform.local", 5*time.Second)
	if len(latest) != 1 || latest[0].Subject != "V2 Ada "+marker || strings.TrimSpace(latest[0].Text) != "Hi Ada" || latest[0].HTML != "" {
		t.Fatalf("published version message: %+v", latest)
	}
	old := testmailpit.FindByMessageID(t, oldID+"@email-platform.local", 5*time.Second)
	if len(old) != 1 || old[0].Subject != "V1 <Bob & Co> "+marker || strings.TrimSpace(old[0].Text) != "Hello <Bob & Co>" {
		t.Fatalf("version 1 message: %+v", old)
	}
	if html := strings.TrimSpace(old[0].HTML); html != `<p>Hello &lt;Bob &amp; Co&gt;</p><a href="#ZgotmplZ">go</a>` {
		t.Fatalf("version 1 html: %q", html)
	}

	// Both are listed by template; a raw send is not.
	if code, _ := s.do(t, http.MethodPost, "/v1/emails", key,
		`{"from":"noreply@example.test","to":"customer@example.test","subject":"raw","text":"raw"}`); code != 202 {
		t.Fatal(code)
	}
	ids := s.listIDs(t, key, "template_id="+tid, 1)
	if len(ids) != 2 || ids[0] != oldID || ids[1] != latestID {
		t.Fatalf("listed by template: %v, want [%s %s]", ids, oldID, latestID)
	}
}
