package emails

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/store"
)

// ErrSenderNotVerified means verified senders are required and the from
// address matches no verified address or domain identity of the project.
var ErrSenderNotVerified = errors.New("sender is not a verified identity of the project")

// ErrSenderExists means the project already has this identity.
var ErrSenderExists = errors.New("sender identity already exists")

// SenderKind is what a sender identity covers.
type SenderKind string

// Sender identity kinds: one exact address, or every address of one exact
// domain (subdomains not included).
const (
	SenderAddress SenderKind = "address"
	SenderDomain  SenderKind = "domain"
)

// SenderIdentity is an address or domain a project may send from once it is
// verified.
type SenderIdentity struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	Kind       string
	Value      string
	Status     string // pending or verified
	VerifiedAt *time.Time
	CreatedAt  time.Time
}

func senderFromRow(r store.SenderIdentity) SenderIdentity {
	return SenderIdentity{
		ID: r.ID, ProjectID: r.ProjectID, Kind: r.Kind, Value: r.Value,
		Status: r.Status, VerifiedAt: r.VerifiedAt, CreatedAt: r.CreatedAt,
	}
}

// AddSender adds a pending sender identity to a project. Values are stored
// in lower case.
func AddSender(ctx context.Context, q *store.Queries, projectID uuid.UUID, kind SenderKind, value string) (SenderIdentity, error) {
	switch kind {
	case SenderAddress:
		if err := ValidateAddress(value); err != nil {
			return SenderIdentity{}, err
		}
	case SenderDomain:
		if err := validateDomain(value); err != nil {
			return SenderIdentity{}, err
		}
	default:
		return SenderIdentity{}, fmt.Errorf("sender kind must be %q or %q", SenderAddress, SenderDomain)
	}
	row, err := q.AddSenderIdentity(ctx, store.AddSenderIdentityParams{
		ProjectID: projectID, Kind: string(kind), Value: strings.ToLower(value),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SenderIdentity{}, ErrSenderExists
	}
	if err != nil {
		return SenderIdentity{}, fmt.Errorf("add sender identity: %w", err)
	}
	return senderFromRow(row), nil
}

// VerifySender marks a pending identity verified. It is the operator's
// assertion that the provider has verified the identity; ok is false if the
// identity does not exist or is already verified.
func VerifySender(ctx context.Context, q *store.Queries, id uuid.UUID) (bool, error) {
	n, err := q.VerifySenderIdentity(ctx, id)
	if err != nil {
		return false, fmt.Errorf("verify sender identity: %w", err)
	}
	return n > 0, nil
}

// RemoveSender deletes an identity; removed is false if it did not exist.
// Emails already accepted from it are not affected.
func RemoveSender(ctx context.Context, q *store.Queries, id uuid.UUID) (bool, error) {
	n, err := q.RemoveSenderIdentity(ctx, id)
	if err != nil {
		return false, fmt.Errorf("remove sender identity: %w", err)
	}
	return n > 0, nil
}

// ListSenders returns the project's identities ordered by value; limit 0
// means the default of the admin listing.
func ListSenders(ctx context.Context, q *store.Queries, projectID uuid.UUID, limit int) ([]SenderIdentity, error) {
	if limit == 0 {
		limit = DefaultAdminListLimit
	}
	if limit < 1 || limit > MaxAdminListLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxAdminListLimit)
	}
	rows, err := q.ListSenderIdentities(ctx, store.ListSenderIdentitiesParams{ProjectID: projectID, Limit: int32(limit)}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("list sender identities: %w", err)
	}
	out := make([]SenderIdentity, 0, len(rows))
	for _, r := range rows {
		out = append(out, senderFromRow(r))
	}
	return out, nil
}

// senderVerified reports whether from (an already validated address) is
// covered by a verified identity of the project.
func senderVerified(ctx context.Context, q *store.Queries, projectID uuid.UUID, from string) (bool, error) {
	domain := from[strings.LastIndexByte(from, '@')+1:]
	ok, err := q.IsSenderVerified(ctx, store.IsSenderVerifiedParams{ProjectID: projectID, Address: from, Domain: domain})
	if err != nil {
		return false, fmt.Errorf("check sender identity: %w", err)
	}
	return ok, nil
}

// validateDomain accepts an ASCII DNS name with at least two labels: letters,
// digits and inner hyphens, labels of 1–63 characters, at most 253 in total,
// no trailing dot. International domains are given in punycode.
func validateDomain(v string) error {
	if len(v) < 3 || len(v) > 253 {
		return errors.New("domain must be 3 to 253 characters")
	}
	labels := strings.Split(v, ".")
	if len(labels) < 2 {
		return errors.New("domain must have at least two labels, like example.com")
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return errors.New("domain labels must be 1 to 63 characters and must not start or end with a hyphen")
		}
		if strings.IndexFunc(l, notHostChar) >= 0 {
			return errors.New("domain must contain only ASCII letters, digits, hyphens and dots (use punycode for international domains)")
		}
	}
	return nil
}

func notHostChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		return false
	}
	return true
}
