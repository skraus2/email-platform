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

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
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
