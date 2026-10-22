package automations

import (
	"context"
	"log/slog"

	"github.com/skraus2/email-platform/internal/jobs"
)

// FailRun exposes the final-attempt failure mark for tests.
func FailRun(e *Engine, ctx context.Context, args jobs.AutomationStepArgs, cause error) error {
	return e.failRun(ctx, slog.New(slog.DiscardHandler), args, cause)
}
