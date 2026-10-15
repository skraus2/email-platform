-- The daily provider cap counts hand-offs per provider over the last 24
-- hours.

-- +goose Up
CREATE INDEX emails_provider_finished_idx ON emails (provider, finished_at) WHERE provider IS NOT NULL;

-- +goose Down
DROP INDEX emails_provider_finished_idx;
