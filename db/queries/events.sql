-- name: InsertPlatformEvent :one
INSERT INTO email_events (project_id, email_id, origin, type, provider, occurred_at, details)
VALUES ($1, $2, 'platform', $3, $4, now(), $5)
RETURNING id;

-- name: LockEmailForEvent :one
-- Taken first by every event ingest: orders it against the worker's state
-- writes on the same email row.
SELECT id, project_id, status, provider, provider_id, recipient
FROM emails WHERE id = $1 AND project_id = $2
FOR UPDATE;

-- name: InsertProviderEvent :one
-- No row: the event was stored before (duplicate delivery).
INSERT INTO email_events (project_id, email_id, origin, type, provider, provider_event_id,
                          provider_message_id, occurred_at, details)
VALUES ($1, $2, $3, $4, $3, $5, $6, $7, $8)
ON CONFLICT (provider, provider_event_id) DO NOTHING
RETURNING id;

-- name: ProjectEmailStatus :execrows
-- Moves a dispatched email to a higher-ranked provider status; never down,
-- never out of captured, failed or an open state.
UPDATE emails SET status = sqlc.arg(status), updated_at = now()
WHERE id = $1 AND project_id = $2
  AND status IN ('submitted', 'delivered', 'bounced', 'complained')
  AND email_status_rank(status) < email_status_rank(sqlc.arg(status));

-- name: TopProviderEvent :one
-- The stored provider event with the highest projection rank (earliest
-- received among equals).
SELECT id, type FROM email_events
WHERE project_id = $1 AND email_id = $2 AND type IN ('delivered', 'bounced', 'complained')
ORDER BY email_status_rank(type) DESC, id
LIMIT 1;

-- name: ListEmailEvents :many
SELECT * FROM email_events
WHERE project_id = $1 AND email_id = $2 AND id > sqlc.arg(after)
ORDER BY id
LIMIT $3;

-- name: ProviderEventOwner :one
SELECT project_id, email_id FROM email_events WHERE provider = $1 AND provider_event_id = $2;
