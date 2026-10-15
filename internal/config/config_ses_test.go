package config

import (
	"strings"
	"testing"
)

// sesEnv is a complete, valid SES opt-in on top of the development defaults.
func sesEnv(extra map[string]string) map[string]string {
	m := map[string]string{
		"DATABASE_URL":          devURL,
		"MAIL_PROVIDER":         "ses",
		"SES_ENABLED":           "true",
		"AWS_REGION":            "eu-central-1",
		"SES_CONFIGURATION_SET": "platform-events",
		"SES_EVENTS_QUEUE_URL":  "https://sqs.eu-central-1.amazonaws.com/123456789012/ses-events",
		"SES_EVENTS_TOPIC_ARN":  "arn:aws:sns:eu-central-1:123456789012:ses-events",

		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS": "true",
	}
	for k, v := range extra {
		if v == "" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func TestLoadSESOptIn(t *testing.T) {
	cfg, err := Load(env(sesEnv(nil)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := SES{
		Region: "eu-central-1", ConfigurationSet: "platform-events",
		EventsQueueURL: "https://sqs.eu-central-1.amazonaws.com/123456789012/ses-events",
		EventsTopicARN: "arn:aws:sns:eu-central-1:123456789012:ses-events",
	}
	if cfg.MailProvider != "ses" || cfg.SES != want {
		t.Fatalf("config = %q %+v, want %+v", cfg.MailProvider, cfg.SES, want)
	}
	// SES identities are account-wide: the platform must check the
	// project's own identities.
	if !cfg.RequireVerifiedSender {
		t.Fatal("SES mode must require verified senders")
	}

	stubs, err := Load(env(sesEnv(map[string]string{
		"SES_ENDPOINT": "http://127.0.0.1:4566", "SQS_ENDPOINT": "http://localhost:4567",
		"REQUIRE_VERIFIED_SENDER": "true",
	})))
	if err != nil {
		t.Fatalf("Load with stub endpoints: %v", err)
	}
	if stubs.SES.Endpoint != "http://127.0.0.1:4566" || stubs.SES.SQSEndpoint != "http://localhost:4567" {
		t.Fatalf("endpoints = %+v", stubs.SES)
	}
}

func TestLoadRejectsIncompleteSESOptIn(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"no opt-in flag", map[string]string{"SES_ENABLED": ""}, "SES_ENABLED"},
		{"opt-in false", map[string]string{"SES_ENABLED": "false"}, "SES_ENABLED"},
		{"opt-in typo", map[string]string{"SES_ENABLED": "yes"}, "SES_ENABLED"},
		{"no region", map[string]string{"AWS_REGION": ""}, "AWS_REGION"},
		{"bad region", map[string]string{"AWS_REGION": "eu-central"}, "AWS_REGION"},
		{"no configuration set", map[string]string{"SES_CONFIGURATION_SET": ""}, "SES_CONFIGURATION_SET"},
		{"bad configuration set", map[string]string{"SES_CONFIGURATION_SET": "has space"}, "SES_CONFIGURATION_SET"},
		{"long configuration set", map[string]string{"SES_CONFIGURATION_SET": strings.Repeat("a", 65)}, "SES_CONFIGURATION_SET"},
		{"no queue", map[string]string{"SES_EVENTS_QUEUE_URL": ""}, "SES_EVENTS_QUEUE_URL"},
		{"queue not sqs", map[string]string{"SES_EVENTS_QUEUE_URL": "https://example.test/123456789012/q"}, "SES_EVENTS_QUEUE_URL"},
		{"queue over http", map[string]string{"SES_EVENTS_QUEUE_URL": "http://sqs.eu-central-1.amazonaws.com/123456789012/ses-events"}, "SES_EVENTS_QUEUE_URL"},
		{"queue in other region", map[string]string{"SES_EVENTS_QUEUE_URL": "https://sqs.us-east-1.amazonaws.com/123456789012/ses-events"}, "SES_EVENTS_QUEUE_URL"},
		{"no topic", map[string]string{"SES_EVENTS_TOPIC_ARN": ""}, "SES_EVENTS_TOPIC_ARN"},
		{"topic not sns", map[string]string{"SES_EVENTS_TOPIC_ARN": "arn:aws:sqs:eu-central-1:123456789012:ses-events"}, "SES_EVENTS_TOPIC_ARN"},
		{"topic in other region", map[string]string{"SES_EVENTS_TOPIC_ARN": "arn:aws:sns:us-east-1:123456789012:ses-events"}, "SES_EVENTS_TOPIC_ARN"},
		{"verification off", map[string]string{"REQUIRE_VERIFIED_SENDER": "false"}, "REQUIRE_VERIFIED_SENDER"},
		{"remote ses endpoint", map[string]string{"SES_ENDPOINT": "https://email.eu-central-1.amazonaws.com", "SQS_ENDPOINT": "http://127.0.0.1:4567"}, "SES_ENDPOINT"},
		{"private ses endpoint", map[string]string{"SES_ENDPOINT": "http://10.0.0.5:4566", "SQS_ENDPOINT": "http://127.0.0.1:4567"}, "SES_ENDPOINT"},
		{"remote sqs endpoint", map[string]string{"SES_ENDPOINT": "http://127.0.0.1:4566", "SQS_ENDPOINT": "https://sqs.eu-central-1.amazonaws.com"}, "SQS_ENDPOINT"},
		{"only the sqs stub", map[string]string{"SQS_ENDPOINT": "http://127.0.0.1:4567"}, "SES_ENDPOINT"},
		{"only the ses stub", map[string]string{"SES_ENDPOINT": "http://127.0.0.1:4566"}, "SQS_ENDPOINT"},
		{"stub endpoint in production", map[string]string{
			"APP_ENV": "production", "DATABASE_URL": "postgres://ep:s3cret@db.internal:5432/ep?sslmode=require",
			"SES_ENDPOINT": "http://127.0.0.1:4566",
		}, "SES_ENDPOINT"},
		{"sqs stub endpoint in production", map[string]string{
			"APP_ENV": "production", "DATABASE_URL": "postgres://ep:s3cret@db.internal:5432/ep?sslmode=require",
			"SQS_ENDPOINT": "http://127.0.0.1:4567",
		}, "SQS_ENDPOINT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(sesEnv(tt.extra)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want mention of %s", err, tt.want)
			}
		})
	}
}

func TestLoadSESInProductionWithoutStubs(t *testing.T) {
	prod := map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://ep:s3cret@db.internal:5432/ep?sslmode=require",
	}
	// The SDK would otherwise honour AWS_ENDPOINT_URL* and profile endpoint
	// overrides for every client it builds.
	for _, v := range []string{"", "false", "TRUE"} {
		prod["AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"] = v
		m := sesEnv(prod)
		if v == "" {
			delete(m, "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS")
		}
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS") {
			t.Fatalf("%q: err = %v, want refusal naming AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", v, err)
		}
	}
	prod["AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"] = "true"
	cfg, err := Load(env(sesEnv(prod)))
	if err != nil || cfg.SES.Endpoint != "" || cfg.SES.SQSEndpoint != "" {
		t.Fatalf("production SES config = %+v, %v", cfg.SES, err)
	}
}

func TestLoadRejectsSESSettingsWithoutOptIn(t *testing.T) {
	// Any SES setting without the opt-in is a mistake (a copied production
	// file, a half-finished switch); fail loudly instead of ignoring it.
	for _, key := range []string{
		"SES_ENABLED", "SES_CONFIGURATION_SET", "SES_EVENTS_QUEUE_URL", "SES_EVENTS_TOPIC_ARN", "SES_ENDPOINT", "SQS_ENDPOINT",
	} {
		t.Run(key, func(t *testing.T) {
			m := sesEnv(nil)
			delete(m, "MAIL_PROVIDER") // Mailpit default
			for _, k := range []string{"SES_ENABLED", "SES_CONFIGURATION_SET", "SES_EVENTS_QUEUE_URL", "SES_EVENTS_TOPIC_ARN"} {
				if k != key {
					delete(m, k)
				}
			}
			if key == "SES_ENDPOINT" || key == "SQS_ENDPOINT" {
				m[key] = "http://127.0.0.1:4566"
			}
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v, want refusal naming %s", err, key)
			}
		})
	}
}

func TestLoadMailpitIgnoresAmbientAWSRegion(t *testing.T) {
	// AWS_REGION is a common shell variable; it alone opts into nothing.
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL, "AWS_REGION": "eu-central-1", "SES_ENABLED": "false"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MailProvider != "mailpit" || cfg.SES != (SES{}) || cfg.RequireVerifiedSender {
		t.Fatalf("config = %q %+v verified=%v", cfg.MailProvider, cfg.SES, cfg.RequireVerifiedSender)
	}
}

func TestLoadProviderDailyLimit(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": devURL}))
	if err != nil || cfg.ProviderDailyLimit != 0 {
		t.Fatalf("default daily limit = %d, %v; want 0 (off)", cfg.ProviderDailyLimit, err)
	}
	cfg, err = Load(env(sesEnv(map[string]string{"PROVIDER_DAILY_LIMIT": "200"})))
	if err != nil || cfg.ProviderDailyLimit != 200 {
		t.Fatalf("daily limit = %d, %v; want 200", cfg.ProviderDailyLimit, err)
	}
	for _, bad := range []string{"-1", "many", "1.5", "100000001"} {
		if _, err := Load(env(map[string]string{"DATABASE_URL": devURL, "PROVIDER_DAILY_LIMIT": bad})); err == nil || !strings.Contains(err.Error(), "PROVIDER_DAILY_LIMIT") {
			t.Errorf("PROVIDER_DAILY_LIMIT=%s: err = %v", bad, err)
		}
	}
}
