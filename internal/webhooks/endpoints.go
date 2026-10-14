package webhooks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

// EventTypes are the webhook event types an endpoint can subscribe to.
var EventTypes = []string{
	"email.captured", "email.submitted", "email.failed",
	"email.delivered", "email.bounced", "email.complained",
}

// Endpoint is a webhook endpoint without its secret.
type Endpoint struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	URL        string
	EventTypes []string
	Status     string // active or disabled
	KeyID      string // key the secret is encrypted with
	CreatedAt  time.Time
}

func endpointFromRow(r store.WebhookEndpoint) Endpoint {
	return Endpoint{
		ID: r.ID, ProjectID: r.ProjectID, URL: r.Url, EventTypes: r.EventTypes,
		Status: r.Status, KeyID: r.SecretKeyID, CreatedAt: r.CreatedAt,
	}
}

// ErrNoKeys means no key-encryption keys are configured (WEBHOOK_KEKS).
var ErrNoKeys = errors.New("WEBHOOK_KEKS is not set; webhook secrets cannot be encrypted or decrypted")

// ErrNotFound means the project has no webhook endpoint with that id.
var ErrNotFound = errors.New("webhook endpoint not found")

// ErrUnknownProject means the project does not exist.
var ErrUnknownProject = errors.New("project not found")

func normalizeTypes(types []string) ([]string, error) {
	if len(types) == 0 {
		return slices.Clone(EventTypes), nil
	}
	var out []string
	for _, t := range types {
		if !slices.Contains(EventTypes, t) {
			return nil, fmt.Errorf("unknown event type %q; known: %v", t, EventTypes)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// CreateEndpoint registers an endpoint (all event types when types is empty)
// and returns it with its new secret. The secret is stored only encrypted and
// cannot be read back: show it to the operator once.
func CreateEndpoint(ctx context.Context, q *store.Queries, keys *Keyring, policy TargetPolicy, projectID uuid.UUID, rawURL string, types []string) (Endpoint, string, error) {
	if keys == nil {
		return Endpoint{}, "", ErrNoKeys
	}
	u, err := policy.ValidateURL(rawURL)
	if err != nil {
		return Endpoint{}, "", err
	}
	types, err = normalizeTypes(types)
	if err != nil {
		return Endpoint{}, "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Endpoint{}, "", fmt.Errorf("generate endpoint id: %w", err)
	}
	secret := NewSecret()
	ct, kid, err := keys.Encrypt([]byte(secret), id, projectID)
	if err != nil {
		return Endpoint{}, "", err
	}
	row, err := q.InsertWebhookEndpoint(ctx, store.InsertWebhookEndpointParams{
		ID: id, ProjectID: projectID, Url: u.String(), EventTypes: types, SecretCiphertext: ct, SecretKeyID: kid,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return Endpoint{}, "", ErrUnknownProject
	}
	if err != nil {
		return Endpoint{}, "", fmt.Errorf("insert webhook endpoint: %w", err)
	}
	return endpointFromRow(row), secret, nil
}

// ListEndpoints returns the project's endpoints, oldest first.
func ListEndpoints(ctx context.Context, q *store.Queries, projectID uuid.UUID) ([]Endpoint, error) {
	rows, err := q.ListWebhookEndpoints(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list webhook endpoints: %w", err)
	}
	out := make([]Endpoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, endpointFromRow(r))
	}
	return out, nil
}

// SetEndpointActive enables or disables an endpoint. changed is false if it
// already had that status; ErrNotFound if the project has no such endpoint.
func SetEndpointActive(ctx context.Context, pool *pgxpool.Pool, projectID, id uuid.UUID, active bool) (bool, error) {
	status := "disabled"
	if active {
		status = "active"
	}
	var n int64
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.LockWebhookEndpoint(ctx, store.LockWebhookEndpointParams{ID: id, ProjectID: projectID}); err != nil {
			return err
		}
		var err error
		n, err = q.SetWebhookEndpointStatus(ctx, store.SetWebhookEndpointStatusParams{ID: id, ProjectID: projectID, Status: status})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("set webhook endpoint status: %w", err)
	}
	return n > 0, nil
}

// RotateSecret replaces an endpoint's secret and returns the new one. The old
// secret stops working immediately.
func RotateSecret(ctx context.Context, pool *pgxpool.Pool, keys *Keyring, projectID, id uuid.UUID) (string, error) {
	if keys == nil {
		return "", ErrNoKeys
	}
	secret := NewSecret()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.LockWebhookEndpoint(ctx, store.LockWebhookEndpointParams{ID: id, ProjectID: projectID}); err != nil {
			return err
		}
		ct, kid, err := keys.Encrypt([]byte(secret), id, projectID)
		if err != nil {
			return err
		}
		return q.UpdateWebhookSecret(ctx, store.UpdateWebhookSecretParams{ID: id, ProjectID: projectID, SecretCiphertext: ct, SecretKeyID: kid})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("rotate webhook secret: %w", err)
	}
	return secret, nil
}

// Reencrypt moves every secret stored under an older key to the active key,
// in one transaction (all or nothing), without changing the secrets. Run it
// before removing an old key from WEBHOOK_KEKS. It returns how many secrets
// were re-encrypted.
func Reencrypt(ctx context.Context, pool *pgxpool.Pool, keys *Keyring) (int, error) {
	if keys == nil {
		return 0, ErrNoKeys
	}
	var n int
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		n = 0
		q := store.New(tx)
		rows, err := q.LockEndpointsNotUnderKey(ctx, keys.ActiveKeyID())
		if err != nil {
			return err
		}
		for _, r := range rows {
			secret, err := keys.Decrypt(r.SecretCiphertext, r.SecretKeyID, r.ID, r.ProjectID)
			if err != nil {
				return fmt.Errorf("endpoint %s (key %q): %w", r.ID, r.SecretKeyID, err)
			}
			ct, kid, err := keys.Encrypt(secret, r.ID, r.ProjectID)
			if err != nil {
				return err
			}
			if err := q.UpdateWebhookSecret(ctx, store.UpdateWebhookSecretParams{
				ID: r.ID, ProjectID: r.ProjectID, SecretCiphertext: ct, SecretKeyID: kid,
			}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("re-encrypt webhook secrets: %w", err)
	}
	return n, nil
}

// secretOf decrypts an endpoint's secret.
func secretOf(keys *Keyring, r store.WebhookEndpoint) (string, error) {
	if keys == nil {
		return "", ErrNoKeys
	}
	b, err := keys.Decrypt(r.SecretCiphertext, r.SecretKeyID, r.ID, r.ProjectID)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
