// Command api runs the HTTP API process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/httpapi"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
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
	logger := logging.New(os.Stdout, cfg.LogFormat, cfg.LogLevel, "api")

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

	queries := store.New(pool)
	enqueuer, err := jobs.NewEnqueuer(pool, logger, cfg.SendMaxAttempts)
	if err != nil {
		return err
	}
	api := httpapi.New(httpapi.Deps{
		Logger: logger,
		Emails: emails.NewService(pool, enqueuer, logger),
		Authenticate: func(ctx context.Context, token string) (auth.Principal, error) {
			return auth.Authenticate(ctx, queries.GetActiveKeyByPrefix, token)
		},
		Checks: []httpapi.Check{{
			Name: "database",
			Fn:   func(ctx context.Context) error { return database.Ready(ctx, pool, migrator) },
		}},
	})

	httpServer := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// Listen before logging "started" so a busy port fails fast and loudly.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	logger.Info("api started",
		slog.String("addr", ln.Addr().String()),
		slog.String("env", cfg.Env),
		slog.String("database", cfg.RedactedDatabaseURL()),
	)

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(ln) }()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	stop() // a second signal now terminates immediately

	logger.Info("shutdown started", slog.Duration("timeout", cfg.ShutdownTimeout))
	api.SetDraining()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}
