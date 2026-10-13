-- Baseline migration.
--
-- Intentionally a no-op: it establishes the migration history that the
-- readiness check (/readyz) and the worker compare against. Domain tables
-- (projects, api_keys, emails, ...) are NOT created here; each arrives in its
-- own reversible migration when the feature that needs it is built.

-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
