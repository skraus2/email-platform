// Package config loads and validates process configuration from environment
// variables. Both cmd/api and cmd/worker use it; invalid configuration is a
// startup error, never a runtime surprise.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/skraus2/email-platform/internal/delivery"
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

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
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
