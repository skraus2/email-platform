//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/usage"
)

type usageBody struct {
	From string `json:"from"`
	To   string `json:"to"`
	Data []struct {
		Date     string `json:"date"`
		Accepted int64  `json:"accepted"`
		Sent     int64  `json:"sent"`
	} `json:"data"`
	Totals struct {
		Accepted int64 `json:"accepted"`
	} `json:"totals"`
	Limits struct {
		SendsPerMinute int `json:"sends_per_minute"`
	} `json:"limits"`
}

func (s *stack) usage(t *testing.T, token, query string) usageBody {
	t.Helper()
	code, raw := s.send(t, token, request{method: "GET", path: "/v1/usage" + query})
	if code != http.StatusOK {
		t.Fatalf("GET /v1/usage%s: %d %s", query, code, raw)
	}
	var b usageBody
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (s *stack) sendEmails(t *testing.T, token string, n int) {
	t.Helper()
	for range n {
		code, body := s.send(t, token, request{method: "POST", path: "/v1/emails",
			body: `{"from":"app@example.test","to":"user@example.test","subject":"Hi","text":"x"}`})
		if code != http.StatusAccepted {
			t.Fatalf("send: %d %s", code, body)
		}
	}
}

func TestUsageDefaultsAndZeroFill(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t)
	today := time.Now().UTC().Format(time.DateOnly)
	s.sendEmails(t, token, 3)
	if _, err := usage.Rollup(context.Background(), s.pool, time.Now()); err != nil {
		t.Fatal(err)
	}
	if time.Now().UTC().Format(time.DateOnly) != today {
		t.Skip("the UTC day changed while the test ran")
	}

	b := s.usage(t, token, "")
	if len(b.Data) != 30 || b.To != today || b.Data[29].Date != today || b.Data[0].Date != b.From {
		t.Fatalf("default range: from %s to %s, %d days (last %+v)", b.From, b.To, len(b.Data), b.Data[len(b.Data)-1])
	}
	if b.Data[29].Accepted != 3 || b.Totals.Accepted != 3 {
		t.Errorf("today accepted %d, total %d, want 3", b.Data[29].Accepted, b.Totals.Accepted)
	}
	for _, d := range b.Data[:29] {
		if d.Accepted != 0 || d.Sent != 0 {
			t.Errorf("day %s not zero: %+v", d.Date, d)
		}
	}
	if b.Limits.SendsPerMinute != 1_000_000 {
		t.Errorf("limits.sends_per_minute = %d", b.Limits.SendsPerMinute)
	}
	if b := s.usage(t, token, "?from="+today+"&to="+today); len(b.Data) != 1 || b.Data[0].Accepted != 3 {
		t.Errorf("single day: %+v", b)
	}
}

func TestUsageIsolation(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, tokenA := s.project(t)
	_, tokenB := s.project(t)
	s.sendEmails(t, tokenA, 4)
	s.sendEmails(t, tokenB, 1)
	if _, err := usage.Rollup(context.Background(), s.pool, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := s.usage(t, tokenA, "").Totals.Accepted; got != 4 {
		t.Errorf("project A total %d, want 4", got)
	}
	if got := s.usage(t, tokenB, "").Totals.Accepted; got != 1 {
		t.Errorf("project B total %d, want only its own 1", got)
	}
	_, tokenC := s.project(t)
	if got := s.usage(t, tokenC, "").Totals.Accepted; got != 0 {
		t.Errorf("project without emails sees %d", got)
	}
}
