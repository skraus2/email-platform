-- name: SetWebhookLockTimeout :exec
-- Bounds the wait for the project row lock in this transaction.
SET LOCAL lock_timeout = '3s';

-- name: LockProjectForWebhooks :one
-- Serialises endpoint creation per project so the endpoint cap holds. NO KEY
-- UPDATE does not block the KEY SHARE locks of email or endpoint inserts.
SELECT id FROM projects WHERE id = $1 FOR NO KEY UPDATE;

-- name: CountWebhookEndpoints :one
-- Active and disabled endpoints both count against the cap.
SELECT count(*) FROM webhook_endpoints WHERE project_id = $1;

-- name: InsertWebhookEndpoint :one
INSERT INTO webhook_endpoints (id, project_id, url, event_types, secret_ciphertext, secret_key_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListWebhookEndpoints :many
SELECT * FROM webhook_endpoints WHERE project_id = $1 ORDER BY created_at, id;

-- name: SetWebhookEndpointStatus :execrows
UPDATE webhook_endpoints SET status = $3, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status <> $3;

-- name: LockWebhookEndpoint :one
-- NO KEY UPDATE: does not block the key-share lock that new deliveries take.
SELECT * FROM webhook_endpoints WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE;

-- name: UpdateWebhookSecret :exec
UPDATE webhook_endpoints SET secret_ciphertext = $3, secret_key_id = $4, updated_at = now()
WHERE id = $1 AND project_id = $2;

-- name: LockEndpointsNotUnderKey :many
-- Maintenance across projects: secrets still encrypted under another key.
SELECT * FROM webhook_endpoints WHERE secret_key_id <> $1 ORDER BY id FOR NO KEY UPDATE;

-- name: InsertWebhookDeliveries :many
-- One delivery per active endpoint of the project subscribed to the type.
INSERT INTO webhook_deliveries (project_id, endpoint_id, event_id)
SELECT e.project_id, e.id, sqlc.arg(event_id) FROM webhook_endpoints e
WHERE e.project_id = sqlc.arg(project_id) AND e.status = 'active' AND sqlc.arg(event_type)::text = ANY (e.event_types)
ON CONFLICT (endpoint_id, event_id) DO NOTHING
RETURNING id;

-- name: LoadWebhookDelivery :one
SELECT d.id, d.project_id, d.status, d.attempts,
       e.id AS endpoint_id, e.url, e.status AS endpoint_status, e.secret_ciphertext, e.secret_key_id,
       e.event_types, e.created_at AS endpoint_created_at, e.updated_at AS endpoint_updated_at,
       ev.id AS event_id, ev.email_id, ev.type AS event_type, ev.received_at
FROM webhook_deliveries d
JOIN webhook_endpoints e ON e.id = d.endpoint_id AND e.project_id = d.project_id
JOIN email_events ev ON ev.id = d.event_id AND ev.project_id = d.project_id
WHERE d.id = $1 AND d.project_id = $2;

-- name: MarkDeliveryDelivered :execrows
UPDATE webhook_deliveries
SET status = 'delivered', attempts = attempts + 1, last_status_code = $3, last_error_code = NULL,
    next_attempt_at = NULL, delivered_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'pending';

-- name: RecordDeliveryRetry :one
-- A failed, retryable attempt: retried at next_attempt_at, or failed in the
-- same statement when it was the last allowed one.
UPDATE webhook_deliveries
SET attempts = attempts + 1, last_status_code = $3, last_error_code = $4,
    status = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts)::integer THEN 'failed' ELSE status END,
    next_attempt_at = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts)::integer THEN NULL ELSE sqlc.arg(next_attempt_at)::timestamptz END,
    updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'pending'
RETURNING attempts, status;

-- name: MarkDeliveryFailed :execrows
-- counted: whether this call ends an attempt (false when no request was made
-- or the attempt was already counted).
UPDATE webhook_deliveries
SET status = 'failed', attempts = attempts + CASE WHEN sqlc.arg(counted)::boolean THEN 1 ELSE 0 END,
    last_status_code = coalesce(sqlc.narg(status_code), last_status_code), last_error_code = $3,
    next_attempt_at = NULL, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'pending';

-- name: ExpireStaleDeliveries :execrows
-- Safety net for deliveries whose job ended without a final state: no
-- activity (creation or attempt) since the cutoff.
UPDATE webhook_deliveries
SET status = 'failed', last_error_code = 'expired', next_attempt_at = NULL, updated_at = now()
WHERE status = 'pending' AND updated_at < $1;

-- name: ListWebhookDeliveries :many
SELECT d.*, ev.email_id, ev.type AS event_type FROM webhook_deliveries d
JOIN email_events ev ON ev.id = d.event_id AND ev.project_id = d.project_id
WHERE d.project_id = $1 AND (sqlc.narg(status)::text IS NULL OR d.status = sqlc.narg(status))
ORDER BY d.created_at DESC, d.id DESC
LIMIT $2;
