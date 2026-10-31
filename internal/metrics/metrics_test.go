package metrics_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/skraus2/email-platform/internal/metrics"
)

// series returns `name{label=value,…} value` for every sample of the
// families whose name starts with prefix, sorted; histograms report their
// sample count.
func series(t *testing.T, reg *prometheus.Registry, prefix string) []string {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), prefix) {
			continue
		}
		for _, m := range f.GetMetric() {
			var labels []string
			for _, l := range m.GetLabel() {
				labels = append(labels, l.GetName()+"="+l.GetValue())
			}
			var v float64
			switch f.GetType() { //nolint:exhaustive // the registry under test has counters, gauges and histograms only
			case dto.MetricType_HISTOGRAM:
				v = float64(m.GetHistogram().GetSampleCount())
			case dto.MetricType_GAUGE:
				v = m.GetGauge().GetValue()
			default:
				v = m.GetCounter().GetValue()
			}
			out = append(out, fmt.Sprintf("%s{%s} %g", f.GetName(), strings.Join(labels, ","), v))
		}
	}
	slices.Sort(out)
	return out
}

func TestHTTPLabelsByPattern(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewHTTP(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/emails/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/v1/emails/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })
	mux.HandleFunc("POST /v1/emails", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	h := m.Wrap(mux)

	for _, r := range []struct{ method, path string }{
		{"GET", "/v1/emails/0199a000-0000-7000-8000-000000000001"},
		{"GET", "/v1/emails/0199a000-0000-7000-8000-000000000002"},
		{"POST", "/v1/emails"},
		{"FOO1", "/v1/emails/x"},
		{"FOO2", "/v1/emails/y"},
		{"GET", "/no/such/path/secret-looking-value"},
		{"BAR", "/anything"},
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(r.method, r.path, nil))
	}
	got := series(t, reg, "email_platform_http_requests_total")
	want := []string{
		"email_platform_http_requests_total{code=202,route=POST /v1/emails} 1",
		"email_platform_http_requests_total{code=404,route=/} 2",
		"email_platform_http_requests_total{code=404,route=GET /v1/emails/{id}} 2",
		"email_platform_http_requests_total{code=405,route=/v1/emails/{id}} 2",
	}
	if !slices.Equal(got, want) {
		t.Errorf("series\n got %q\nwant %q", got, want)
	}
	if got := series(t, reg, "email_platform_http_request_duration_seconds"); len(got) != 4 {
		t.Errorf("duration series %q, want one per route", got)
	}
}

func TestHTTPNilIsPassThrough(t *testing.T) {
	var m *metrics.HTTP
	called := false
	h := m.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("nil HTTP did not call the handler")
	}
}

func TestServerServesOnlyMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	srv := metrics.NewServer(reg, "127.0.0.1", slog.New(slog.DiscardHandler))
	for _, tc := range []struct {
		method, path, host string
		code               int
	}{
		{"GET", "/metrics", "127.0.0.1:9091", 200},
		{"GET", "/metrics", "localhost:9091", 200},
		{"GET", "/metrics", "evil.example.test", http.StatusMisdirectedRequest},
		{"POST", "/metrics", "127.0.0.1:9091", http.StatusMethodNotAllowed},
		{"GET", "/admin/v1/me", "127.0.0.1:9091", http.StatusNotFound},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Errorf("%s %s (Host %s) = %d, want %d", tc.method, tc.path, tc.host, rec.Code, tc.code)
		}
		if tc.code == 200 {
			body, _ := io.ReadAll(rec.Body)
			if !strings.Contains(string(body), "go_goroutines") || !strings.Contains(string(body), "process_") {
				t.Errorf("metrics body lacks the runtime/process collectors")
			}
		}
	}
	if srv.ReadHeaderTimeout == 0 || srv.WriteTimeout == 0 {
		t.Error("metrics server without timeouts")
	}
}

func TestCountersStartAtZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics.NewDispatch(reg, []string{"suppressed", "expired"})
	metrics.NewProviderEvents(reg, []string{"delivered", "bounced"}, []string{"stored", "duplicate"})
	metrics.NewWebhooks(reg)
	got := series(t, reg, "email_platform_")
	for _, want := range []string{
		"email_platform_dispatch_total{outcome=handed_off} 0",
		"email_platform_dispatch_total{outcome=snoozed} 0",
		"email_platform_dispatch_failures_total{error_code=expired} 0",
		"email_platform_provider_events_total{bounce_type=permanent,outcome=stored,type=bounced} 0",
		"email_platform_provider_events_total{bounce_type=none,outcome=duplicate,type=delivered} 0",
		"email_platform_webhook_deliveries_total{result=failed} 0",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if slices.Contains(got, "email_platform_provider_events_total{bounce_type=none,outcome=stored,type=bounced} 0") {
		t.Error("bounces exported without a bounce type")
	}
}

func TestBounceType(t *testing.T) {
	for _, c := range [][3]string{
		{"bounced", "Permanent", "permanent"}, {"bounced", "Transient", "transient"},
		{"bounced", "Undetermined", "undetermined"}, {"bounced", "", "undetermined"},
		{"delivered", "Permanent", "none"}, {"complained", "", "none"},
	} {
		if got := metrics.BounceType(c[0], c[1]); got != c[2] {
			t.Errorf("BounceType(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestUsageRollupMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewUsageRollup(reg)
	var nilM *metrics.UsageRollup
	nilM.Run(time.Now(), nil) // no panic
	m.Run(time.Unix(1_800_000_000, 0), nil)
	m.Run(time.Unix(1_800_000_300, 0), errors.New("timeout"))
	got := series(t, reg, "email_platform_usage_rollup")
	want := []string{
		"email_platform_usage_rollup_failures_total{} 1",
		"email_platform_usage_rollup_last_success_timestamp_seconds{} 1.8e+09",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEventQueueGaugeStartsAtProcessStart(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewEventQueue(reg, time.Unix(1_800_000_000, 0))
	want := []string{"email_platform_ses_queue_drained_timestamp_seconds{} 1.8e+09"}
	if got := series(t, reg, "email_platform_ses_queue"); !slices.Equal(got, want) {
		t.Fatalf("at start: got %q, want %q", got, want)
	}
	var nilM *metrics.EventQueue
	nilM.Drained(time.Now()) // no panic
	m.Drained(time.Unix(1_800_000_060, 0))
	want = []string{"email_platform_ses_queue_drained_timestamp_seconds{} 1.80000006e+09"}
	if got := series(t, reg, "email_platform_ses_queue"); !slices.Equal(got, want) {
		t.Fatalf("after drain: got %q, want %q", got, want)
	}
}

func TestDuplicatesCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewDuplicates(reg)
	want := []string{"email_platform_duplicate_sends_total{kind=confirmed} 0", "email_platform_duplicate_sends_total{kind=possible} 0"}
	if got := series(t, reg, "email_platform_duplicate"); !slices.Equal(got, want) {
		t.Fatalf("at start: got %q, want %q", got, want)
	}
	var nilM *metrics.Duplicates
	nilM.Possible()
	nilM.Confirmed(1) // no panic
	m.Possible()
	m.Confirmed(2)
	m.Confirmed(0)
	want = []string{"email_platform_duplicate_sends_total{kind=confirmed} 2", "email_platform_duplicate_sends_total{kind=possible} 1"}
	if got := series(t, reg, "email_platform_duplicate"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
