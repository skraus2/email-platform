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
