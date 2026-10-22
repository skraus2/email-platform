package broadcasts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
)

// errUnsubscribeInSubject: the version's subject uses the unsubscribe URL
// (create refuses such versions; fan-out checks again where it stores).
var errUnsubscribeInSubject = errors.New("template subject uses the unsubscribe URL")

// BatchSize is the number of audience members one fan-out run handles.
const BatchSize = 500

// SendEnqueuer inserts recipient dispatch jobs inside the caller's
// transaction.
type SendEnqueuer interface {
	EnqueueSendEmails(ctx context.Context, tx pgx.Tx, args []jobs.SendEmailArgs, opts jobs.SendOpts) error
}

// Fanout creates the recipient emails of broadcasts, one batch per job run.
type Fanout struct {
	pool     *pgxpool.Pool
	enq      SendEnqueuer
	logger   *slog.Logger
	compiled *templates.Cache
	batch    int
}

// NewFanout builds the fan-out handler.
func NewFanout(pool *pgxpool.Pool, enq SendEnqueuer, logger *slog.Logger, compiled *templates.Cache) *Fanout {
	return &Fanout{pool: pool, enq: enq, logger: logger, compiled: compiled, batch: BatchSize}
}

// Run is the broadcast_fanout job handler. It processes one batch in one
// transaction under the broadcast's row lock and then snoozes to continue,
// so a crash or stop at any point resumes from the last committed batch.
func (f *Fanout) Run(ctx context.Context, args jobs.BroadcastFanoutArgs, at jobs.Attempt) error {
	log := f.logger.With(
		slog.String("project_id", args.ProjectID.String()),
		slog.String("broadcast_id", args.BroadcastID.String()),
		slog.Int64("job_id", at.JobID),
	)
	var done, disabled bool
	var created, skipped int
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		b, err := q.LockBroadcastForFanout(ctx, store.LockBroadcastForFanoutParams{ID: args.BroadcastID, ProjectID: args.ProjectID})
		if errors.Is(err, pgx.ErrNoRows) {
			done = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock broadcast: %w", err)
		}
		switch {
		case b.Status == StatusCanceled || b.Status == StatusCompleted || b.Status == StatusDraft:
			done = true
			return nil
		case b.ProjectStatus != "active":
			disabled = true
			return nil
		case b.Status == StatusScheduled:
			if err := q.StartBroadcast(ctx, store.StartBroadcastParams{ID: b.ID, ProjectID: b.ProjectID}); err != nil {
				return fmt.Errorf("start broadcast: %w", err)
			}
		}

		v, err := templates.Resolve(ctx, q, b.ProjectID, b.TemplateID, int(b.TemplateVersion))
		if err != nil {
			return fmt.Errorf("load template version: %w", err)
		}
		c, _, err := f.compiled.Compiled(v)
		if err != nil {
			return fmt.Errorf("compile template version: %w", err)
		}
		// Subjects are stored: a version that would put the unsubscribe
		// URL (with its token) there is never fanned out.
		if c.References("subject", contacts.ReservedAttribute) {
			return errUnsubscribeInSubject
		}
		after := uuid.Nil
		if b.CursorContactID != nil {
			after = *b.CursorContactID
		}
		members, err := q.FanoutBatch(ctx, store.FanoutBatchParams{
			AudienceID: b.AudienceID, ProjectID: b.ProjectID, AfterID: after, RowLimit: int32(f.batch), //nolint:gosec // small constant
		})
		if err != nil {
			return fmt.Errorf("load audience batch: %w", err)
		}

		var unsub, supp, invalid int32
		var recipients, subjects []string
		var contactIDs []uuid.UUID
		for _, m := range members {
			switch {
			case m.Status != contacts.StatusSubscribed:
				unsub++
				continue
			case m.Suppressed:
				supp++
				continue
			}
			vars, err := templates.MergeVariables(c.Schema(), b.Variables, m.Attributes,
				map[string]string{contacts.ReservedAttribute: contacts.UnsubscribeURL(b.UnsubscribeUrl, m.UnsubscribeToken)})
			if err != nil {
				invalid++
				continue
			}
			r, err := c.Render(vars)
			if err != nil {
				// The contact's attributes do not fit this version
				// (never logged: they are personal data).
				invalid++
				continue
			}
			recipients = append(recipients, m.Email)
			subjects = append(subjects, r.Subject)
			contactIDs = append(contactIDs, m.ID)
		}
		var ids []uuid.UUID
		if len(contactIDs) > 0 {
			ids, err = q.InsertBroadcastEmails(ctx, store.InsertBroadcastEmailsParams{
				ProjectID: b.ProjectID, Sender: b.Sender, Recipients: recipients, Subjects: subjects,
				TemplateID: b.TemplateID, TemplateVersion: b.TemplateVersion, BroadcastID: b.ID, ContactIds: contactIDs,
			})
			if err != nil {
				return fmt.Errorf("insert recipient emails: %w", err)
			}
			sends := make([]jobs.SendEmailArgs, 0, len(ids))
			for _, id := range ids {
				sends = append(sends, jobs.SendEmailArgs{EmailID: id, ProjectID: b.ProjectID})
			}
			if err := f.enq.EnqueueSendEmails(ctx, tx, sends, jobs.SendOpts{Bulk: true}); err != nil {
				return err
			}
		}
		done = len(members) < f.batch
		cursor := b.CursorContactID
		if len(members) > 0 {
			cursor = &members[len(members)-1].ID
		}
		created, skipped = len(ids), int(unsub+supp+invalid)
		return q.AdvanceBroadcast(ctx, store.AdvanceBroadcastParams{
			ID: b.ID, ProjectID: b.ProjectID, CursorContactID: cursor, Recipients: int32(len(ids)), //nolint:gosec // ≤ batch size
			SkippedUnsubscribed: unsub, SkippedSuppressed: supp, SkippedInvalid: invalid, Done: done,
		})
	})
	if err != nil {
		if at.Final() {
			log.ErrorContext(ctx, "broadcast fan-out failed on its final attempt; the broadcast stays open until it is cancelled", slog.Any("error", err))
		}
		return err
	}
	switch {
	case disabled:
		// Disabling a project stops its broadcasts for good: re-enabling it
		// must not silently send the rest. The broadcast stays open and can
		// be cancelled.
		log.WarnContext(ctx, "project disabled; broadcast fan-out stopped")
		return nil
	case done:
		if created > 0 || skipped > 0 {
			log.InfoContext(ctx, "broadcast fan-out batch", slog.Int("created", created), slog.Int("skipped", skipped))
		}
		return nil
	}
	log.DebugContext(ctx, "broadcast fan-out batch", slog.Int("created", created), slog.Int("skipped", skipped))
	return jobs.Snooze(0)
}
