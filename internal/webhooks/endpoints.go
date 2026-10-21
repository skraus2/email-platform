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
	"email.delivered", "email.bounced", "email.complained", "email.canceled",
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

// ErrUnknownEventType means an event type is not one of EventTypes.
var ErrUnknownEventType = errors.New("unknown webhook event type")

// ErrEndpointLimit means the project already has as many endpoints as the
// cap allows (active and disabled).
var ErrEndpointLimit = errors.New("project has reached its webhook endpoint limit")

// ErrBusy means the create could not take the project lock in time; retry.
var ErrBusy = errors.New("webhook endpoint creation busy")

func normalizeTypes(types []string) ([]string, error) {
	if len(types) == 0 {
		return slices.Clone(EventTypes), nil
	}
	var out []string
	for _, t := range types {
		if !slices.Contains(EventTypes, t) {
			return nil, fmt.Errorf("%w %q; known: %v", ErrUnknownEventType, t, EventTypes)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Registrar creates webhook endpoints, at most MaxEndpoints per project.
type Registrar struct {
	Pool         *pgxpool.Pool
	Keys         *Keyring
	Policy       TargetPolicy
	MaxEndpoints int
}

// Create registers an endpoint (all event types when types is empty) and
// returns it with its new secret. The secret is stored only encrypted and
// cannot be read back: show it once.
func (r Registrar) Create(ctx context.Context, projectID uuid.UUID, rawURL string, types []string) (Endpoint, string, error) {
	if r.Keys == nil {
		return Endpoint{}, "", ErrNoKeys
	}
	if r.MaxEndpoints < 1 {
		return Endpoint{}, "", errors.New("webhook endpoint cap must be at least 1")
	}
	u, err := r.Policy.ValidateURL(rawURL)
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
	// A project at its cap is refused without taking the project lock, so
	// repeated refused creates do not queue other writers of the project
	// behind it; the locked count below stays authoritative.
	if n, err := store.New(r.Pool).CountWebhookEndpoints(ctx, projectID); err != nil {
		return Endpoint{}, "", fmt.Errorf("count webhook endpoints: %w", err)
	} else if n >= int64(r.MaxEndpoints) {
		return Endpoint{}, "", ErrEndpointLimit
	}
	secret := NewSecret()
	ct, kid, err := r.Keys.Encrypt([]byte(secret), id, projectID)
	if err != nil {
		return Endpoint{}, "", err
	}
	var e Endpoint
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetWebhookLockTimeout(ctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		if _, err := q.LockProjectForWebhooks(ctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownProject
			}
			return fmt.Errorf("lock project: %w", err)
		}
		n, err := q.CountWebhookEndpoints(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count webhook endpoints: %w", err)
		}
		if n >= int64(r.MaxEndpoints) {
			return ErrEndpointLimit
		}
		row, err := q.InsertWebhookEndpoint(ctx, store.InsertWebhookEndpointParams{
			ID: id, ProjectID: projectID, Url: u.String(), EventTypes: types, SecretCiphertext: ct, SecretKeyID: kid,
		})
		if err != nil {
			return fmt.Errorf("insert webhook endpoint: %w", err)
		}
		e = endpointFromRow(row)
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" && ctx.Err() == nil {
		return Endpoint{}, "", ErrBusy
	}
	if err != nil {
		return Endpoint{}, "", err
	}
	return e, secret, nil
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
