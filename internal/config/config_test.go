package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) LookupFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

const devURL = "postgres://ep:s3cret@localhost:54320/ep?sslmode=disable"

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvDevelopment {
		t.Errorf("Env = %q, want %q", cfg.Env, EnvDevelopment)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf("HTTPAddr = %q, want loopback default", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "json" {
		t.Errorf("log = %v/%q, want info/json", cfg.LogLevel, cfg.LogFormat)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"APP_ENV":          "test",
		"HTTP_ADDR":        ":9090",
		"DATABASE_URL":     devURL,
		"LOG_LEVEL":        "debug",
		"LOG_FORMAT":       "text",
		"SHUTDOWN_TIMEOUT": "3s",
		"PROVIDER_TIMEOUT": "2s",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvTest || cfg.HTTPAddr != ":9090" || cfg.LogLevel != slog.LevelDebug ||
		cfg.LogFormat != "text" || cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"non-postgres url", map[string]string{"DATABASE_URL": "mysql://u:p@h/db"}, "postgres:// URL"},
		{"unknown env", map[string]string{"DATABASE_URL": devURL, "APP_ENV": "staging"}, "APP_ENV"},
		{"bad log level", map[string]string{"DATABASE_URL": devURL, "LOG_LEVEL": "loud"}, "LOG_LEVEL"},
		{"bad log format", map[string]string{"DATABASE_URL": devURL, "LOG_FORMAT": "xml"}, "LOG_FORMAT"},
		{"bad timeout", map[string]string{"DATABASE_URL": devURL, "SHUTDOWN_TIMEOUT": "soon"}, "SHUTDOWN_TIMEOUT"},
		{"negative timeout", map[string]string{"DATABASE_URL": devURL, "SHUTDOWN_TIMEOUT": "-1s"}, "SHUTDOWN_TIMEOUT"},
		{"production without tls", map[string]string{"DATABASE_URL": devURL, "APP_ENV": "production"}, "sslmode"},
		{"max attempts zero", map[string]string{"DATABASE_URL": devURL, "SEND_MAX_ATTEMPTS": "0"}, "SEND_MAX_ATTEMPTS"},
		{"max attempts too high", map[string]string{"DATABASE_URL": devURL, "SEND_MAX_ATTEMPTS": "26"}, "SEND_MAX_ATTEMPTS"},
		{"max attempts not a number", map[string]string{"DATABASE_URL": devURL, "SEND_MAX_ATTEMPTS": "many"}, "SEND_MAX_ATTEMPTS"},
		{"base delay below 1s", map[string]string{"DATABASE_URL": devURL, "RETRY_BASE_DELAY": "500ms"}, "RETRY_BASE_DELAY"},
		{"max delay below base", map[string]string{"DATABASE_URL": devURL, "RETRY_BASE_DELAY": "1m", "RETRY_MAX_DELAY": "30s"}, "RETRY_MAX_DELAY"},
		{"bad max delay", map[string]string{"DATABASE_URL": devURL, "RETRY_MAX_DELAY": "later"}, "RETRY_MAX_DELAY"},
		{"rescue before job timeout", map[string]string{"DATABASE_URL": devURL, "JOB_RESCUE_AFTER": "30s"}, "JOB_RESCUE_AFTER"},
		{"rescue without margin", map[string]string{"DATABASE_URL": devURL, "JOB_RESCUE_AFTER": "40s"}, "JOB_RESCUE_AFTER"},
		{"bad rescue", map[string]string{"DATABASE_URL": devURL, "JOB_RESCUE_AFTER": "soon"}, "JOB_RESCUE_AFTER"},
		{"provider rate zero", map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "0"}, "PROVIDER_RATE_PER_SECOND"},
		{"provider rate NaN", map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "NaN"}, "PROVIDER_RATE_PER_SECOND"},
		{"provider rate below minimum", map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "0.005"}, "PROVIDER_RATE_PER_SECOND"},
		{"provider rate negative", map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "-1"}, "PROVIDER_RATE_PER_SECOND"},
		{"provider rate Inf", map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "Inf"}, "PROVIDER_RATE_PER_SECOND"},
		{"provider rate overflow", map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "1e400"}, "PROVIDER_RATE_PER_SECOND"},
		{"provider rate too high", map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "20000"}, "PROVIDER_RATE_PER_SECOND"},
		{"rate limit zero", map[string]string{"DATABASE_URL": devURL, "API_RATE_LIMIT_PER_MINUTE": "0"}, "API_RATE_LIMIT_PER_MINUTE"},
		{"rate limit too high", map[string]string{"DATABASE_URL": devURL, "API_RATE_LIMIT_PER_MINUTE": "1000001"}, "API_RATE_LIMIT_PER_MINUTE"},
		{"max delay above a day", map[string]string{"DATABASE_URL": devURL, "RETRY_MAX_DELAY": "25h"}, "RETRY_MAX_DELAY"},
		{"retries outlive content", map[string]string{"DATABASE_URL": devURL, "SEND_MAX_ATTEMPTS": "25", "RETRY_BASE_DELAY": "1h", "RETRY_MAX_DELAY": "24h", "BODY_MAX_AGE": "48h"}, "BODY_MAX_AGE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"APP_ENV": "nope", "LOG_FORMAT": "xml"}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"APP_ENV", "LOG_FORMAT", "DATABASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadProductionRequiresTLS(t *testing.T) {
	_, err := Load(env(map[string]string{
		"APP_ENV":      "production",
		"DATABASE_URL": "postgres://ep:s3cret@db:5432/ep?sslmode=verify-full",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestErrorsNeverContainPassword(t *testing.T) {
	cases := []map[string]string{
		{"DATABASE_URL": "mysql://ep:s3cret@h/db"},
		{"DATABASE_URL": "postgres://ep:s3cret@db/ep", "APP_ENV": "production"},
		{"DATABASE_URL": "postgres://ep:s3cret@[::1/ep"}, // unparseable
	}
	for _, c := range cases {
		_, err := Load(env(c))
		if err == nil {
			t.Fatalf("want error for %v", c)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error leaks password: %v", err)
		}
	}
}

func TestRedactedDatabaseURL(t *testing.T) {
	cfg := Config{DatabaseURL: "postgres://ep:s3cret@localhost:54320/ep?sslmode=disable&password=other"}
	got := cfg.RedactedDatabaseURL()
	if strings.Contains(got, "s3cret") || strings.Contains(got, "other") {
		t.Errorf("RedactedDatabaseURL leaks credentials: %q", got)
	}
	if !strings.Contains(got, "localhost:54320/ep") {
		t.Errorf("RedactedDatabaseURL lost host/db: %q", got)
	}
}

func TestLoadMailDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MailProvider != "mailpit" || cfg.SMTPAddr != "127.0.0.1:1025" ||
		cfg.ProviderTimeout != 10*time.Second || cfg.BodyMaxAge != 168*time.Hour {
		t.Fatalf("mail defaults = %q %q %v %v", cfg.MailProvider, cfg.SMTPAddr, cfg.ProviderTimeout, cfg.BodyMaxAge)
	}
}

func TestLoadRetryDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JobRescueAfter != 5*time.Minute {
		t.Errorf("JobRescueAfter = %v, want 5m", cfg.JobRescueAfter)
	}
	if cfg.ProviderRatePerSecond != 10 {
		t.Errorf("ProviderRatePerSecond = %v, want 10", cfg.ProviderRatePerSecond)
	}
	if cfg.APIRateLimitPerMinute != 600 {
		t.Errorf("APIRateLimitPerMinute = %d, want 600", cfg.APIRateLimitPerMinute)
	}
	if cfg.SendMaxAttempts != 8 || cfg.RetryBaseDelay != 30*time.Second || cfg.RetryMaxDelay != time.Hour {
		t.Fatalf("retry defaults = %d %v %v", cfg.SendMaxAttempts, cfg.RetryBaseDelay, cfg.RetryMaxDelay)
	}
	cfg, err = Load(env(map[string]string{"DATABASE_URL": devURL, "SEND_MAX_ATTEMPTS": "20", "RETRY_BASE_DELAY": "1s", "RETRY_MAX_DELAY": "5s"}))
	if err != nil {
		t.Fatal(err)
	}
	if frac, err := Load(env(map[string]string{"DATABASE_URL": devURL, "PROVIDER_RATE_PER_SECOND": "0.5"})); err != nil || frac.ProviderRatePerSecond != 0.5 {
		t.Errorf("fractional provider rate: %v, %v", frac.ProviderRatePerSecond, err)
	}
	if cfg.SendMaxAttempts != 20 || cfg.RetryBaseDelay != time.Second || cfg.RetryMaxDelay != 5*time.Second {
		t.Fatalf("retry overrides = %d %v %v", cfg.SendMaxAttempts, cfg.RetryBaseDelay, cfg.RetryMaxDelay)
	}
}

func TestLoadRejectsUnsafeMailConfig(t *testing.T) {
	tests := map[string]map[string]string{
		"ses provider":         {"MAIL_PROVIDER": "ses"},
		"unknown provider":     {"MAIL_PROVIDER": "smtp"},
		"remote smtp host":     {"SMTP_ADDR": "smtp.gmail.com:587"},
		"aws smtp host":        {"SMTP_ADDR": "email-smtp.eu-central-1.amazonaws.com:587"},
		"private ip":           {"SMTP_ADDR": "10.0.0.5:25"},
		"no port":              {"SMTP_ADDR": "127.0.0.1"},
		"timeout > shutdown":   {"PROVIDER_TIMEOUT": "20s", "SHUTDOWN_TIMEOUT": "15s"},
		"bad provider timeout": {"PROVIDER_TIMEOUT": "fast"},
		"zero body max age":    {"BODY_MAX_AGE": "0s"},
		"huge body max age":    {"BODY_MAX_AGE": "876000h"},
	}
	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			m := map[string]string{"DATABASE_URL": devURL}
			for k, v := range extra {
				m[k] = v
			}
			if _, err := Load(env(m)); err == nil {
				t.Fatalf("Load accepted %v", extra)
			}
		})
	}
}

func TestLoadAcceptsComposeMailpitHost(t *testing.T) {
	if _, err := Load(env(map[string]string{"DATABASE_URL": devURL, "SMTP_ADDR": "mailpit:1025"})); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRequireVerifiedSender(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL}))
	if err != nil || cfg.RequireVerifiedSender {
		t.Fatalf("default = %v, %v; want off for the local test sink", cfg.RequireVerifiedSender, err)
	}
	cfg, err = Load(env(map[string]string{"DATABASE_URL": devURL, "REQUIRE_VERIFIED_SENDER": "true"}))
	if err != nil || !cfg.RequireVerifiedSender {
		t.Fatalf("true = %v, %v", cfg.RequireVerifiedSender, err)
	}
	for _, bad := range []string{"yes", "1", "TRUE ", "on"} {
		if _, err := Load(env(map[string]string{"DATABASE_URL": devURL, "REQUIRE_VERIFIED_SENDER": bad})); err == nil || !strings.Contains(err.Error(), "REQUIRE_VERIFIED_SENDER") {
			t.Errorf("%q: err = %v, want a REQUIRE_VERIFIED_SENDER error", bad, err)
		}
	}
}

func TestLoadWebhookSettings(t *testing.T) {
	kek := "k1:" + strings.Repeat("A", 43) + "="
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebhookKeys != nil || !cfg.WebhookTargets.AllowHTTP || !cfg.WebhookTargets.AllowPrivate {
		t.Fatalf("development defaults = keys %v, %+v; want no keys, http and private allowed", cfg.WebhookKeys, cfg.WebhookTargets)
	}
	cfg, err = Load(env(map[string]string{"DATABASE_URL": devURL, "WEBHOOK_KEKS": kek, "WEBHOOK_ALLOW_PRIVATE": "false"}))
	if err != nil || cfg.WebhookKeys == nil || cfg.WebhookKeys.ActiveKeyID() != "k1" || cfg.WebhookTargets.AllowPrivate {
		t.Fatalf("explicit = %+v, %v", cfg.WebhookTargets, err)
	}

	prod := map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://ep:s3cret@db.internal:5432/ep?sslmode=require"}
	cfg, err = Load(env(prod))
	if err != nil || cfg.WebhookTargets.AllowHTTP || cfg.WebhookTargets.AllowPrivate {
		t.Fatalf("production defaults = %+v, %v; want both off", cfg.WebhookTargets, err)
	}
	prod["WEBHOOK_ALLOW_PRIVATE"] = "true"
	if cfg, err := Load(env(prod)); err != nil || !cfg.WebhookTargets.AllowPrivate {
		t.Fatalf("production private opt-in = %+v, %v", cfg.WebhookTargets, err)
	}
	prod["WEBHOOK_ALLOW_HTTP"] = "true"
	if _, err := Load(env(prod)); err == nil || !strings.Contains(err.Error(), "WEBHOOK_ALLOW_HTTP") {
		t.Fatalf("plain http in production: err = %v", err)
	}

	secret := strings.Repeat("B", 43) + "="
	for _, bad := range []string{"k1:short", "k1:" + secret + ",k1:" + secret, "nokey"} {
		_, err := Load(env(map[string]string{"DATABASE_URL": devURL, "WEBHOOK_KEKS": bad}))
		if err == nil || !strings.Contains(err.Error(), "WEBHOOK_KEKS") {
			t.Errorf("%q: err = %v", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks the key: %v", err)
		}
	}
}

func TestLoadWebhookDelivery(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebhookMaxAttempts != 12 || cfg.WebhookTimeout != 10*time.Second || cfg.WebhookConcurrency != 5 ||
		cfg.WebhookRetryBaseDelay != 30*time.Second || cfg.WebhookRetryMaxDelay != time.Hour {
		t.Fatalf("defaults = %d %v %d %v %v", cfg.WebhookMaxAttempts, cfg.WebhookTimeout, cfg.WebhookConcurrency, cfg.WebhookRetryBaseDelay, cfg.WebhookRetryMaxDelay)
	}
	for key, bad := range map[string]string{
		"WEBHOOK_MAX_ATTEMPTS":     "0",
		"WEBHOOK_TIMEOUT":          "0s",
		"WEBHOOK_CONCURRENCY":      "1000",
		"WEBHOOK_RETRY_BASE_DELAY": "100ms",
		"WEBHOOK_RETRY_MAX_DELAY":  "48h",
	} {
		if _, err := Load(env(map[string]string{"DATABASE_URL": devURL, key: bad})); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%s: err = %v", key, bad, err)
		}
	}
	if _, err := Load(env(map[string]string{"DATABASE_URL": devURL, "WEBHOOK_TIMEOUT": "2m"})); err == nil {
		t.Error("timeout above 60s accepted")
	}
	if _, err := Load(env(map[string]string{"DATABASE_URL": devURL, "WEBHOOK_RETRY_BASE_DELAY": "2h"})); err == nil {
		t.Error("base delay above max delay accepted")
	}
	_, err = Load(env(map[string]string{"DATABASE_URL": devURL, "PROVIDER_TIMEOUT": "1s", "WEBHOOK_TIMEOUT": "60s", "JOB_RESCUE_AFTER": "100s"}))
	if err == nil || !strings.Contains(err.Error(), "WEBHOOK_TIMEOUT + 50s") {
		t.Errorf("rescue window shorter than a webhook job: err = %v", err)
	}
}
