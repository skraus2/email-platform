-- name: LockProjectForBroadcasts :one
-- Serialises broadcast creation per project so the open-broadcast cap holds.
SELECT id FROM projects WHERE id = $1 FOR NO KEY UPDATE;

-- name: CountOpenBroadcasts :one
SELECT count(*) FROM broadcasts WHERE project_id = $1 AND status IN ('draft', 'scheduled', 'sending');

-- name: InsertBroadcast :one
INSERT INTO broadcasts (project_id, name, audience_id, sender, template_id, template_version, variables, unsubscribe_url)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetBroadcast :one
SELECT * FROM broadcasts WHERE id = $1 AND project_id = $2;

-- name: LockBroadcast :one
SELECT * FROM broadcasts WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE;

-- name: ScheduleBroadcast :one
UPDATE broadcasts SET status = 'scheduled', scheduled_at = $3, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'draft'
RETURNING *;

-- name: CancelBroadcast :one
UPDATE broadcasts SET status = 'canceled', finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status <> 'canceled'
RETURNING *;

-- name: ListBroadcasts :many
-- One page of a project's broadcasts, newest first; before_id is the cursor.
SELECT * FROM broadcasts
WHERE project_id = sqlc.arg(project_id) AND id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountBroadcastEmails :many
-- Recipient emails of a broadcast by status.
SELECT status, count(*) AS n FROM emails
WHERE broadcast_id = $1 AND project_id = $2
GROUP BY status;
