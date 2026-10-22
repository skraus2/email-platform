-- name: CountContactEvent :one
-- Counts one event in the current minute of the project and returns the new
-- count and the seconds until the window ends (by the database clock).
INSERT INTO contact_event_windows AS w (project_id, window_start, count)
VALUES ($1, date_trunc('minute', now()), 1)
ON CONFLICT (project_id, window_start) DO UPDATE SET count = w.count + 1
RETURNING w.count,
    ceil(extract(epoch FROM (w.window_start + interval '1 minute') - clock_timestamp()))::integer AS retry_after_seconds;

-- name: DeleteContactEventWindowsBefore :execrows
-- Maintenance (all projects): windows that ended long ago.
DELETE FROM contact_event_windows WHERE window_start < now() - interval '1 hour';

-- name: GetContactEventByKey :one
SELECT id, contact_id, name, fingerprint, created_at FROM contact_events
WHERE project_id = $1 AND idempotency_key = $2;

-- name: LockContactForEvent :one
-- Serialises event processing per contact (re-entry rules, condition
-- ordering). Exactly one of contact_id and email is given.
SELECT id FROM contacts
WHERE project_id = sqlc.arg(project_id)
  AND ((sqlc.narg(contact_id)::uuid IS NOT NULL AND id = sqlc.narg(contact_id)::uuid)
       OR (sqlc.narg(email)::text IS NOT NULL AND lower(email) = lower(sqlc.narg(email)::text)))
FOR NO KEY UPDATE;

-- name: InsertContactEvent :one
-- No row when another request with the same key inserted first.
INSERT INTO contact_events (project_id, contact_id, name, idempotency_key, fingerprint)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (project_id, idempotency_key) DO NOTHING
RETURNING id, created_at;

-- name: TriggeredAutomations :many
-- Active automations of the project started by an event name.
SELECT id, allow_reentry FROM automations
WHERE project_id = $1 AND trigger_event = $2 AND status = 'active'
ORDER BY id;

-- name: BlockingRunExists :one
-- Whether the contact may not start a run of the automation: an active run,
-- or (without re-entry) any run at all.
SELECT EXISTS (
    SELECT 1 FROM automation_runs
        WHERE automation_id = sqlc.arg(automation_id) AND contact_id = sqlc.arg(contact_id) AND project_id = sqlc.arg(project_id)
      AND (status = 'active' OR NOT sqlc.arg(allow_reentry)::boolean))::boolean;

-- name: InsertRun :one
INSERT INTO automation_runs (project_id, automation_id, contact_id, trigger_event_id, next_step_at, trigger_data)
VALUES ($1, $2, $3, $4, clock_timestamp(), $5)
RETURNING id, next_step_at;

-- name: RunsOfEvent :many
SELECT id, automation_id FROM automation_runs
WHERE trigger_event_id = $1 AND project_id = $2
ORDER BY automation_id;

-- name: DeleteOldContactEvents :execrows
-- Retention (all projects): one batch of events older than the cutoff.
DELETE FROM contact_events WHERE id IN (
    SELECT ce.id FROM contact_events ce WHERE ce.created_at < sqlc.arg(before)::timestamptz
    LIMIT sqlc.arg(batch_size));

-- name: DeleteOldReentryRuns :execrows
-- Retention (all projects): one batch of runs finished before the cutoff of
-- automations that allow re-entry (the others are bounded and needed by the
-- "one run per contact" rule).
DELETE FROM automation_runs WHERE id IN (
    SELECT r.id FROM automation_runs r
    JOIN automations a ON a.id = r.automation_id AND a.project_id = r.project_id
    WHERE a.allow_reentry AND r.finished_at < sqlc.arg(before)::timestamptz
    LIMIT sqlc.arg(batch_size));

-- name: GetContactEventCount :one
-- Read-only pre-check: events already committed in the current minute of
-- the project, and the seconds until that window ends.
SELECT coalesce((SELECT w.count FROM contact_event_windows w
                 WHERE w.project_id = $1 AND w.window_start = date_trunc('minute', now())), 0)::integer AS count,
       ceil(extract(epoch FROM date_trunc('minute', now()) + interval '1 minute' - clock_timestamp()))::integer AS retry_after_seconds;
