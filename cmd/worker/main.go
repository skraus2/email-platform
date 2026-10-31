// Command worker runs the background worker process: it dispatches queued
// emails to the configured provider (Mailpit, or SES with the opt-in), polls
// SES events (SES only), delivers webhooks and runs periodic cleanup.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/events/ses"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/metrics"
	"github.com/skraus2/email-platform/internal/retention"
	"github.com/skraus2/email-platform/internal/templates"
	"github.com/skraus2/email-platform/internal/usage"
	"github.com/skraus2/email-platform/internal/webhooks"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogFormat, cfg.LogLevel, "worker")
	if cfg.WebhookKeys == nil {
		// Every status change may create a webhook delivery; without keys
		// none could ever be signed.
		return errors.New("WEBHOOK_KEKS is required: webhook secrets cannot be decrypted without it")
	}

	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	migrator, err := database.NewMigrator(pool)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	logger.Info("worker starting", slog.String("env", cfg.Env), slog.String("database", cfg.RedactedDatabaseURL()))

	registry := metrics.NewRegistry()
	metrics.RegisterPool(registry, pool)
	metrics.RegisterBacklog(registry, pool, cfg.MailProvider, cfg.ProviderDailyLimit, logger)
	// The metrics listener starts before the database wait: a database that
	// is down shows as db_up 0, and a busy port fails the start at once.
	stopMetrics, err := serveMetrics(ctx, cfg.MetricsAddr, registry, logger)
	if err != nil {
		return err
	}
	defer stopMetrics(metricsShutdownTimeout) // no-op after the regular stop below

	if !waitReady(ctx, logger, func(ctx context.Context) error { return database.Ready(ctx, pool, migrator) }) {
		logger.Info("shutdown before ready")
		return nil
	}
	provider, err := newProvider(ctx, cfg)
	if err != nil {
		return err
	}
	dispatcher := emails.NewDispatcher(pool, provider, logger, cfg.ProviderTimeout, cfg.BodyMaxAge)
	dispatcher.Observe(metrics.NewDispatch(registry, emails.FailureCodes), cfg.MailProvider)
	dispatcher.Events().Observe(metrics.NewProviderEvents(registry, events.ProviderEventTypes(), events.IngestOutcomes()))
	dispatcher.Events().ObserveDuplicates(metrics.NewDuplicates(registry))
	if err := dispatcher.LimitProviderRate(cfg.ProviderRatePerSecond); err != nil {
		return err
	}
	if cfg.RequireVerifiedSender {
		dispatcher.RequireVerifiedSenders()
	}
	if cfg.ProviderDailyLimit > 0 {
		if err := dispatcher.LimitDailyHandoffs(cfg.MailProvider, cfg.ProviderDailyLimit); err != nil {
			return err
		}
	}
	enqueuer, err := jobs.NewEnqueuer(pool, logger, cfg.SendMaxAttempts)
	if err != nil {
		return err
	}
	dispatcher.Events().OnStatusChange(webhooks.NewOutbox(enqueuer).OnStatusChange)
	deliverer, err := webhooks.NewDeliverer(webhooks.DelivererConfig{
		Pool: pool, Logger: logger, Keys: cfg.WebhookKeys, Targets: cfg.WebhookTargets,
		Timeout: cfg.WebhookTimeout, MaxAttempts: cfg.WebhookMaxAttempts,
		Retry: jobs.RetryPolicy{Base: cfg.WebhookRetryBaseDelay, Max: cfg.WebhookRetryMaxDelay},
	})
	if err != nil {
		return err
	}
	deliverer.Observe(metrics.NewWebhooks(registry))
	pruner := retention.Pruner{
		Pool: pool, Logger: logger, EventRetention: cfg.EventRetention,
		DeliveryRetention: cfg.WebhookDeliveryRetention, EmailRetention: cfg.EmailRetention,
		ContactEventRetention: cfg.ContactEventRetention,
	}
	compiled := templates.NewCache(emails.CompiledCacheBytes, emails.CompiledCacheEntryBytes)
	fanout := broadcasts.NewFanout(pool, enqueuer, logger, compiled)
	engine := automations.NewEngine(pool, enqueuer, logger, compiled)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool:   pool,
		Logger: logger,
		Handlers: jobs.Handlers{
			SendEmail:         dispatcher.SendEmail,
			DeliverWebhook:    deliverer.Deliver,
			BroadcastFanout:   fanout.Run,
			AutomationStep:    engine.Step,
			AutomationArchive: automations.CancelArchivedRuns(pool),
			Cleanup: func(ctx context.Context) error {
				// Expiry first, so retention sees the final states.
				return errors.Join(dispatcher.Cleanup(ctx), deliverer.Cleanup(ctx), pruner.Run(ctx),
					automations.DeleteOldEventWindows(ctx, pool), engine.Requeue(ctx))
			},
			UsageRollup: usage.Roller{Pool: pool, Logger: logger, Metrics: metrics.NewUsageRollup(registry)}.Run,
		},
		Retry:       jobs.RetryPolicy{Base: cfg.RetryBaseDelay, Max: cfg.RetryMaxDelay},
		Concurrency: workerConcurrency,
		// A job may run for one provider call plus a few DB round trips; a job
		// still "running" well past that belongs to a dead worker.
		JobTimeout:            jobs.AttemptBudget(cfg.ProviderTimeout),
		RescueStuckJobsAfter:  cfg.JobRescueAfter,
		WebhookConcurrency:    cfg.WebhookConcurrency,
		WebhookTimeout:        cfg.WebhookTimeout,
		AutomationConcurrency: cfg.AutomationConcurrency,
	})
	if err != nil {
		return err
	}
	poller, err := newEventPoller(ctx, cfg, dispatcher.Events(), pool, registry, logger)
	if err != nil {
		return err
	}
	if err := worker.Start(ctx); err != nil {
		return fmt.Errorf("start job worker: %w", err)
	}
	var pollerDone chan struct{}
	if poller != nil {
		pollerDone = make(chan struct{})
		go func() {
			defer close(pollerDone)
			poller.Run(ctx)
		}()
	}
	if cfg.MailProvider == "ses" {
		logger.Info("worker ready", slog.String("provider", cfg.MailProvider), slog.String("region", cfg.SES.Region),
			slog.String("configuration_set", cfg.SES.ConfigurationSet), slog.Bool("stub_endpoint", cfg.SES.Endpoint != ""))
	} else {
		logger.Info("worker ready", slog.String("provider", cfg.MailProvider), slog.String("smtp_addr", cfg.SMTPAddr))
	}

	<-ctx.Done()
	logger.Info("shutdown started", slog.Duration("timeout", cfg.ShutdownTimeout))
	// The poller stops on the same cancellation, in parallel with the job
	// worker; both share one deadline.
	deadline := time.Now().Add(cfg.ShutdownTimeout)
	if err := worker.Stop(cfg.ShutdownTimeout); err != nil {
		logger.Warn("job worker did not stop cleanly", slog.Any("error", err))
	}
	if pollerDone != nil {
		timer := time.NewTimer(max(time.Until(deadline), 0))
		select {
		case <-pollerDone:
		case <-timer.C:
			select {
			case <-pollerDone: // stopped just as the deadline passed
			default:
				logger.Warn("ses event poller did not stop in time")
			}
		}
		timer.Stop()
	}
	stopMetrics(max(time.Until(deadline), minMetricsShutdown))
	logger.Info("shutdown complete")
	return nil
}

const workerConcurrency = 10

// serveMetrics serves the registry on addr ("" = off) until the returned
// stop function is called; stop shuts the listener down within its timeout
// and does nothing after the first call.
func serveMetrics(ctx context.Context, addr string, reg *prometheus.Registry, logger *slog.Logger) (func(time.Duration), error) {
	if addr == "" {
		return func(time.Duration) {}, nil
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on METRICS_ADDR %s: %w", addr, err)
	}
	host, _, _ := net.SplitHostPort(addr)
	if ip, err := netip.ParseAddr(host); !strings.EqualFold(host, "localhost") && (err != nil || !ip.IsLoopback()) {
		logger.Warn("metrics listener binds beyond loopback: anyone who reaches it can read the (aggregate, unauthenticated) metrics",
			slog.String("metrics_addr", addr))
	}
	srv := metrics.NewServer(reg, host, logger)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	logger.Info("metrics listener started", slog.String("metrics_addr", ln.Addr().String()))
	var once sync.Once
	// The stop function runs at shutdown, when the caller's context is
	// already done, so it starts from a fresh one.
	return func(timeout time.Duration) { //nolint:contextcheck // see above
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				logger.Warn("metrics listener did not stop cleanly", slog.Any("error", err))
				_ = srv.Close()
			}
			if err := <-done; !errors.Is(err, http.ErrServerClosed) {
				logger.Warn("metrics listener failed", slog.Any("error", err))
			}
		})
	}, nil
}

const (
	// metricsShutdownTimeout bounds the metrics listener's shutdown on an
	// early return (scrapes are short).
	metricsShutdownTimeout = 2 * time.Second
	// minMetricsShutdown is what the listener gets when the worker used up
	// the shutdown deadline.
	minMetricsShutdown = 100 * time.Millisecond
)

// newProvider builds the configured provider. An SES client exists only with
// the complete SES opt-in, which config.Load enforces; everything else gets
// the local Mailpit sink.
func newProvider(ctx context.Context, cfg config.Config) (delivery.Provider, error) {
	if cfg.MailProvider == "ses" {
		return delivery.NewSES(ctx, delivery.SESConfig{
			Region: cfg.SES.Region, ConfigurationSet: cfg.SES.ConfigurationSet, Endpoint: cfg.SES.Endpoint,
		})
	}
	return delivery.NewMailpit(cfg.SMTPAddr)
}

// newEventPoller builds the SES event poller, or nil without SES: the queue
// and topic settings, like every AWS client, exist only with the complete
// opt-in. Ingested events go through rec, which carries the webhook outbox.
func newEventPoller(ctx context.Context, cfg config.Config, rec *events.Recorder, pool *pgxpool.Pool, reg prometheus.Registerer, logger *slog.Logger) (*ses.Poller, error) {
	if cfg.MailProvider != "ses" {
		return nil, nil //nolint:nilnil // no poller without SES, as documented
	}
	return ses.NewPoller(ctx, ses.PollerConfig{
		Region: cfg.SES.Region, QueueURL: cfg.SES.EventsQueueURL, TopicARN: cfg.SES.EventsTopicARN,
		Endpoint: cfg.SES.SQSEndpoint, Recorder: rec, Logger: logger,
		Pool: pool, Metrics: metrics.NewEventQueue(reg, time.Now()),
		// A message being handled is finished on shutdown.
		MessageTimeout: cfg.ShutdownTimeout,
	})
}

// waitReady polls check with capped exponential backoff until it succeeds
// (true) or ctx is cancelled (false). A database that comes up after the
// worker is a normal startup race, not a fatal error.
func waitReady(ctx context.Context, logger *slog.Logger, check func(context.Context) error) bool {
	delay := 500 * time.Millisecond
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := check(attemptCtx)
		cancel()
		if err == nil {
			return true
		}
		logger.Warn("dependencies not ready", slog.Any("error", err), slog.Duration("retry_in", delay))
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		delay = nextDelay(delay)
	}
}

const maxReadyDelay = 30 * time.Second

func nextDelay(d time.Duration) time.Duration {
	return min(d*2, maxReadyDelay)
}
