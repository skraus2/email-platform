// Package retention deletes data past its retention period: email events,
// finished webhook deliveries, expired admin sessions, contact events and
// finished runs of automations that allow re-entry and, only when
// configured, finished emails. Nothing a pending webhook delivery or an
// unfinished email still needs is deleted.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/store"
)

// Minimum retention periods. Events must outlive the 14-day SQS redelivery
// window, or a redelivered provider event could be stored twice.
const (
	MinEventRetention    = 15 * 24 * time.Hour
	MinDeliveryRetention = 24 * time.Hour
	MinEmailRetention    = 30 * 24 * time.Hour
	// MinContactEventRetention keeps idempotency keys of contact events at
	// least as long as those of emails.
	MinContactEventRetention = 24 * time.Hour
)

// Pruner runs the retention deletes. EmailRetention 0 keeps emails.
type Pruner struct {
	Pool              *pgxpool.Pool
	Logger            *slog.Logger
	EventRetention    time.Duration
	DeliveryRetention time.Duration
	EmailRetention    time.Duration
	// ContactEventRetention also applies to finished runs of automations
	// that allow re-entry (zero: MinContactEventRetention is not met).
	ContactEventRetention time.Duration
}

func (p Pruner) validate() error {
	switch {
	case p.EventRetention < MinEventRetention:
		return fmt.Errorf("event retention %v is below the minimum %v", p.EventRetention, MinEventRetention)
	case p.DeliveryRetention < MinDeliveryRetention:
		return fmt.Errorf("webhook delivery retention %v is below the minimum %v", p.DeliveryRetention, MinDeliveryRetention)
	case p.ContactEventRetention < MinContactEventRetention:
		return fmt.Errorf("contact event retention %v is below the minimum %v", p.ContactEventRetention, MinContactEventRetention)
	case p.EmailRetention != 0 && p.EmailRetention < MinEmailRetention:
		return fmt.Errorf("email retention %v is below the minimum %v", p.EmailRetention, MinEmailRetention)
	}
	return nil
}

// Run deletes one pass of expired rows in batches. A failing kind does not
// stop the others; their errors are joined.
func (p Pruner) Run(ctx context.Context) error {
	if err := p.validate(); err != nil {
		return fmt.Errorf("retention: %w", err)
	}
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
		return q.DeleteOldDeliveries(ctx, store.DeleteOldDeliveriesParams{Before: now.Add(-p.DeliveryRetention), BatchSize: database.BatchSize})
	})
	events := prune("events", func(ctx context.Context) (int64, error) {
		return q.DeleteOldEvents(ctx, store.DeleteOldEventsParams{Before: now.Add(-p.EventRetention), BatchSize: database.BatchSize})
	})
	var emails int64
	if p.EmailRetention > 0 {
		// The batch size counts locked emails (what the loop needs); emails
		// counts the ones actually deleted.
		prune("emails", func(ctx context.Context) (int64, error) {
			var locked int
			var deleted int64
			err := pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
				tq := store.New(tx)
				ids, err := tq.LockOldEmails(ctx, store.LockOldEmailsParams{Before: now.Add(-p.EmailRetention), BatchSize: database.BatchSize})
				if err != nil || len(ids) == 0 {
					return err
				}
				locked = len(ids)
				deleted, err = tq.DeleteLockedEmails(ctx, ids)
				return err
			})
			if err != nil {
				return 0, err
			}
			emails += deleted
			return int64(locked), nil
		})
	}
	sessions := prune("admin sessions", func(ctx context.Context) (int64, error) {
		return q.DeleteExpiredAdminSessions(ctx, database.BatchSize)
	})
	contactEvents := prune("contact events", func(ctx context.Context) (int64, error) {
		return q.DeleteOldContactEvents(ctx, store.DeleteOldContactEventsParams{Before: now.Add(-p.ContactEventRetention), BatchSize: database.BatchSize})
	})
	runs := prune("automation runs", func(ctx context.Context) (int64, error) {
		return q.DeleteOldReentryRuns(ctx, store.DeleteOldReentryRunsParams{Before: now.Add(-p.ContactEventRetention), BatchSize: database.BatchSize})
	})

	if deliveries > 0 || events > 0 || emails > 0 || sessions > 0 || contactEvents > 0 || runs > 0 {
		p.Logger.InfoContext(ctx, "retention",
			slog.Int64("events_deleted", events), slog.Int64("deliveries_deleted", deliveries),
			slog.Int64("emails_deleted", emails), slog.Int64("sessions_deleted", sessions),
			slog.Int64("contact_events_deleted", contactEvents), slog.Int64("automation_runs_deleted", runs))
	}
	return errors.Join(errs...)
}
