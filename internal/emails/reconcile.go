package emails

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/metrics"
	"github.com/skraus2/email-platform/internal/store"
)

// ReconcileConfig sets how long the dispatcher waits for proof that SES
// accepted an email an earlier attempt left "sending" before it sends the
// email again: Window after that attempt in any case, and up to MaxWait while
// the SES event queue QueueKey was not drained within HealthyWithin.
type ReconcileConfig struct {
	Window        time.Duration
	MaxWait       time.Duration
	HealthyWithin time.Duration
	QueueKey      string
}

// Reconcile makes the dispatcher look for a stored SES event of an email an
// earlier attempt left "sending" before it sends the email again. Without it
// (the local test sink) such an email is sent again at once.
func (d *Dispatcher) Reconcile(c ReconcileConfig) error {
	if c.Window < time.Minute || c.MaxWait < c.Window || c.HealthyWithin < time.Minute || c.QueueKey == "" {
		return errors.New("reconcile needs a window of at least 1m, a max wait not below it, a health window of at least 1m and a queue")
	}
	d.reconcile = &c
	return nil
}

// Reconciliation results, one per job run that found an email left
// "sending".
const (
	ReconcileFound             = "found"              // SES had accepted it: recorded, not sent again
	ReconcileWaiting           = "waiting"            // within the window after the earlier attempt
	ReconcilePipelineUnhealthy = "pipeline_unhealthy" // the event queue is not drained: absence proves nothing
	ReconcileResent            = "resent"             // no proof: sent again
)

// unhealthyRecheck is how often a waiting email looks again while the event
// queue is not healthy; up to a tenth of a wait (at most reconcileJitter) is
// added to spread the wake-ups.
const (
	unhealthyRecheck = 5 * time.Minute
	reconcileJitter  = 30 * time.Second
)

// reconcileWait decides, without proof that SES accepted the email, how long
// to wait (0: send again now) and why. age is the time since the earlier
// attempt; healthy is asked only once the window has passed.
func reconcileWait(c ReconcileConfig, age time.Duration, healthy func() (bool, error)) (time.Duration, string, error) {
	if age < c.Window {
		return c.Window - age, ReconcileWaiting, nil
	}
	if age >= c.MaxWait {
		return 0, ReconcileResent, nil
	}
	ok, err := healthy()
	if err != nil {
		return 0, "", err
	}
	if ok {
		return 0, ReconcileResent, nil
	}
	return min(unhealthyRecheck, c.MaxWait-age), ReconcilePipelineUnhealthy, nil
}

// reconcileSending handles an email an earlier attempt left "sending" before
// anything else is checked: if SES accepted it, it is out, and the record
// must say so. done reports that the job run ends here with err; otherwise
// the email is sent again.
func (d *Dispatcher) reconcileSending(ctx context.Context, log *slog.Logger, key store.GetEmailForDispatchParams, at jobs.Attempt, recipient string) (done bool, err error) {
	q := store.New(d.pool)
	ev, err := q.ReconcileEvidence(ctx, store.ReconcileEvidenceParams{ID: key.ID, ProjectID: key.ProjectID, Provider: providerSES})
	if errors.Is(err, pgx.ErrNoRows) {
		d.metrics.Outcome(metrics.OutcomeSkipped)
		return true, nil
	}
	if err != nil {
		return true, d.internalError(ctx, log, key, at, fmt.Errorf("look for SES events: %w", err))
	}
	if d.afterReconcileRead != nil {
		d.afterReconcileRead()
	}
	if ev.MessageID != nil {
		d.metrics.Reconciled(ReconcileFound)
		log.InfoContext(ctx, "SES had accepted the email; recording it instead of sending again", slog.String("result", ReconcileFound))
		return true, d.finish(ctx, log, key, delivery.Result{Provider: providerSES, ProviderID: *ev.MessageID, Status: events.TypeSubmitted}, recipient)
	}
	age := time.Duration(ev.AgeSeconds * float64(time.Second))
	wait, result, err := reconcileWait(*d.reconcile, age, func() (bool, error) {
		return q.QueueDrainedWithin(ctx, store.QueueDrainedWithinParams{QueueKey: d.reconcile.QueueKey, WithinSeconds: d.reconcile.HealthyWithin.Seconds()})
	})
	if err != nil {
		return true, d.internalError(ctx, log, key, at, fmt.Errorf("read event queue health: %w", err))
	}
	d.metrics.Reconciled(result)
	if wait == 0 {
		return false, nil
	}
	log.InfoContext(ctx, "waiting for SES events before sending again", slog.String("result", result), slog.Duration("wait", wait))
	d.metrics.Outcome(metrics.OutcomeSnoozed)
	return true, jobs.Snooze(wait + rand.N(min(wait/10, reconcileJitter)+time.Millisecond)) //nolint:gosec // scheduling jitter, not security
}

// providerSES is the provider name SES hand-offs are stored with.
const providerSES = "ses"
