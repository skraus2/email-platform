-- name: LockProjectForAutomations :one
-- Serialises automation creation per project so the automation cap holds.
SELECT id FROM projects WHERE id = $1 FOR NO KEY UPDATE;

-- name: CountLiveAutomations :one
SELECT count(*) FROM automations WHERE project_id = $1 AND status <> 'archived';

-- name: InsertAutomation :one
INSERT INTO automations (project_id, name, status, trigger_event, allow_reentry, steps)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAutomation :one
SELECT * FROM automations WHERE id = $1 AND project_id = $2;

-- name: LockAutomation :one
SELECT * FROM automations WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE;

-- name: UpdateAutomation :one
UPDATE automations
SET name = sqlc.arg(name), status = sqlc.arg(status), updated_at = now(),
    archived_at = CASE WHEN sqlc.arg(status)::text = 'archived' THEN now() END
WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id)
RETURNING *;

-- name: ListAutomations :many
-- One page of a project's automations, newest first; before_id is the cursor.
SELECT * FROM automations
WHERE project_id = sqlc.arg(project_id) AND id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountRecentAutomations :one
-- Automations the project created in the last hour (archived ones count).
SELECT count(*) FROM automations WHERE project_id = $1 AND created_at > now() - interval '1 hour';
