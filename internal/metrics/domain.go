package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Dispatch outcomes of one send_email job run.
const (
	OutcomeHandedOff = "handed_off" // the provider accepted the message
	OutcomeFailed    = "failed"     // the email ended failed (see the error code)
	OutcomeCanceled  = "canceled"   // its broadcast or automation run was cancelled
	OutcomeRetry     = "retry"      // the provider call failed retryably
	OutcomeError     = "error"      // an internal error; the job is retried
	OutcomeSnoozed   = "snoozed"    // waits for the daily cap or the rate limit
	OutcomeSkipped   = "skipped"    // nothing to do (gone, already final, finalized concurrently)
)

var dispatchOutcomes = []string{OutcomeHandedOff, OutcomeFailed, OutcomeCanceled, OutcomeRetry, OutcomeError, OutcomeSnoozed, OutcomeSkipped}

// Dispatch counts send_email outcomes, failures by code and provider call
// durations. A nil *Dispatch records nothing.
type Dispatch struct {
	outcomes   *prometheus.CounterVec
	failures   *prometheus.CounterVec
	provider   *prometheus.HistogramVec
	reconciled *prometheus.CounterVec
}

// ReconcileResults are the results of a job run that found an email an
// earlier attempt left sending (with SES).
var ReconcileResults = []string{"found", "waiting", "pipeline_unhealthy", "resent"}

// NewDispatch registers the dispatch metrics; errorCodes is the closed set
// of failure codes, all exported at zero from the start.
func NewDispatch(reg prometheus.Registerer, errorCodes []string) *Dispatch {
	m := &Dispatch{
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "dispatch_total", Help: "send_email job runs by outcome.",
		}, []string{"outcome"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "dispatch_failures_total", Help: "Emails that ended failed, by error code (incl. expired by the safety net).",
		}, []string{"error_code"}),
		provider: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "provider_send_duration_seconds", Help: "Duration of provider send calls.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"provider", "result"}),
		reconciled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "dispatch_reconciliations_total",
			Help: "send_email job runs that found an email an earlier attempt left sending, by result (SES only); resent = no proof found, the email goes on to the usual checks and send.",
		}, []string{"result"}),
	}
	reg.MustRegister(m.outcomes, m.failures, m.provider, m.reconciled)
	for _, r := range ReconcileResults {
		m.reconciled.WithLabelValues(r)
	}
	for _, o := range dispatchOutcomes {
		m.outcomes.WithLabelValues(o)
	}
	for _, c := range errorCodes {
		m.failures.WithLabelValues(c)
	}
	return m
}

// Outcome counts one job run.
func (m *Dispatch) Outcome(outcome string) {
	if m != nil {
		m.outcomes.WithLabelValues(outcome).Inc()
	}
}

// Failed counts one job run that failed its email with code.
func (m *Dispatch) Failed(code string) {
	if m != nil {
		m.outcomes.WithLabelValues(OutcomeFailed).Inc()
		m.failures.WithLabelValues(code).Inc()
	}
}

// Reconciled counts one job run that looked for proof of an earlier attempt.
func (m *Dispatch) Reconciled(result string) {
	if m != nil {
		m.reconciled.WithLabelValues(result).Inc()
	}
}

// Expired counts n emails the safety net failed with code expired.
func (m *Dispatch) Expired(n int) {
	if m != nil && n > 0 {
		m.failures.WithLabelValues("expired").Add(float64(n))
	}
}

// ProviderSend records one provider call.
func (m *Dispatch) ProviderSend(provider string, ok bool, d time.Duration) {
	if m == nil {
		return
	}
	result := "ok"
	if !ok {
		result = "error"
	}
	m.provider.WithLabelValues(provider, result).Observe(d.Seconds())
}

// ProviderEvents counts ingested provider events. A nil *ProviderEvents
// records nothing.
type ProviderEvents struct{ events *prometheus.CounterVec }

// NewProviderEvents registers the provider event counter; every known type
// and outcome combination is exported at zero.
func NewProviderEvents(reg prometheus.Registerer, types, outcomes []string) *ProviderEvents {
	m := &ProviderEvents{events: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "provider_events_total",
		Help: "Provider events by type, bounce type (bounces only) and ingest outcome.",
	}, []string{"type", "bounce_type", "outcome"})}
	reg.MustRegister(m.events)
	for _, t := range types {
		for _, o := range outcomes {
			if t == "bounced" {
				for _, b := range bounceTypes {
					m.events.WithLabelValues(t, b, o)
				}
				continue
			}
			m.events.WithLabelValues(t, "none", o)
		}
	}
	return m
}

var bounceTypes = []string{"permanent", "transient", "undetermined"}

// BounceType maps a provider's bounce type to the closed label set.
func BounceType(eventType, providerBounceType string) string {
	if eventType != "bounced" {
		return "none"
	}
	switch providerBounceType {
	case "Permanent":
		return "permanent"
	case "Transient":
		return "transient"
	default:
		return "undetermined"
	}
}

// Event counts one ingested event.
func (m *ProviderEvents) Event(eventType, bounceType, outcome string) {
	if m != nil {
		m.events.WithLabelValues(eventType, bounceType, outcome).Inc()
	}
}

// Webhook delivery results.
const (
	WebhookDelivered = "delivered"
	WebhookRetry     = "retry"
	WebhookFailed    = "failed"
)

// Webhooks counts webhook delivery attempts by result. A nil *Webhooks
// records nothing.
type Webhooks struct{ results *prometheus.CounterVec }

// NewWebhooks registers the webhook delivery counter.
func NewWebhooks(reg prometheus.Registerer) *Webhooks {
	m := &Webhooks{results: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "webhook_deliveries_total",
		Help: "Webhook delivery attempts by result (failed includes deliveries expired by the safety net).",
	}, []string{"result"})}
	reg.MustRegister(m.results)
	for _, r := range []string{WebhookDelivered, WebhookRetry, WebhookFailed} {
		m.results.WithLabelValues(r)
	}
	return m
}

// Result counts n deliveries with result.
func (m *Webhooks) Result(result string, n int) {
	if m != nil && n > 0 {
		m.results.WithLabelValues(result).Add(float64(n))
	}
}

// UsageRollup tracks the periodic usage rollup. A nil *UsageRollup records
// nothing.
type UsageRollup struct {
	lastSuccess prometheus.Gauge
	failures    prometheus.Counter
}

// NewUsageRollup registers the usage rollup metrics.
func NewUsageRollup(reg prometheus.Registerer) *UsageRollup {
	m := &UsageRollup{
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "usage_rollup_last_success_timestamp_seconds",
			Help: "Unix time of the last successful usage rollup (0 = none since the process started).",
		}),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "usage_rollup_failures_total", Help: "Usage rollup runs that failed or timed out.",
		}),
	}
	reg.MustRegister(m.lastSuccess, m.failures)
	return m
}

// Run records the end of one rollup run.
func (m *UsageRollup) Run(at time.Time, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.failures.Inc()
		return
	}
	m.lastSuccess.Set(float64(at.Unix()))
}

// EventQueue tracks the SES event queue this process polls. A nil
// *EventQueue records nothing.
type EventQueue struct{ drained prometheus.Gauge }

// NewEventQueue registers the drained-queue gauge, starting at start (the
// process start): a poller that never drains the queue then looks stale
// after the same time as one that stopped draining it.
func NewEventQueue(reg prometheus.Registerer, start time.Time) *EventQueue {
	m := &EventQueue{drained: prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "ses_queue_drained_timestamp_seconds",
		Help: "Unix time this process last drained the SES event queue (a receive with less than a full batch, all handled); the process start until then.",
	})}
	reg.MustRegister(m.drained)
	m.drained.Set(float64(start.Unix()))
	return m
}

// Drained records a drained receive at at.
func (m *EventQueue) Drained(at time.Time) {
	if m != nil {
		m.drained.Set(float64(at.Unix()))
	}
}

// Duplicate send kinds.
const (
	DuplicatePossible  = "possible"  // an email an earlier attempt left sending was sent again
	DuplicateConfirmed = "confirmed" // the provider reported another accepted copy
)

// Duplicates counts duplicate sends. A nil *Duplicates records nothing.
type Duplicates struct{ sends *prometheus.CounterVec }

// NewDuplicates registers the duplicate send counter, both kinds at zero.
func NewDuplicates(reg prometheus.Registerer) *Duplicates {
	m := &Duplicates{sends: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "duplicate_sends_total",
		Help: "Duplicate sends: possible (sent again without proof the earlier attempt failed) and confirmed (another provider message id for the same email).",
	}, []string{"kind"})}
	reg.MustRegister(m.sends)
	for _, k := range []string{DuplicatePossible, DuplicateConfirmed} {
		m.sends.WithLabelValues(k)
	}
	return m
}

// Possible counts one send of an email an earlier attempt left sending.
func (m *Duplicates) Possible() {
	if m != nil {
		m.sends.WithLabelValues(DuplicatePossible).Inc()
	}
}

// Confirmed counts n duplicate sends the provider's events prove.
func (m *Duplicates) Confirmed(n int) {
	if m != nil && n > 0 {
		m.sends.WithLabelValues(DuplicateConfirmed).Add(float64(n))
	}
}
