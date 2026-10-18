// Package retention deletes data past its retention period: email events,
// finished webhook deliveries, expired admin sessions and, only when
// configured, finished emails. Nothing a pending webhook delivery or an
// unfinished email still needs is deleted.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/store"
)

// sessionIdleBound is the longest valid admin session idle timeout. Sessions
// unused for longer are deleted whatever the API process is configured
// with; the API refuses shorter idle sessions itself.
const sessionIdleBound = 24 * time.Hour

// Pruner runs the retention deletes. EmailRetention 0 keeps emails.
type Pruner struct {
	Pool              *pgxpool.Pool
	Logger            *slog.Logger
	EventRetention    time.Duration
	DeliveryRetention time.Duration
	EmailRetention    time.Duration
}

// Run deletes one pass of expired rows in batches. A failing kind does not
// stop the others; their errors are joined.
func (p Pruner) Run(ctx context.Context) error {
	q := store.New(p.Pool)
	now := time.Now()
	var errs []error
	prune := func(kind string, batch func(context.Context) (int64, error)) int64 {
		n, err := database.DeleteInBatches(ctx, batch)
		if err != nil {
			errs = append(errs, fmt.Errorf("retention %s: %w", kind, err))
		}
		return n
	}

	deliveries := prune("webhook deliveries", func(ctx context.Context) (int64, error) {
		return q.DeleteOldDeliveries(ctx, now.Add(-p.DeliveryRetention))
	})
	events := prune("events", func(ctx context.Context) (int64, error) {
		return q.DeleteOldEvents(ctx, now.Add(-p.EventRetention))
	})
	var emails int64
	if p.EmailRetention > 0 {
		emails = prune("emails", func(ctx context.Context) (int64, error) {
			return q.DeleteOldEmails(ctx, now.Add(-p.EmailRetention))
		})
	}
	sessions := prune("admin sessions", func(ctx context.Context) (int64, error) {
		return q.DeleteExpiredAdminSessions(ctx, now.Add(-sessionIdleBound))
	})

	if deliveries > 0 || events > 0 || emails > 0 || sessions > 0 {
		p.Logger.InfoContext(ctx, "retention",
			slog.Int64("events_deleted", events), slog.Int64("deliveries_deleted", deliveries),
			slog.Int64("emails_deleted", emails), slog.Int64("sessions_deleted", sessions))
	}
	return errors.Join(errs...)
}
