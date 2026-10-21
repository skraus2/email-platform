// Command worker runs the background worker process: it dispatches queued
// emails to the configured provider (Mailpit, or SES with the opt-in), polls
// SES events (SES only), delivers webhooks and runs periodic cleanup.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/events/ses"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/retention"
	"github.com/skraus2/email-platform/internal/templates"
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

	if !waitReady(ctx, logger, func(ctx context.Context) error { return database.Ready(ctx, pool, migrator) }) {
		logger.Info("shutdown before ready")
		return nil
	}
	provider, err := newProvider(ctx, cfg)
	if err != nil {
		return err
	}
	dispatcher := emails.NewDispatcher(pool, provider, logger, cfg.ProviderTimeout, cfg.BodyMaxAge)
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
	pruner := retention.Pruner{
		Pool: pool, Logger: logger, EventRetention: cfg.EventRetention,
		DeliveryRetention: cfg.WebhookDeliveryRetention, EmailRetention: cfg.EmailRetention,
	}
	fanout := broadcasts.NewFanout(pool, enqueuer, logger, templates.NewCache(emails.CompiledCacheBytes, emails.CompiledCacheEntryBytes))
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool:   pool,
		Logger: logger,
		Handlers: jobs.Handlers{
			SendEmail:       dispatcher.SendEmail,
			DeliverWebhook:  deliverer.Deliver,
			BroadcastFanout: fanout.Run,
			Cleanup: func(ctx context.Context) error {
				// Expiry first, so retention sees the final states.
				return errors.Join(dispatcher.Cleanup(ctx), deliverer.Cleanup(ctx), pruner.Run(ctx))
			},
		},
		Retry:       jobs.RetryPolicy{Base: cfg.RetryBaseDelay, Max: cfg.RetryMaxDelay},
		Concurrency: workerConcurrency,
		// A job may run for one provider call plus a few DB round trips; a job
		// still "running" well past that belongs to a dead worker.
		JobTimeout:           jobs.AttemptBudget(cfg.ProviderTimeout),
		RescueStuckJobsAfter: cfg.JobRescueAfter,
		WebhookConcurrency:   cfg.WebhookConcurrency,
		WebhookTimeout:       cfg.WebhookTimeout,
	})
	if err != nil {
		return err
	}
	poller, err := newEventPoller(ctx, cfg, dispatcher.Events(), logger)
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
	logger.Info("shutdown complete")
	return nil
}

const workerConcurrency = 10

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
func newEventPoller(ctx context.Context, cfg config.Config, rec *events.Recorder, logger *slog.Logger) (*ses.Poller, error) {
	if cfg.MailProvider != "ses" {
		return nil, nil
	}
	return ses.NewPoller(ctx, ses.PollerConfig{
		Region: cfg.SES.Region, QueueURL: cfg.SES.EventsQueueURL, TopicARN: cfg.SES.EventsTopicARN,
		Endpoint: cfg.SES.SQSEndpoint, Recorder: rec, Logger: logger,
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
