package metrics

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/httpguard"
)

// NewServer is a listener's server that serves only GET /metrics of reg, to
// requests naming listenHost, a loopback address or localhost.
func NewServer(reg *prometheus.Registry, listenHost string, logger *slog.Logger) *http.Server {
	metrics := Handler(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if !httpguard.HostAllowed(r, listenHost) {
			http.Error(w, "this host name is not served here", http.StatusMisdirectedRequest)
			return
		}
		metrics.ServeHTTP(w, r)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}
