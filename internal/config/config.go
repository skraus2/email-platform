// Package config loads and validates process configuration from environment
// variables. Both cmd/api and cmd/worker use it; invalid configuration is a
// startup error, never a runtime surprise.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/skraus2/email-platform/internal/awsconfig"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/events/ses"
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
	Env      string
	HTTPAddr string
	// AdminHTTPAddr is the admin API listener ("" = off). In production it
	// must be a loopback IP literal.
	AdminHTTPAddr   string
	DatabaseURL     string
	LogLevel        slog.Level
	LogFormat       string // "json" or "text"
	ShutdownTimeout time.Duration

	// Mail dispatch (worker): the local Mailpit sink by default, Amazon SES
	// only with the complete opt-in (MAIL_PROVIDER=ses, SES_ENABLED=true and
	// the SES settings). No other setting can reach a real mail server.
	MailProvider    string
	SMTPAddr        string
	ProviderTimeout time.Duration
	BodyMaxAge      time.Duration
	// SES is set only when MailProvider is "ses".
	SES SES
	// ProviderDailyLimit caps hand-offs to the provider per rolling 24 hours
	// across all workers (0 = off), e.g. the SES sandbox quota.
	ProviderDailyLimit int

	// Dispatch retries. The API stamps SendMaxAttempts on each new job; the
	// worker spaces retries with the delays.
	SendMaxAttempts int
	RetryBaseDelay  time.Duration
	RetryMaxDelay   time.Duration

	// APIRateLimitPerMinute caps accepted sends per project and minute.
	APIRateLimitPerMinute int

	// Template caps: templates per project, versions per template, and the
	// bytes of template source and schemas a project stores.
	TemplateMaxPerProject   int
	TemplateMaxVersions     int
	TemplateMaxStorageBytes int
	// TemplateWritesPerMinute caps template creates/publishes per project
	// and minute (attempts, also refused ones).
	TemplateWritesPerMinute int

	// Admin sessions end after AdminSessionIdle without use or
	// AdminSessionMax after login.
	AdminSessionIdle time.Duration
	AdminSessionMax  time.Duration
	// ProviderRatePerSecond caps messages per second the worker process
	// hands to the provider.
	ProviderRatePerSecond float64
	// RequireVerifiedSender makes the API accept only from addresses that
	// match a verified sender identity of the project.
	RequireVerifiedSender bool

	// WebhookKeys encrypt and decrypt webhook endpoint secrets (nil when
	// WEBHOOK_KEKS is unset; every use then fails with webhooks.ErrNoKeys).
	WebhookKeys *webhooks.Keyring
	// WebhookTargets limits which webhook URLs and addresses may be called.
	WebhookTargets webhooks.TargetPolicy
	// Retention (worker cleanup): events and finished webhook deliveries are
	// deleted after these ages; EmailRetention 0 keeps emails forever.
	EventRetention           time.Duration
	WebhookDeliveryRetention time.Duration
	EmailRetention           time.Duration
	// WebhookMaxEndpointsPerProject caps a project's webhook endpoints,
	// active and disabled.
	WebhookMaxEndpointsPerProject int
	// Webhook delivery (worker): requests per delivery, one request's
	// timeout, parallel deliveries, and the backoff between attempts.
	WebhookMaxAttempts    int
	WebhookTimeout        time.Duration
	WebhookConcurrency    int
	WebhookRetryBaseDelay time.Duration
	WebhookRetryMaxDelay  time.Duration

	// JobRescueAfter: a dispatch job still running this long (its worker
	// died) is handed to another attempt. At least one attempt budget.
	JobRescueAfter time.Duration
}

// SES is the Amazon SES setup: the account region, the configuration set
// that publishes events to the SNS topic, the SQS queue subscribed to it,
// and loopback stub endpoints for local tests (never in production).
type SES struct {
	Region           string
	ConfigurationSet string
	EventsQueueURL   string
	EventsTopicARN   string
	Endpoint         string
	SQSEndpoint      string
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
	switch cfg.MailProvider {
	case "mailpit":
		// A stray SES setting (a copied production file, a half-done
		// switch) must fail loudly, not be ignored.
		for _, key := range sesSettings {
			if v := get(key, ""); v != "" && (key != "SES_ENABLED" || v != "false") {
				errs = append(errs, fmt.Errorf("%s is set, but SES is not enabled (needs MAIL_PROVIDER=ses and SES_ENABLED=true)", key))
			}
		}
	case "ses":
		cfg.SES, errs = loadSES(get, cfg.Env, errs)
	default:
		errs = append(errs, fmt.Errorf("MAIL_PROVIDER must be mailpit or ses; got %q", cfg.MailProvider))
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

	// SES identities are account-wide, so with SES the platform itself must
	// hold every project to its own verified identities.
	sesMode := cfg.MailProvider == "ses"
	nErrs := len(errs)
	cfg.RequireVerifiedSender, errs = boolSetting(get, "REQUIRE_VERIFIED_SENDER", strconv.FormatBool(sesMode), errs)
	if sesMode && !cfg.RequireVerifiedSender && len(errs) == nErrs {
		errs = append(errs, errors.New("REQUIRE_VERIFIED_SENDER must not be false with MAIL_PROVIDER=ses"))
	}
	cfg.ProviderDailyLimit, errs = intInRange(get, "PROVIDER_DAILY_LIMIT", "0", 0, maxDailyLimit, errs)

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

	cfg.EventRetention, errs = minDuration(get, "EVENT_RETENTION", "2160h", 360*time.Hour,
		"(longer than the 14-day SQS redelivery window)", errs)
	cfg.WebhookDeliveryRetention, errs = minDuration(get, "WEBHOOK_DELIVERY_RETENTION", "720h", 24*time.Hour, "", errs)
	if get("EMAIL_RETENTION", "") != "" {
		cfg.EmailRetention, errs = minDuration(get, "EMAIL_RETENTION", "", 720*time.Hour, "(or empty to keep emails)", errs)
	}
	cfg.WebhookMaxEndpointsPerProject, errs = intInRange(get, "WEBHOOK_MAX_ENDPOINTS_PER_PROJECT", "10", 1, 100, errs)
	cfg.WebhookMaxAttempts, errs = intInRange(get, "WEBHOOK_MAX_ATTEMPTS", "12", 1, 25, errs)
	cfg.WebhookConcurrency, errs = intInRange(get, "WEBHOOK_CONCURRENCY", "5", 1, 100, errs)
	cfg.WebhookTimeout, errs = positiveDuration(get, "WEBHOOK_TIMEOUT", "10s", errs)
	if cfg.WebhookTimeout > time.Minute {
		errs = append(errs, errors.New("WEBHOOK_TIMEOUT must be at most 60s"))
	}
	cfg.WebhookRetryBaseDelay, errs = positiveDuration(get, "WEBHOOK_RETRY_BASE_DELAY", "30s", errs)
	if cfg.WebhookRetryBaseDelay > 0 && cfg.WebhookRetryBaseDelay < time.Second {
		errs = append(errs, errors.New("WEBHOOK_RETRY_BASE_DELAY must be at least 1s"))
	}
	cfg.WebhookRetryMaxDelay, errs = positiveDuration(get, "WEBHOOK_RETRY_MAX_DELAY", "1h", errs)
	if cfg.WebhookRetryMaxDelay > maxRetryDelay {
		errs = append(errs, errors.New("WEBHOOK_RETRY_MAX_DELAY must be at most 24h"))
	}
	if cfg.WebhookRetryMaxDelay > 0 && cfg.WebhookRetryMaxDelay < cfg.WebhookRetryBaseDelay {
		errs = append(errs, errors.New("WEBHOOK_RETRY_MAX_DELAY must not be shorter than WEBHOOK_RETRY_BASE_DELAY"))
	}

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
	cfg.TemplateMaxPerProject, errs = intInRange(get, "TEMPLATE_MAX_PER_PROJECT", "200", 1, 10_000, errs)
	cfg.TemplateMaxVersions, errs = intInRange(get, "TEMPLATE_MAX_VERSIONS", "500", 1, 10_000, errs)
	cfg.TemplateMaxStorageBytes, errs = intInRange(get, "TEMPLATE_MAX_STORAGE_BYTES", "52428800", 1<<20, 10<<30, errs)
	cfg.TemplateWritesPerMinute, errs = intInRange(get, "TEMPLATE_WRITES_PER_MINUTE", "60", 1, 100_000, errs)
	if addr := get("ADMIN_HTTP_ADDR", "127.0.0.1:8081"); addr != "off" {
		if err := validAdminAddr(addr, cfg.Env == EnvProduction); err != nil {
			errs = append(errs, fmt.Errorf("ADMIN_HTTP_ADDR: %w", err))
		} else {
			cfg.AdminHTTPAddr = addr
		}
	}
	cfg.AdminSessionIdle, errs = positiveDuration(get, "ADMIN_SESSION_IDLE", "30m", errs)
	if cfg.AdminSessionIdle > 0 && (cfg.AdminSessionIdle < 5*time.Minute || cfg.AdminSessionIdle > 24*time.Hour) {
		errs = append(errs, errors.New("ADMIN_SESSION_IDLE must be between 5m and 24h"))
	}
	cfg.AdminSessionMax, errs = positiveDuration(get, "ADMIN_SESSION_MAX", "12h", errs)
	if cfg.AdminSessionMax > 0 && (cfg.AdminSessionMax < cfg.AdminSessionIdle || cfg.AdminSessionMax > 7*24*time.Hour) {
		errs = append(errs, errors.New("ADMIN_SESSION_MAX must be at least ADMIN_SESSION_IDLE and at most 168h"))
	}
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
	// A webhook job runs up to WEBHOOK_TIMEOUT plus 10s of database work and
	// a detached 10s result write; it must not be rescued while running.
	if minRescue := cfg.WebhookTimeout + 50*time.Second; cfg.JobRescueAfter > 0 && cfg.WebhookTimeout > 0 && cfg.JobRescueAfter < minRescue {
		errs = append(errs, fmt.Errorf("JOB_RESCUE_AFTER must be at least %v (WEBHOOK_TIMEOUT + 50s), or running webhook deliveries could be rescued and sent twice", minRescue))
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

// sesSettings are the settings that only exist with the SES opt-in.
var sesSettings = []string{
	"SES_ENABLED", "SES_CONFIGURATION_SET", "SES_EVENTS_QUEUE_URL", "SES_EVENTS_TOPIC_ARN", "SES_ENDPOINT", "SQS_ENDPOINT",
}

var (
	configurationSetPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	queueURLPattern         = regexp.MustCompile(`^https://sqs\.([a-z0-9-]+)\.amazonaws\.com/[0-9]{12}/[A-Za-z0-9_-]{1,80}$`)
)

// loadSES validates the SES opt-in. Every part is required, so a partial
// setup never sends.
func loadSES(get func(string, string) string, env string, errs []error) (SES, []error) {
	if get("SES_ENABLED", "") != "true" {
		errs = append(errs, errors.New("SES_ENABLED must be true to use MAIL_PROVIDER=ses (explicit opt-in)"))
	}
	c := SES{
		Region:           get("AWS_REGION", ""),
		ConfigurationSet: get("SES_CONFIGURATION_SET", ""),
		EventsQueueURL:   get("SES_EVENTS_QUEUE_URL", ""),
		EventsTopicARN:   get("SES_EVENTS_TOPIC_ARN", ""),
		Endpoint:         get("SES_ENDPOINT", ""),
		SQSEndpoint:      get("SQS_ENDPOINT", ""),
	}
	if !awsconfig.ValidRegion(c.Region) {
		errs = append(errs, errors.New("AWS_REGION must be an AWS region such as eu-central-1"))
	}
	if !configurationSetPattern.MatchString(c.ConfigurationSet) {
		errs = append(errs, errors.New("SES_CONFIGURATION_SET must be a configuration set name (letters, digits, - and _, at most 64)"))
	}
	if m := queueURLPattern.FindStringSubmatch(c.EventsQueueURL); m == nil || m[1] != c.Region {
		errs = append(errs, errors.New("SES_EVENTS_QUEUE_URL must be an https://sqs.<AWS_REGION>.amazonaws.com/<account>/<queue> URL"))
	}
	if r, ok := ses.TopicRegion(c.EventsTopicARN); !ok || r != c.Region {
		errs = append(errs, errors.New("SES_EVENTS_TOPIC_ARN must be an SNS topic ARN in AWS_REGION"))
	}
	for _, ep := range []struct{ key, v string }{{"SES_ENDPOINT", c.Endpoint}, {"SQS_ENDPOINT", c.SQSEndpoint}} {
		key, v := ep.key, ep.v
		switch {
		case v == "":
		case env == EnvProduction:
			errs = append(errs, fmt.Errorf("%s must not be set in production (stub endpoints are for local tests)", key))
		default:
			if err := awsconfig.ValidateEndpoint(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			}
		}
	}
	// Against AWS the SDK would honour endpoint overrides from
	// AWS_ENDPOINT_URL* and the shared config for every client it builds
	// (including the credential clients), bypassing the checks above.
	if c.Endpoint == "" && get(awsconfig.IgnoreEndpointsEnv, "") != "true" {
		errs = append(errs, fmt.Errorf("%s=true is required with SES against AWS (configured endpoint overrides could redirect signed requests)", awsconfig.IgnoreEndpointsEnv))
	}
	// Half a stub setup would send through real SES while believing it is
	// local.
	if env != EnvProduction && (c.Endpoint == "") != (c.SQSEndpoint == "") {
		errs = append(errs, errors.New("SES_ENDPOINT and SQS_ENDPOINT must be set together (local stubs) or both left unset (AWS)"))
	}
	return c, errs
}

// maxDailyLimit bounds PROVIDER_DAILY_LIMIT to a plausible daily volume.
const maxDailyLimit = 10_000_000

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

// minDuration parses a duration that must be at least min.
func minDuration(get func(string, string) string, key, def string, minimum time.Duration, why string, errs []error) (time.Duration, []error) {
	d, err := time.ParseDuration(get(key, def))
	switch {
	case err != nil:
		return 0, append(errs, fmt.Errorf("%s invalid: %w", key, err))
	case d < minimum:
		return 0, append(errs, errors.New(strings.TrimSpace(fmt.Sprintf("%s must be at least %v %s", key, minimum, why))))
	}
	return d, errs
}

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

// validAdminAddr accepts host:port. In production the host must be a
// loopback IP literal, so admin passwords and session tokens never cross a
// network in clear (no TLS on this listener).
func validAdminAddr(addr string, production bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("must be host:port or off")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("port must be 1-65535")
	}
	if !production {
		return nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return errors.New("in production the host must be a loopback IP address such as 127.0.0.1 or ::1")
	}
	return nil
}

// AdminBindIsLoopback reports whether the admin listener binds only to a
// loopback address (false for wildcards, hostnames and other IPs).
func (c Config) AdminBindIsLoopback() bool {
	host, _, err := net.SplitHostPort(c.AdminHTTPAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
