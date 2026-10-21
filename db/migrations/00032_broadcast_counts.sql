-- Progress counts of a broadcast's recipient emails by status, from the
-- index alone.

-- +goose Up
CREATE INDEX emails_broadcast_status_idx ON emails (broadcast_id, status) WHERE broadcast_id IS NOT NULL;

-- +goose Down
DROP INDEX emails_broadcast_status_idx;
