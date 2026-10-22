-- name: CountAutomationRuns :many
-- Runs of an automation by status (callers have checked that the
-- automation belongs to the project).
SELECT status, count(*) AS n FROM automation_runs
WHERE automation_id = $1 AND project_id = $2
GROUP BY status;

-- name: CancelArchivedRuns :execrows
-- Ends up to row_limit active runs of an archived automation. Runs locked by
-- a step job are skipped (that job cancels its run itself).
UPDATE automation_runs
SET status = 'canceled', reason = 'automation_archived', next_step_at = NULL, trigger_data = NULL,
    finished_at = now(), updated_at = now()
WHERE id IN (
    SELECT r.id FROM automation_runs r
    JOIN automations a ON a.id = r.automation_id AND a.project_id = r.project_id
    WHERE r.automation_id = sqlc.arg(automation_id) AND r.project_id = sqlc.arg(project_id)
      AND r.status = 'active' AND a.status = 'archived'
    LIMIT sqlc.arg(row_limit)
    FOR NO KEY UPDATE OF r SKIP LOCKED);

-- name: ActiveRunsLeft :one
-- Whether an archived automation still has active runs (some may have been
-- locked during the last cancellation batch).
SELECT EXISTS (SELECT 1 FROM automation_runs r
               JOIN automations a ON a.id = r.automation_id AND a.project_id = r.project_id
               WHERE r.automation_id = $1 AND r.project_id = $2 AND r.status = 'active' AND a.status = 'archived')::boolean;
