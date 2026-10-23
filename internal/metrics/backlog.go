package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
)

const (
	// backlogMaxAge: scrapes within this time reuse the last snapshot, so
	// several scrapers do not multiply the database queries.
	backlogMaxAge = 10 * time.Second
	// backlogTimeout bounds the snapshot queries of one scrape.
	backlogTimeout = 3 * time.Second
)

var (
	queueJobs = prometheus.NewDesc(namespace+"_queue_jobs", "Unfinished jobs by queue and state.", []string{"queue", "state"}, nil)
	queueLag  = prometheus.NewDesc(namespace+"_queue_oldest_available_seconds",
		"How long the oldest due job has waited for a worker, by queue and class (transactional or bulk).", []string{"queue", "class"}, nil)
	emailsOpen     = prometheus.NewDesc(namespace+"_emails_open", "Emails queued and due now (queued) or being sent (sending).", []string{"status"}, nil)
	webhookPending = prometheus.NewDesc(namespace+"_webhook_pending", "Pending webhook deliveries.", nil, nil)
	webhookOldest  = prometheus.NewDesc(namespace+"_webhook_oldest_pending_seconds", "How long the oldest due webhook delivery has waited.", nil, nil)
	handoffs       = prometheus.NewDesc(namespace+"_provider_handoffs_24h", "Messages handed to the provider in the last 24 hours (all projects).", []string{"provider"}, nil)
	dailyLimit     = prometheus.NewDesc(namespace+"_provider_daily_limit", "Configured daily hand-off cap (0 = none).", []string{"provider"}, nil)
	dbUp           = prometheus.NewDesc(namespace+"_db_up", "1 if the last backlog snapshot could be read from the database.", nil, nil)
)

// Backlog exports database-derived gauges (queues, open emails, webhook
// backlog, daily cap use) at scrape time.
type Backlog struct {
	pool       *pgxpool.Pool
	provider   string
	dailyLimit int

	mu      sync.Mutex
	at      time.Time
	ok      bool
	queue   jobs.QueueStats
	open    store.CountOpenEmailsRow
	hooks   store.WebhookBacklogRow
	handoff int64
}

// RegisterBacklog registers the backlog gauges for the configured provider
// and its daily cap.
func RegisterBacklog(reg prometheus.Registerer, pool *pgxpool.Pool, provider string, limit int) *Backlog {
	b := &Backlog{pool: pool, provider: provider, dailyLimit: limit}
	reg.MustRegister(b)
	return b
}

// Describe implements prometheus.Collector.
func (b *Backlog) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{queueJobs, queueLag, emailsOpen, webhookPending, webhookOldest, handoffs, dailyLimit, dbUp} {
		ch <- d
	}
}

// Collect implements prometheus.Collector. A database failure sets db_up to
// 0 and leaves the other gauges out; it never fails the scrape.
func (b *Backlog) Collect(ch chan<- prometheus.Metric) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.at) >= backlogMaxAge {
		b.refresh()
	}
	ch <- prometheus.MustNewConstMetric(dailyLimit, prometheus.GaugeValue, float64(b.dailyLimit), b.provider)
	if !b.ok {
		ch <- prometheus.MustNewConstMetric(dbUp, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(dbUp, prometheus.GaugeValue, 1)
	for _, q := range jobs.Queues() {
		for _, s := range jobs.UnfinishedStates {
			ch <- prometheus.MustNewConstMetric(queueJobs, prometheus.GaugeValue, float64(b.queue.Jobs[[2]string{q, s}]), q, s)
		}
		for _, c := range []string{jobs.ClassTransactional, jobs.ClassBulk} {
			ch <- prometheus.MustNewConstMetric(queueLag, prometheus.GaugeValue, b.queue.OldestAvailable[[2]string{q, c}], q, c)
		}
	}
	ch <- prometheus.MustNewConstMetric(emailsOpen, prometheus.GaugeValue, float64(b.open.QueuedDue), "queued")
	ch <- prometheus.MustNewConstMetric(emailsOpen, prometheus.GaugeValue, float64(b.open.Sending), "sending")
	ch <- prometheus.MustNewConstMetric(webhookPending, prometheus.GaugeValue, float64(b.hooks.Pending))
	ch <- prometheus.MustNewConstMetric(webhookOldest, prometheus.GaugeValue, b.hooks.OldestDueSeconds)
	ch <- prometheus.MustNewConstMetric(handoffs, prometheus.GaugeValue, float64(b.handoff), b.provider)
}

func (b *Backlog) refresh() {
	b.at = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), backlogTimeout)
	defer cancel()
	q := store.New(b.pool)
	var err error
	b.ok = false
	if b.queue, err = jobs.Stats(ctx, b.pool); err != nil {
		return
	}
	if b.open, err = q.CountOpenEmails(ctx); err != nil {
		return
	}
	if b.hooks, err = q.WebhookBacklog(ctx); err != nil {
		return
	}
	if b.handoff, err = q.CountProviderHandoffs(ctx, &b.provider); err != nil {
		return
	}
	b.ok = true
}
