package emails

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
)

// Enqueuer inserts the dispatch job inside the caller's transaction.
type Enqueuer interface {
	EnqueueSendEmail(ctx context.Context, tx pgx.Tx, args jobs.SendEmailArgs) error
}

// Email is the client-visible state of a send request.
type Email struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	Status     string
	From       string
	To         string
	Subject    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

func fromRow(r store.Email) Email {
	return Email{
		ID: r.ID, ProjectID: r.ProjectID, Status: r.Status, From: r.Sender, To: r.Recipient,
		Subject: r.Subject, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt,
	}
}

// Service accepts and reads emails.
type Service struct {
	pool   *pgxpool.Pool
	enq    Enqueuer
	logger *slog.Logger
}

// NewService builds a Service.
func NewService(pool *pgxpool.Pool, enq Enqueuer, logger *slog.Logger) *Service {
	return &Service{pool: pool, enq: enq, logger: logger}
}

// Create validates the request and commits the email, its content and its
// dispatch job atomically. Only a nil error means the email is persisted.
func (s *Service) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (Email, error) {
	if err := in.Validate(); err != nil {
		return Email{}, err
	}
	var row store.Email
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		row, err = q.InsertEmail(ctx, store.InsertEmailParams{
			ProjectID: projectID, Sender: in.From, Recipient: in.To, Subject: in.Subject,
		})
		if err != nil {
			return fmt.Errorf("insert email: %w", err)
		}
		if err := q.InsertEmailContent(ctx, store.InsertEmailContentParams{
			EmailID: row.ID, ProjectID: projectID, TextBody: nonEmpty(in.Text), HtmlBody: nonEmpty(in.HTML),
		}); err != nil {
			return fmt.Errorf("insert email content: %w", err)
		}
		return s.enq.EnqueueSendEmail(ctx, tx, jobs.SendEmailArgs{EmailID: row.ID, ProjectID: projectID})
	})
	if err != nil {
		return Email{}, err
	}
	s.logger.InfoContext(ctx, "email queued",
		slog.String("project_id", projectID.String()),
		slog.String("email_id", row.ID.String()),
		slog.String("recipient", logging.MaskEmail(in.To)),
	)
	return fromRow(row), nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ErrNotFound means no email with this id exists for the project. Emails of
// other projects are reported the same way.
var ErrNotFound = errors.New("email not found")

// Get returns an email of the project.
func (s *Service) Get(ctx context.Context, projectID, id uuid.UUID) (Email, error) {
	row, err := store.New(s.pool).GetEmail(ctx, store.GetEmailParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Email{}, ErrNotFound
	}
	if err != nil {
		return Email{}, fmt.Errorf("get email: %w", err)
	}
	return fromRow(row), nil
}
