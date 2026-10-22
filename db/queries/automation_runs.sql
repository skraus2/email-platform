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

-- name: LockRunContact :one
-- First lock of a step job: the run's contact, shared (same order as contact
-- deletion and event reports, which lock the contact before runs; it also
-- waits for an event report of the contact still in flight).
SELECT c.id, c.email, c.status, c.attributes, c.unsubscribe_token FROM contacts c
WHERE c.project_id = sqlc.arg(project_id)
  AND c.id = (SELECT r.contact_id FROM automation_runs r WHERE r.id = sqlc.arg(run_id) AND r.project_id = sqlc.arg(project_id))
FOR SHARE;

-- name: LockRunForStep :one
SELECT r.id, r.project_id, r.automation_id, r.status, r.step, r.trigger_data, r.created_at,
       a.status AS automation_status, a.steps, p.status AS project_status,
       EXISTS (SELECT 1 FROM suppressions s
               WHERE s.project_id = r.project_id AND lower(s.recipient) = lower(c.email))::boolean AS suppressed
FROM automation_runs r
JOIN automations a ON a.id = r.automation_id AND a.project_id = r.project_id
JOIN projects p ON p.id = r.project_id
JOIN contacts c ON c.id = r.contact_id AND c.project_id = r.project_id
WHERE r.id = $1 AND r.project_id = $2
FOR NO KEY UPDATE OF r;

-- name: WaitRun :exec
UPDATE automation_runs SET step = $3, next_step_at = $4, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'active';

-- name: EndRun :exec
UPDATE automation_runs
SET status = sqlc.arg(status), reason = sqlc.narg(reason), step = sqlc.arg(step), next_step_at = NULL,
    trigger_data = NULL, finished_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id) AND status = 'active';

-- name: FailRunAtStep :execrows
-- After the final attempt of a step job: only a run still at that step.
UPDATE automation_runs
SET status = 'failed', reason = 'internal_error', next_step_at = NULL, trigger_data = NULL,
    finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'active' AND step = $3;

-- name: EventOccurredSince :one
-- Whether the contact reported an event of that name after the given time
-- (the run's start; its trigger event is older).
SELECT EXISTS (SELECT 1 FROM contact_events
               WHERE contact_id = $1 AND project_id = $2 AND name = $3 AND created_at > sqlc.arg(since)::timestamptz)::boolean;

-- name: OverdueRuns :many
-- Active runs whose next step was due before the cutoff, oldest first.
SELECT id, project_id, step FROM automation_runs
WHERE status = 'active' AND next_step_at < sqlc.arg(before)::timestamptz
ORDER BY next_step_at
LIMIT sqlc.arg(row_limit);

-- name: GetRun :one
-- Never the trigger data (personal event data for rendering only).
SELECT id, project_id, automation_id, contact_id, trigger_event_id, status, reason, step, next_step_at, created_at, updated_at, finished_at FROM automation_runs WHERE id = $1 AND project_id = $2;

-- name: EmailsOfRun :many
SELECT id, automation_step, status FROM emails
WHERE automation_run_id = $1 AND project_id = $2
ORDER BY automation_step;

-- name: ListRuns :many
-- One page of an automation's runs, newest first; before_id is the cursor.
SELECT id, project_id, automation_id, contact_id, trigger_event_id, status, reason, step, next_step_at, created_at, updated_at, finished_at FROM automation_runs
WHERE project_id = sqlc.arg(project_id) AND automation_id = sqlc.arg(automation_id) AND id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(contact_id)::uuid IS NULL OR contact_id = sqlc.narg(contact_id)::uuid)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: CancelRun :one
-- Waits for a step job holding the run; the run's queued emails are
-- canceled by dispatch.
UPDATE automation_runs
SET status = 'canceled', reason = 'canceled', next_step_at = NULL, trigger_data = NULL,
    finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'active'
RETURNING id, project_id, automation_id, contact_id, trigger_event_id, status, reason, step, next_step_at, created_at, updated_at, finished_at;
