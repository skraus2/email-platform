package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/delivery"
)

func TestNextDelayDoublesAndCaps(t *testing.T) {
	if got := nextDelay(time.Second); got != 2*time.Second {
		t.Errorf("nextDelay(1s) = %v, want 2s", got)
	}
	if got := nextDelay(20 * time.Second); got != maxReadyDelay {
		t.Errorf("nextDelay(20s) = %v, want cap %v", got, maxReadyDelay)
	}
}

func TestWaitReadyRetriesUntilSuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := 0
	ok := waitReady(context.Background(), logger, func(context.Context) error {
		calls++
		if calls < 2 {
			return errors.New("db down")
		}
		return nil
	})
	if !ok || calls != 2 {
		t.Fatalf("ok=%v calls=%d, want true after 2 calls", ok, calls)
	}
}

func TestWaitReadyStopsOnCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok := waitReady(ctx, logger, func(context.Context) error { return errors.New("db down") })
	if ok {
		t.Fatal("waitReady returned true for cancelled context with failing check")
	}
}

func TestNewProviderIsMailpitWithoutSESOptIn(t *testing.T) {
	cfg, err := config.Load(func(k string) (string, bool) {
		v, ok := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:5432/db"}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProvider(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*delivery.Mailpit); !ok {
		t.Fatalf("provider = %T, want the Mailpit sink", p)
	}
}

func TestNewProviderBuildsSESOnlyFromTheOptIn(t *testing.T) {
	cfg, err := config.Load(func(k string) (string, bool) {
		v, ok := map[string]string{
			"DATABASE_URL": "postgres://u:p@localhost:5432/db", "MAIL_PROVIDER": "ses", "SES_ENABLED": "true",
			"AWS_REGION": "eu-central-1", "SES_CONFIGURATION_SET": "platform-events",
			"SES_EVENTS_QUEUE_URL": "https://sqs.eu-central-1.amazonaws.com/123456789012/ses-events",
			"SES_EVENTS_TOPIC_ARN": "arn:aws:sns:eu-central-1:123456789012:ses-events",
			"SES_ENDPOINT":         "http://127.0.0.1:4566",
		}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProvider(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*delivery.SES); !ok {
		t.Fatalf("provider = %T, want SES", p)
	}
}
