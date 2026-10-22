-- Every ended run but a completed one names its reason (a NULL reason no
-- longer satisfies the check). Retention finds finished runs by their end
-- time; the hourly automation create limit counts a project's recent
-- creates.

-- +goose Up
ALTER TABLE automation_runs DROP CONSTRAINT automation_runs_check3;
ALTER TABLE automation_runs ADD CONSTRAINT automation_runs_reason_check CHECK (coalesce(CASE status
    WHEN 'active' THEN reason IS NULL
    WHEN 'completed' THEN reason IS NULL
    WHEN 'exited' THEN reason IN ('condition_not_met', 'unsubscribed', 'suppressed', 'project_disabled')
    WHEN 'canceled' THEN reason IN ('canceled', 'automation_archived')
    WHEN 'failed' THEN reason IN ('render_failed', 'internal_error')
END, false));

CREATE INDEX automation_runs_finished_idx ON automation_runs (finished_at) WHERE finished_at IS NOT NULL;
CREATE INDEX automations_project_created_idx ON automations (project_id, created_at);

-- +goose Down
DROP INDEX automations_project_created_idx;
DROP INDEX automation_runs_finished_idx;

ALTER TABLE automation_runs DROP CONSTRAINT automation_runs_reason_check;
ALTER TABLE automation_runs ADD CONSTRAINT automation_runs_check3 CHECK (CASE status
    WHEN 'active' THEN reason IS NULL
    WHEN 'completed' THEN reason IS NULL
    WHEN 'exited' THEN reason IN ('condition_not_met', 'unsubscribed', 'suppressed', 'project_disabled')
    WHEN 'canceled' THEN reason IN ('canceled', 'automation_archived')
    WHEN 'failed' THEN reason IN ('render_failed', 'internal_error')
END);
