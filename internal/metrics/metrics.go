// Package metrics exposes the processes' Prometheus metrics: one registry
// per process (no global registry), HTTP request metrics, connection pool
// statistics and the domain counters and gauges the alert rules use. Labels
// come from closed sets only (route patterns, outcomes, error codes, queue
// names) — never ids, addresses or content.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "email_platform"

// NewRegistry returns a registry with the Go runtime and process collectors.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// Handler serves reg in the Prometheus exposition format. A collector that
// fails is reported in the output; the rest is still served.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError, Registry: reg})
}
