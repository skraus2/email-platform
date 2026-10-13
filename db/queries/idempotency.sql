-- name: DeleteExpiredIdempotencyKey :exec
-- An expired key no longer counts; deleting it first lets the same key create
-- a new email.
DELETE FROM idempotency_keys
WHERE project_id = $1 AND operation = $2 AND key = $3
  AND created_at < now() - make_interval(secs => sqlc.arg(ttl_seconds)::float8);

-- name: GetIdempotencyKey :one
SELECT fingerprint, email_id FROM idempotency_keys
WHERE project_id = $1 AND operation = $2 AND key = $3;

-- name: InsertIdempotencyKey :execrows
-- Zero rows: a concurrent request with the same key committed first.
INSERT INTO idempotency_keys (project_id, operation, key, fingerprint, email_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: DeleteIdempotencyKeysOlderThan :execrows
-- Maintenance (all projects): keys past their lifetime, by the database clock.
DELETE FROM idempotency_keys
WHERE created_at < now() - make_interval(secs => sqlc.arg(ttl_seconds)::float8);
