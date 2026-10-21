package contacts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/store"
)

// Unsubscribe marks the contact holding token unsubscribed and returns its
// id. Unknown, malformed and foreign tokens are all ErrNotFound; a repeat
// changes nothing. The token is never logged or wrapped into errors.
func (s *Store) Unsubscribe(ctx context.Context, projectID uuid.UUID, token string) (uuid.UUID, error) {
	if !validToken(token) {
		return uuid.Nil, ErrNotFound
	}
	q := store.New(s.pool)
	params := store.UnsubscribeByTokenParams{ProjectID: projectID, UnsubscribeToken: token}
	id, err := q.UnsubscribeByToken(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		// Unknown here, or already unsubscribed.
		id, err = q.ContactIDByToken(ctx, store.ContactIDByTokenParams(params))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("unsubscribe: %w", err)
	}
	return id, nil
}
