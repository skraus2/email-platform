package database

import "context"

// BatchSize is how many rows one retention delete removes; every batch is
// its own statement and therefore its own short transaction.
const BatchSize = 1000

// DeleteInBatches runs batch until it deletes fewer than BatchSize rows,
// fails, or ctx is done, and returns the rows deleted so far.
func DeleteInBatches(ctx context.Context, batch func(context.Context) (int64, error)) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := batch(ctx)
		total += n
		if err != nil {
			return total, err
		}
		if n < BatchSize {
			return total, nil
		}
	}
}
