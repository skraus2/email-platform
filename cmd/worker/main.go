// Command worker runs the background worker process: it dispatches queued
// emails to the configured provider and runs periodic cleanup.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
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
	provider, err := delivery.NewMailpit(cfg.SMTPAddr)
	if err != nil {
		return err
	}
	dispatcher := emails.NewDispatcher(pool, provider, logger, cfg.ProviderTimeout, cfg.BodyMaxAge)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool:   pool,
		Logger: logger,
		Handlers: jobs.Handlers{
			SendEmail: dispatcher.SendEmail,
			Cleanup:   dispatcher.Cleanup,
		},
		Concurrency: workerConcurrency,
		// A job may run for one provider call plus a few DB round trips; a job
		// still "running" well past that belongs to a dead worker.
		JobTimeout:           cfg.ProviderTimeout + 30*time.Second,
		RescueStuckJobsAfter: 5 * time.Minute,
	})
	if err != nil {
		return err
	}
	if err := worker.Start(ctx); err != nil {
		return fmt.Errorf("start job worker: %w", err)
	}
	logger.Info("worker ready", slog.String("provider", cfg.MailProvider), slog.String("smtp_addr", cfg.SMTPAddr))

	<-ctx.Done()
	logger.Info("shutdown started", slog.Duration("timeout", cfg.ShutdownTimeout))
	if err := worker.Stop(cfg.ShutdownTimeout); err != nil {
		logger.Warn("job worker did not stop cleanly", slog.Any("error", err))
	}
	logger.Info("shutdown complete")
	return nil
}

const workerConcurrency = 10

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
