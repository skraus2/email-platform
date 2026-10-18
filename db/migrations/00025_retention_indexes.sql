-- Indexes for the periodic retention deletes: old events by arrival time and
-- finished webhook deliveries by their last change.

-- +goose Up
CREATE INDEX email_events_received_idx ON email_events (received_at);
CREATE INDEX webhook_deliveries_finished_idx ON webhook_deliveries (updated_at) WHERE status IN ('delivered', 'failed');

-- +goose Down
DROP INDEX webhook_deliveries_finished_idx;
DROP INDEX email_events_received_idx;
