package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// HTTP records requests by matched route pattern and status code.
type HTTP struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewHTTP registers the HTTP request metrics with reg.
func NewHTTP(reg prometheus.Registerer) *HTTP {
	m := &HTTP{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "http_requests_total",
			Help: "HTTP requests by matched route pattern and status code.",
		}, []string{"route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "http_request_duration_seconds",
			Help:    "HTTP request duration by matched route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// Wrap measures next, which must be a ServeMux (or contain one that serves
// the request object it is given): the route label is the pattern the mux
// matched, which carries the method where the route has one. Raw methods and
// paths are never labels, so arbitrary requests cannot create series. A nil
// HTTP returns next unchanged.
func (m *HTTP) Wrap(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.requests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		m.duration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b) //nolint:wrapcheck // an io.Writer wrapper passes the underlying error through
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
