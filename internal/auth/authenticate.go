package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/store"
)

// ErrUnauthorized covers every reason a key is not accepted: malformed,
// unknown, wrong secret, revoked, or disabled project. Callers must not tell
// them apart in responses.
var ErrUnauthorized = errors.New("unauthorized")

// Principal is the authenticated caller of a /v1 request.
type Principal struct {
	ProjectID uuid.UUID
	KeyID     uuid.UUID
	Scopes    []string
}

// Has reports whether the principal holds scope.
func (p Principal) Has(scope string) bool { return slices.Contains(p.Scopes, scope) }

// KeyLookup returns the active (not revoked, project active) key for a
// prefix, or pgx.ErrNoRows. store.Queries.GetActiveKeyByPrefix satisfies it.
type KeyLookup func(ctx context.Context, prefix string) (store.GetActiveKeyByPrefixRow, error)

// Authenticate verifies a bearer token. Lookup failures other than "no such
// key" are returned as-is (not ErrUnauthorized) so an outage is not reported
// as bad credentials.
func Authenticate(ctx context.Context, lookup KeyLookup, token string) (Principal, error) {
	prefix, secret, err := ParseToken(token)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	row, err := lookup(ctx, prefix)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, fmt.Errorf("look up api key: %w", err)
	}
	// One comparison on every path: an unknown prefix is compared against a
	// dummy hash, so timing does not reveal which prefixes exist.
	hash := DummyHash()
	if found {
		hash = row.SecretHash
	}
	if !Matches(secret, hash) || !found {
		return Principal{}, ErrUnauthorized
	}
	return Principal{ProjectID: row.ProjectID, KeyID: row.ID, Scopes: row.Scopes}, nil
}
