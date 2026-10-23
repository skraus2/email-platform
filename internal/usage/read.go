package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

// MaxDays is the longest range one read returns.
const MaxDays = 93

// Counts is the usage of one day (or a sum of days).
type Counts struct {
	Accepted, Sent, Failed, Canceled, Delivered, Bounced, Complained int64
}

// Add returns the sum of c and o.
func (c Counts) Add(o Counts) Counts {
	return Counts{
		Accepted: c.Accepted + o.Accepted, Sent: c.Sent + o.Sent, Failed: c.Failed + o.Failed,
		Canceled: c.Canceled + o.Canceled, Delivered: c.Delivered + o.Delivered,
		Bounced: c.Bounced + o.Bounced, Complained: c.Complained + o.Complained,
	}
}

// Day is the usage of one UTC day.
type Day struct {
	Date time.Time // midnight UTC
	Counts
}

// Reader reads stored usage.
type Reader struct{ pool *pgxpool.Pool }

// NewReader builds a Reader.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

// Days returns one entry per UTC day from from through to (both midnight
// UTC, at most MaxDays apart) for the project, zeros where nothing is
// stored.
func (r *Reader) Days(ctx context.Context, projectID uuid.UUID, from, to time.Time) ([]Day, error) {
	if to.Before(from) || to.Sub(from) >= MaxDays*day {
		return nil, fmt.Errorf("usage range %s..%s: invalid", from.Format(time.DateOnly), to.Format(time.DateOnly))
	}
	rows, err := store.New(r.pool).ListUsageDaily(ctx, store.ListUsageDailyParams{
		ProjectID: projectID,
		FromDay:   pgtype.Date{Time: from, Valid: true},
		ToDay:     pgtype.Date{Time: to, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list usage: %w", err)
	}
	stored := make(map[string]Counts, len(rows))
	for _, row := range rows {
		stored[row.Day.Time.Format(time.DateOnly)] = Counts{
			Accepted: row.Accepted, Sent: row.Sent, Failed: row.Failed, Canceled: row.Canceled,
			Delivered: row.Delivered, Bounced: row.Bounced, Complained: row.Complained,
		}
	}
	out := make([]Day, 0, int(to.Sub(from)/day)+1)
	for d := from; !d.After(to); d = d.Add(day) {
		out = append(out, Day{Date: d, Counts: stored[d.Format(time.DateOnly)]})
	}
	return out, nil
}
