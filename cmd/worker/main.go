// Command worker runs the background worker process. For now it only proves
// controlled startup/shutdown against the database; job processing is not
// implemented yet.
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
	logger.Info("worker ready", slog.Int("registered_jobs", 0))

	<-ctx.Done()
	logger.Info("shutdown complete")
	return nil
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
