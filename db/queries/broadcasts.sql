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
-- Recipient emails of a broadcast by status (callers have checked that the
-- broadcast belongs to the project; the count uses the index alone).
SELECT status, count(*) AS n FROM emails
WHERE broadcast_id = $1
GROUP BY status;

-- name: CountRecentBroadcasts :one
-- Broadcasts the project created in the last hour.
SELECT count(*) FROM broadcasts WHERE project_id = $1 AND created_at > now() - interval '1 hour';

-- name: SetBroadcastStatementTimeout :exec
SET LOCAL statement_timeout = '5s';

-- name: LockBroadcastForFanout :one
-- The broadcast with its project's status, locked for one fan-out batch.
SELECT b.*, p.status AS project_status
FROM broadcasts b JOIN projects p ON p.id = b.project_id
WHERE b.id = $1 AND b.project_id = $2
FOR NO KEY UPDATE OF b;

-- name: StartBroadcast :exec
UPDATE broadcasts SET status = 'sending', started_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'scheduled';

-- name: FanoutBatch :many
-- The next members of the audience after the cursor, with what the
-- fan-out needs to decide and render (this query and the dispatch load are
-- the only broadcast paths that read the unsubscribe token).
SELECT c.id, c.email, c.status, c.attributes, c.unsubscribe_token,
       EXISTS (SELECT 1 FROM suppressions s
               WHERE s.project_id = c.project_id AND lower(s.recipient) = lower(c.email))::boolean AS suppressed
FROM audience_members m
JOIN contacts c ON c.id = m.contact_id AND c.project_id = m.project_id
WHERE m.audience_id = sqlc.arg(audience_id) AND m.project_id = sqlc.arg(project_id)
  AND m.contact_id > sqlc.arg(after_id)::uuid
ORDER BY m.contact_id
LIMIT sqlc.arg(row_limit);

-- name: InsertBroadcastEmails :many
-- One email per contact; a contact that already has one for this broadcast
-- is skipped (a repeated batch creates nothing twice).
INSERT INTO emails (project_id, sender, recipient, subject, template_id, template_version, broadcast_id, contact_id)
-- The three arrays have one element per contact (unnest in the select
-- list zips arrays of equal length).
SELECT sqlc.arg(project_id)::uuid, sqlc.arg(sender)::text, unnest(sqlc.arg(recipients)::text[]),
       unnest(sqlc.arg(subjects)::text[]), sqlc.arg(template_id)::uuid, sqlc.arg(template_version)::integer,
       sqlc.arg(broadcast_id)::uuid, unnest(sqlc.arg(contact_ids)::uuid[])
ON CONFLICT (broadcast_id, contact_id) DO NOTHING
RETURNING id;

-- name: AdvanceBroadcast :exec
-- Records one fan-out batch; a short batch completes the broadcast.
UPDATE broadcasts
SET cursor_contact_id = sqlc.narg(cursor_contact_id),
    recipients = recipients + sqlc.arg(recipients),
    skipped_unsubscribed = skipped_unsubscribed + sqlc.arg(skipped_unsubscribed),
    skipped_suppressed = skipped_suppressed + sqlc.arg(skipped_suppressed),
    skipped_invalid = skipped_invalid + sqlc.arg(skipped_invalid),
    status = CASE WHEN sqlc.arg(done)::boolean THEN 'completed' ELSE status END,
    finished_at = CASE WHEN sqlc.arg(done)::boolean THEN now() ELSE finished_at END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id) AND status = 'sending';
