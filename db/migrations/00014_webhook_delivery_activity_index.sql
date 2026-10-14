-- Stale pending deliveries are found by their last activity (creation or
-- attempt), not by their creation time.

-- +goose Up
DROP INDEX webhook_deliveries_pending_idx;
CREATE INDEX webhook_deliveries_pending_idx ON webhook_deliveries (updated_at) WHERE status = 'pending';

-- +goose Down
DROP INDEX webhook_deliveries_pending_idx;
CREATE INDEX webhook_deliveries_pending_idx ON webhook_deliveries (created_at) WHERE status = 'pending';
