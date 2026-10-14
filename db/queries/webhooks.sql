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
SELECT * FROM webhook_endpoints WHERE id = $1 AND project_id = $2 FOR UPDATE;

-- name: UpdateWebhookSecret :exec
UPDATE webhook_endpoints SET secret_ciphertext = $3, secret_key_id = $4, updated_at = now()
WHERE id = $1 AND project_id = $2;

-- name: LockEndpointsNotUnderKey :many
-- Maintenance across projects: secrets still encrypted under another key.
SELECT * FROM webhook_endpoints WHERE secret_key_id <> $1 ORDER BY id FOR UPDATE;
