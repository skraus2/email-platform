-- Deleting an email cascades to its idempotency keys; without this index each
-- deleted email scans the key table.

-- +goose Up
CREATE INDEX idempotency_keys_email_idx ON idempotency_keys (email_id, project_id);

-- +goose Down
DROP INDEX idempotency_keys_email_idx;
