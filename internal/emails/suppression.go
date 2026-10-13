package emails

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/store"
)

// ErrRecipientSuppressed means the project must not send to the recipient
// (earlier bounce, complaint, unsubscribe or operator decision).
var ErrRecipientSuppressed = errors.New("recipient is on the project's suppression list")

// SuppressionReasons lists why an address can be suppressed.
var SuppressionReasons = []string{"bounce", "complaint", "unsubscribe", "manual"}

// Suppression is a suppressed address of a project.
type Suppression struct {
	Recipient string
	Reason    string
	CreatedAt time.Time
}

// AddSuppression suppresses an address for a project. added is false if
// the address (in any letter case) was already suppressed.
func AddSuppression(ctx context.Context, q *store.Queries, projectID uuid.UUID, addr, reason string) (bool, error) {
	if err := ValidateAddress(addr); err != nil {
		return false, err
	}
	if !slices.Contains(SuppressionReasons, reason) {
		return false, fmt.Errorf("reason must be one of %v", SuppressionReasons)
	}
	n, err := q.AddSuppression(ctx, store.AddSuppressionParams{ProjectID: projectID, Recipient: addr, Reason: reason})
	if err != nil {
		return false, fmt.Errorf("add suppression: %w", err)
	}
	return n > 0, nil
}

// RemoveSuppression lifts a suppression. removed is false if the address
// was not suppressed.
func RemoveSuppression(ctx context.Context, q *store.Queries, projectID uuid.UUID, addr string) (bool, error) {
	n, err := q.RemoveSuppression(ctx, store.RemoveSuppressionParams{ProjectID: projectID, Recipient: addr})
	if err != nil {
		return false, fmt.Errorf("remove suppression: %w", err)
	}
	return n > 0, nil
}

// ListSuppressions returns the project's suppressed addresses, newest first.
func ListSuppressions(ctx context.Context, q *store.Queries, projectID uuid.UUID) ([]Suppression, error) {
	rows, err := q.ListSuppressions(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list suppressions: %w", err)
	}
	out := make([]Suppression, 0, len(rows))
	for _, r := range rows {
		out = append(out, Suppression{Recipient: r.Recipient, Reason: r.Reason, CreatedAt: r.CreatedAt})
	}
	return out, nil
}
