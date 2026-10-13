package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
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
