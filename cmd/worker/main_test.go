package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/config"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/metrics"
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
	logger := slog.New(slog.DiscardHandler)
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
	logger := slog.New(slog.DiscardHandler)
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

func sesConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) (string, bool) {
		v, ok := map[string]string{
			"DATABASE_URL": "postgres://u:p@localhost:5432/db", "MAIL_PROVIDER": "ses", "SES_ENABLED": "true",
			"AWS_REGION": "eu-central-1", "SES_CONFIGURATION_SET": "platform-events",
			"SES_EVENTS_QUEUE_URL": "https://sqs.eu-central-1.amazonaws.com/123456789012/ses-events",
			"SES_EVENTS_TOPIC_ARN": "arn:aws:sns:eu-central-1:123456789012:ses-events",
			"SES_ENDPOINT":         "http://127.0.0.1:4566", "SQS_ENDPOINT": "http://127.0.0.1:4567",
		}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNewProviderBuildsSESOnlyFromTheOptIn(t *testing.T) {
	p, err := newProvider(context.Background(), sesConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*delivery.SES); !ok {
		t.Fatalf("provider = %T, want SES", p)
	}
}

func TestEventPollerOnlyWithSES(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	cfg, err := config.Load(func(k string) (string, bool) {
		v, ok := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:5432/db"}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := newEventPoller(context.Background(), cfg, nil, logger); p != nil || err != nil {
		t.Fatalf("Mailpit: poller %v, err %v; want none", p, err)
	}
	rec := events.NewRecorder(nil, logger) // never used: nothing is received
	if p, err := newEventPoller(context.Background(), sesConfig(t), rec, logger); p == nil || err != nil {
		t.Fatalf("SES: poller %v, err %v; want one", p, err)
	}
}

func TestServeMetrics(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	reg := metrics.NewRegistry()
	stop, err := serveMetrics(context.Background(), "", reg, logger)
	if err != nil || stop == nil {
		t.Fatalf("off: %v", err)
	}
	stop(time.Second)

	// A busy port fails the start.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if _, err := serveMetrics(context.Background(), busy.Addr().String(), reg, logger); err == nil {
		t.Error("busy METRICS_ADDR accepted")
	}

	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := probe.Addr().String()
	_ = probe.Close()
	stop, err = serveMetrics(context.Background(), addr, reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "go_goroutines") {
		t.Errorf("GET /metrics = %d", resp.StatusCode)
	}
	stop(time.Second)
	stop(time.Second) // a second stop does nothing
	if resp, err := http.Get("http://" + addr + "/metrics"); err == nil {
		_ = resp.Body.Close()
		t.Error("metrics listener still serving after stop")
	}
}
