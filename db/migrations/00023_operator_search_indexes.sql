-- Operator searches across projects by status or failure code, and the
-- webhook deliveries of an email's events.

-- +goose Up
CREATE INDEX emails_status_idx ON emails (status, id DESC);
CREATE INDEX emails_error_code_idx ON emails (error_code, id DESC) WHERE error_code IS NOT NULL;
CREATE INDEX webhook_deliveries_event_idx ON webhook_deliveries (event_id, project_id);

-- +goose Down
DROP INDEX webhook_deliveries_event_idx;
DROP INDEX emails_error_code_idx;
DROP INDEX emails_status_idx;
