package database

import (
	"context"
	"errors"
	"testing"
)

func TestDeleteInBatches(t *testing.T) {
	// Full batches repeat; a short batch ends the loop.
	sizes := []int64{BatchSize, BatchSize, 7, 99}
	calls := 0
	n, err := DeleteInBatches(context.Background(), func(context.Context) (int64, error) {
		calls++
		return sizes[calls-1], nil
	})
	if err != nil || n != 2*BatchSize+7 || calls != 3 {
		t.Fatalf("n = %d, calls = %d, err = %v", n, calls, err)
	}

	// An error stops the loop and keeps the count so far.
	boom := errors.New("boom")
	calls = 0
	n, err = DeleteInBatches(context.Background(), func(context.Context) (int64, error) {
		calls++
		if calls == 2 {
			return 0, boom
		}
		return BatchSize, nil
	})
	if !errors.Is(err, boom) || n != BatchSize || calls != 2 {
		t.Fatalf("n = %d, calls = %d, err = %v", n, calls, err)
	}

	// Cancellation stops between batches.
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	n, err = DeleteInBatches(ctx, func(context.Context) (int64, error) {
		calls++
		cancel()
		return BatchSize, nil
	})
	if !errors.Is(err, context.Canceled) || n != BatchSize || calls != 1 {
		t.Fatalf("n = %d, calls = %d, err = %v", n, calls, err)
	}
}
