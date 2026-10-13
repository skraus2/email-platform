package emails

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
)

// Limits of the operator email listing.
const (
	DefaultAdminListLimit = 50
	MaxAdminListLimit     = 500
)

var statuses = []string{"queued", "sending", "captured", "submitted", "failed"}

// AdminEmail is an email as operators see it: with the internal failure
// detail, but with the recipient masked.
type AdminEmail struct {
	ID         uuid.UUID
	Status     string
	ErrorCode  *string
	Detail     *string // sanitized failure class and SMTP reply code
	Attempts   int
	Recipient  string // masked
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

// ListEmails returns the newest emails of a project for failure inspection,
// optionally only those with status. limit 0 means the default.
func ListEmails(ctx context.Context, q *store.Queries, projectID uuid.UUID, status string, limit int) ([]AdminEmail, error) {
	if status != "" && !slices.Contains(statuses, status) {
		return nil, fmt.Errorf("status must be one of %v", statuses)
	}
	if limit == 0 {
		limit = DefaultAdminListLimit
	}
	if limit < 1 || limit > MaxAdminListLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxAdminListLimit)
	}
	var filter *string
	if status != "" {
		filter = &status
	}
	rows, err := q.ListEmails(ctx, store.ListEmailsParams{ProjectID: projectID, Status: filter, Limit: int32(limit)}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("list emails: %w", err)
	}
	out := make([]AdminEmail, 0, len(rows))
	for _, r := range rows {
		out = append(out, AdminEmail{
			ID: r.ID, Status: r.Status, ErrorCode: r.ErrorCode, Detail: r.LastError, Attempts: int(r.Attempts),
			Recipient: logging.MaskEmail(r.Recipient), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt,
		})
	}
	return out, nil
}
