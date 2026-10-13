package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/store"
)

// Errors returned by the admin operations.
var (
	ErrProjectNotFound = errors.New("project not found")
	ErrKeyNotFound     = errors.New("api key not found or already revoked")
)

const sqlstateForeignKeyViolation = "23503"

// CreateProject creates an active project.
func CreateProject(ctx context.Context, q *store.Queries, name string) (store.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return store.Project{}, errors.New("project name must be 1-200 characters")
	}
	p, err := q.CreateProject(ctx, name)
	if err != nil {
		return store.Project{}, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

// SetProjectStatus enables or disables a project. Keys of a disabled project
// stop authenticating immediately.
func SetProjectStatus(ctx context.Context, q *store.Queries, id uuid.UUID, active bool) error {
	status := "disabled"
	if active {
		status = "active"
	}
	n, err := q.SetProjectStatus(ctx, store.SetProjectStatusParams{ID: id, Status: status})
	if err != nil {
		return fmt.Errorf("set project status: %w", err)
	}
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// CreateKey issues a key for a project. The returned token is the only time
// the secret exists outside the caller; it is never stored. nil scopes means
// DefaultScopes.
func CreateKey(ctx context.Context, q *store.Queries, projectID uuid.UUID, scopes []string) (string, store.CreateAPIKeyRow, error) {
	if scopes == nil {
		scopes = DefaultScopes
	}
	if err := ValidateScopes(scopes); err != nil {
		return "", store.CreateAPIKeyRow{}, err
	}
	k, err := GenerateKey()
	if err != nil {
		return "", store.CreateAPIKeyRow{}, err
	}
	row, err := q.CreateAPIKey(ctx, store.CreateAPIKeyParams{
		ProjectID:  projectID,
		Prefix:     k.Prefix,
		SecretHash: k.SecretHash,
		Scopes:     scopes,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlstateForeignKeyViolation {
		return "", store.CreateAPIKeyRow{}, ErrProjectNotFound
	}
	if err != nil {
		return "", store.CreateAPIKeyRow{}, fmt.Errorf("create api key: %w", err)
	}
	return k.Token, row, nil
}

// RevokeKey revokes the key with the given prefix.
func RevokeKey(ctx context.Context, q *store.Queries, prefix string) error {
	n, err := q.RevokeAPIKey(ctx, prefix)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if n == 0 {
		return ErrKeyNotFound
	}
	return nil
}
