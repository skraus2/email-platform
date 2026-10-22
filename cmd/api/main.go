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
	"sync"
	"syscall"
	"time"

	"github.com/skraus2/email-platform/internal/admin"
	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/httpapi"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
	"github.com/skraus2/email-platform/internal/webhooks"
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
	service := emails.NewService(pool, enqueuer, logger, cfg.APIRateLimitPerMinute)
	if cfg.RequireVerifiedSender {
		service.RequireVerifiedSenders()
	}
	service.LimitScheduleAhead(cfg.ScheduleMaxAhead)
	service.LimitScheduledOpen(cfg.ScheduleMaxOpen)
	// Cancellations are status changes: they reach subscribed webhook
	// endpoints like those made by the worker.
	service.Recorder().OnStatusChange(webhooks.NewOutbox(enqueuer).OnStatusChange)
	broadcastStore := broadcasts.NewStore(pool, enqueuer, cfg.BroadcastMaxOpen)
	broadcastStore.LimitScheduleAhead(cfg.ScheduleMaxAhead)
	broadcastStore.LimitCreates(cfg.BroadcastCreatesPerHour)
	if cfg.RequireVerifiedSender {
		broadcastStore.RequireVerifiedSenders()
	}
	automationStore := automations.NewStore(pool, enqueuer, cfg.AutomationMaxPerProject)
	if cfg.RequireVerifiedSender {
		automationStore.RequireVerifiedSenders()
	}
	// Registration through the API is opt-in: only an API process given the
	// webhook key-encryption keys can store new endpoint secrets.
	var registrar httpapi.WebhookRegistrar
	if cfg.WebhookKeys != nil {
		registrar = webhooks.Registrar{Pool: pool, Keys: cfg.WebhookKeys, Policy: cfg.WebhookTargets, MaxEndpoints: cfg.WebhookMaxEndpointsPerProject}
		logger.Info("webhook endpoint registration through the API is enabled")
	}
	api := httpapi.New(httpapi.Deps{
		Logger:      logger,
		Emails:      service,
		Webhooks:    registrar,
		Templates:   templates.NewStore(pool, cfg.TemplateMaxPerProject, cfg.TemplateMaxVersions, int64(cfg.TemplateMaxStorageBytes), cfg.TemplateWritesPerMinute),
		Contacts:    contacts.NewStore(pool, cfg.ContactMaxPerProject),
		Audiences:   contacts.NewAudiences(pool, cfg.AudienceMaxPerProject, cfg.AudienceMaxContacts),
		Broadcasts:  broadcastStore,
		Automations: automationStore,
		Authenticate: func(ctx context.Context, token string) (auth.Principal, error) {
			return auth.Authenticate(ctx, queries.GetActiveKeyByPrefix, token)
		},
		Checks: []httpapi.Check{{
			Name: "database",
			Fn:   func(ctx context.Context) error { return database.Ready(ctx, pool, migrator) },
		}},
	})

	newServer := func(h http.Handler) *http.Server {
		return &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    64 << 10,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		}
	}
	servers := []*http.Server{newServer(api.Handler())}
	addrs := []string{cfg.HTTPAddr}
	if cfg.AdminHTTPAddr != "" {
		// The operator API: its own listener and mux, never reachable with a
		// project key.
		adminHost, _, _ := net.SplitHostPort(cfg.AdminHTTPAddr)
		adminAPI := httpapi.NewAdmin(httpapi.AdminDeps{
			Logger:     logger,
			Sessions:   admin.NewService(pool, logger, cfg.AdminSessionIdle, cfg.AdminSessionMax),
			Views:      admin.NewViews(pool),
			ListenHost: adminHost,
		})
		if !cfg.AdminBindIsLoopback() {
			logger.Warn("admin API listens beyond loopback without TLS: admin passwords and session tokens cross the network in clear",
				slog.String("admin_addr", cfg.AdminHTTPAddr))
		}
		servers = append(servers, newServer(adminAPI.Handler()))
		addrs = append(addrs, cfg.AdminHTTPAddr)
	}

	// Listen before logging "started" so a busy port fails fast and loudly.
	listeners := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return fmt.Errorf("listen on %s: %w", addr, err)
		}
		listeners = append(listeners, ln)
	}
	attrs := []any{
		slog.String("addr", listeners[0].Addr().String()),
		slog.String("env", cfg.Env),
		slog.String("database", cfg.RedactedDatabaseURL()),
	}
	if len(listeners) > 1 {
		attrs = append(attrs, slog.String("admin_addr", listeners[1].Addr().String()))
	}
	logger.Info("api started", attrs...)

	serveErr := make(chan error, len(servers))
	for i, srv := range servers {
		go func() { serveErr <- srv.Serve(listeners[i]) }()
	}

	var failed error
	select {
	case err := <-serveErr:
		// One listener died: shut the others down too before returning.
		failed = fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	stop() // a second signal now terminates immediately

	logger.Info("shutdown started", slog.Duration("timeout", cfg.ShutdownTimeout))
	api.SetDraining()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	// All listeners drain at the same time, within one deadline.
	shutdownErrs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Go(func() { shutdownErrs[i] = srv.Shutdown(shutdownCtx) })
	}
	wg.Wait()
	remaining := len(servers)
	if failed != nil {
		remaining-- // its Serve error was already received
	}
	for range remaining {
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			failed = errors.Join(failed, fmt.Errorf("http server: %w", err))
		}
	}
	for _, err := range shutdownErrs {
		if err != nil {
			failed = errors.Join(failed, fmt.Errorf("graceful shutdown: %w", err))
		}
	}
	if failed != nil {
		return failed
	}
	logger.Info("shutdown complete")
	return nil
}
