// Package config loads and validates process configuration from environment
// variables. Both cmd/api and cmd/worker use it; invalid configuration is a
// startup error, never a runtime surprise.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/webhooks"
)

// Environment names. Production enables stricter validation.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// Config is the validated configuration shared by API and worker.
type Config struct {
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	LogFormat       string // "json" or "text"
	ShutdownTimeout time.Duration

	// Mail dispatch (worker). Only the local Mailpit sink exists: no setting
	// can point dispatch at a real mail server or SES.
	MailProvider    string
	SMTPAddr        string
	ProviderTimeout time.Duration
	BodyMaxAge      time.Duration

	// Dispatch retries. The API stamps SendMaxAttempts on each new job; the
	// worker spaces retries with the delays.
	SendMaxAttempts int
	RetryBaseDelay  time.Duration
	RetryMaxDelay   time.Duration

	// APIRateLimitPerMinute caps accepted sends per project and minute.
	APIRateLimitPerMinute int
	// ProviderRatePerSecond caps messages per second the worker process
	// hands to the provider.
	ProviderRatePerSecond float64
	// RequireVerifiedSender makes the API accept only from addresses that
	// match a verified sender identity of the project.
	RequireVerifiedSender bool

	// WebhookKeys encrypt and decrypt webhook endpoint secrets (nil when
	// WEBHOOK_KEKS is unset; processes that need it refuse to start).
	WebhookKeys *webhooks.Keyring
	// WebhookTargets limits which webhook URLs and addresses may be called.
	WebhookTargets webhooks.TargetPolicy

	// JobRescueAfter: a dispatch job still running this long (its worker
	// died) is handed to another attempt. At least one attempt budget.
	JobRescueAfter time.Duration
}

// LookupFunc matches os.LookupEnv so tests can inject an environment.
type LookupFunc func(key string) (string, bool)

// Load reads configuration via lookup and validates it. All problems are
// reported together so a misconfigured deployment fails with one clear message.
func Load(lookup LookupFunc) (Config, error) {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}

	var errs []error
	cfg := Config{
		Env:         get("APP_ENV", EnvDevelopment),
		HTTPAddr:    get("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL: get("DATABASE_URL", ""),
		LogFormat:   get("LOG_FORMAT", "json"),
	}

	switch cfg.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV must be one of development, test, production; got %q", cfg.Env))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(get("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL invalid: %w", err))
	}

	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text; got %q", cfg.LogFormat))
	}

	timeout, err := time.ParseDuration(get("SHUTDOWN_TIMEOUT", "15s"))
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT invalid: %w", err))
	case timeout <= 0:
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be positive"))
	default:
		cfg.ShutdownTimeout = timeout
	}

	if err := validateDatabaseURL(cfg.DatabaseURL, cfg.Env); err != nil {
		errs = append(errs, err)
	}

	cfg.MailProvider = get("MAIL_PROVIDER", "mailpit")
	if cfg.MailProvider != "mailpit" {
		errs = append(errs, fmt.Errorf("MAIL_PROVIDER must be mailpit (the only provider available); got %q", cfg.MailProvider))
	}
	cfg.SMTPAddr = get("SMTP_ADDR", "127.0.0.1:1025")
	if err := delivery.ValidateMailpitAddr(cfg.SMTPAddr); err != nil {
		errs = append(errs, fmt.Errorf("SMTP_ADDR: %w", err))
	}
	cfg.ProviderTimeout, errs = positiveDuration(get, "PROVIDER_TIMEOUT", "10s", errs)
	if cfg.ProviderTimeout > cfg.ShutdownTimeout && cfg.ShutdownTimeout > 0 {
		errs = append(errs, errors.New("PROVIDER_TIMEOUT must not exceed SHUTDOWN_TIMEOUT (a send must finish within shutdown)"))
	}
	cfg.BodyMaxAge, errs = positiveDuration(get, "BODY_MAX_AGE", "168h", errs)
	if cfg.BodyMaxAge > maxBodyMaxAge {
		errs = append(errs, errors.New("BODY_MAX_AGE must be at most 720h (message content is deleted by then at the latest)"))
	}

	cfg.RequireVerifiedSender, errs = boolSetting(get, "REQUIRE_VERIFIED_SENDER", "false", errs)

	if spec := get("WEBHOOK_KEKS", ""); spec != "" {
		keys, err := webhooks.ParseKeyring(spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("WEBHOOK_KEKS: %w", err))
		}
		cfg.WebhookKeys = keys
	}
	// Plain http and private targets are local conveniences; production
	// needs https, and private targets only by explicit choice.
	devDefault := strconv.FormatBool(cfg.Env != EnvProduction)
	cfg.WebhookTargets.AllowHTTP, errs = boolSetting(get, "WEBHOOK_ALLOW_HTTP", devDefault, errs)
	if cfg.WebhookTargets.AllowHTTP && cfg.Env == EnvProduction {
		errs = append(errs, errors.New("WEBHOOK_ALLOW_HTTP must not be true in production (webhooks need TLS)"))
	}
	cfg.WebhookTargets.AllowPrivate, errs = boolSetting(get, "WEBHOOK_ALLOW_PRIVATE", devDefault, errs)

	cfg.JobRescueAfter, errs = positiveDuration(get, "JOB_RESCUE_AFTER", "5m", errs)
	// A job is rescued this long after its attempt started. Leave a margin
	// beyond the attempt budget for the detached result write and the
	// queue's own bookkeeping, or a send that already reached the provider
	// could be rescued and sent again.
	if minRescue := jobs.AttemptBudget(cfg.ProviderTimeout) + 30*time.Second; cfg.JobRescueAfter > 0 && cfg.ProviderTimeout > 0 && cfg.JobRescueAfter < minRescue {
		errs = append(errs, fmt.Errorf("JOB_RESCUE_AFTER must be at least %v (PROVIDER_TIMEOUT + 60s), or running sends could be rescued and sent twice", minRescue))
	}
	cfg.ProviderRatePerSecond, errs = rateInRange(get, "PROVIDER_RATE_PER_SECOND", "10", errs)
	cfg.APIRateLimitPerMinute, errs = intInRange(get, "API_RATE_LIMIT_PER_MINUTE", "600", 1, 1_000_000, errs)
	cfg.SendMaxAttempts, errs = intInRange(get, "SEND_MAX_ATTEMPTS", "8", 1, maxSendAttempts, errs)
	cfg.RetryBaseDelay, errs = positiveDuration(get, "RETRY_BASE_DELAY", "30s", errs)
	if cfg.RetryBaseDelay > 0 && cfg.RetryBaseDelay < time.Second {
		errs = append(errs, errors.New("RETRY_BASE_DELAY must be at least 1s"))
	}
	cfg.RetryMaxDelay, errs = positiveDuration(get, "RETRY_MAX_DELAY", "1h", errs)
	if cfg.RetryMaxDelay > 0 && cfg.RetryMaxDelay < cfg.RetryBaseDelay {
		errs = append(errs, errors.New("RETRY_MAX_DELAY must not be shorter than RETRY_BASE_DELAY"))
	}
	if cfg.RetryMaxDelay > maxRetryDelay {
		errs = append(errs, errors.New("RETRY_MAX_DELAY must be at most 24h"))
	}
	if len(errs) == 0 {
		// Sanity check, not a hard bound (snoozes, queue lag and crash rescue
		// add time; the expiry job is the real safety net): a full retry
		// schedule must fit into the content retention.
		policy := jobs.RetryPolicy{Base: cfg.RetryBaseDelay, Max: cfg.RetryMaxDelay}
		schedule := policy.WorstCase(cfg.SendMaxAttempts) + time.Duration(cfg.SendMaxAttempts)*jobs.AttemptBudget(cfg.ProviderTimeout)
		if schedule >= cfg.BodyMaxAge {
			errs = append(errs, fmt.Errorf("retry schedule (up to %v with SEND_MAX_ATTEMPTS, RETRY_*_DELAY and PROVIDER_TIMEOUT) must be shorter than BODY_MAX_AGE", schedule))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// maxSendAttempts bounds how often one email can be retried; maxRetryDelay
// bounds the wait between two attempts.
const (
	maxSendAttempts = 25
	maxRetryDelay   = 24 * time.Hour
)

func intInRange(get func(string, string) string, key, def string, lo, hi int, errs []error) (int, []error) {
	n, err := strconv.Atoi(get(key, def))
	if err != nil || n < lo || n > hi {
		return 0, append(errs, fmt.Errorf("%s must be an integer between %d and %d", key, lo, hi))
	}
	return n, errs
}

// boolSetting accepts exactly "true" or "false", so a typo never silently
// turns a safety setting off.
func boolSetting(get func(string, string) string, key, def string, errs []error) (bool, []error) {
	switch get(key, def) {
	case "true":
		return true, errs
	case "false":
		return false, errs
	default:
		return false, append(errs, fmt.Errorf("%s must be true or false", key))
	}
}

func rateInRange(get func(string, string) string, key, def string, errs []error) (float64, []error) {
	r, err := strconv.ParseFloat(get(key, def), 64)
	if err != nil || !(r >= 0.01 && r <= 10_000) { // also rejects NaN
		return 0, append(errs, fmt.Errorf("%s must be a number between 0.01 and 10000", key))
	}
	return r, errs
}

// maxBodyMaxAge caps the content safety net so it cannot be disabled by
// configuration.
const maxBodyMaxAge = 720 * time.Hour

func positiveDuration(get func(string, string) string, key, def string, errs []error) (time.Duration, []error) {
	d, err := time.ParseDuration(get(key, def))
	switch {
	case err != nil:
		return 0, append(errs, fmt.Errorf("%s invalid: %w", key, err))
	case d <= 0:
		return 0, append(errs, fmt.Errorf("%s must be positive", key))
	}
	return d, errs
}

func validateDatabaseURL(raw, env string) error {
	if raw == "" {
		return errors.New("DATABASE_URL is required")
	}
	u, err := url.Parse(raw)
	// Never echo the URL or the parse error: both can contain the password.
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return errors.New("DATABASE_URL must be a postgres:// URL with a host")
	}
	if env == EnvProduction {
		switch u.Query().Get("sslmode") {
		case "require", "verify-ca", "verify-full":
		default:
			return errors.New("DATABASE_URL must set sslmode=require, verify-ca or verify-full in production")
		}
	}
	return nil
}

// RedactedDatabaseURL returns the database URL without password or query
// parameters (which may also carry credentials), safe for logs.
func (c Config) RedactedDatabaseURL() string {
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		return "<unparseable>"
	}
	u.RawQuery = ""
	return u.Redacted()
}
