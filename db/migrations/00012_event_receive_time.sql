-- received_at records when the event row was written, not when its
-- transaction began: an ingest that waited for the email's row lock must not
-- show an earlier receive time than an event stored before it.

-- +goose Up
ALTER TABLE email_events ALTER COLUMN received_at SET DEFAULT clock_timestamp();

-- +goose Down
ALTER TABLE email_events ALTER COLUMN received_at SET DEFAULT now();
