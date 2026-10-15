-- Operators look emails up by the provider's message id.

-- +goose Up
CREATE INDEX emails_provider_id_idx ON emails (provider_id) WHERE provider_id IS NOT NULL;

-- +goose Down
DROP INDEX emails_provider_id_idx;
