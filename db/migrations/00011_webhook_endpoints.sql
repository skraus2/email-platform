-- Outbound webhook endpoints of a project. The signing secret is stored only
-- encrypted (AES-256-GCM under a key-encryption key from the environment,
-- identified by secret_key_id; the ciphertext is bound to id and project).

-- +goose Up
CREATE TABLE webhook_endpoints (
    id                uuid        PRIMARY KEY,
    project_id        uuid        NOT NULL REFERENCES projects (id),
    url               text        NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    event_types       text[]      NOT NULL CHECK (cardinality(event_types) > 0 AND event_types <@ ARRAY[
                          'email.captured', 'email.submitted', 'email.failed',
                          'email.delivered', 'email.bounced', 'email.complained']::text[]),
    secret_ciphertext bytea       NOT NULL CHECK (length(secret_ciphertext) > 28),
    secret_key_id     text        NOT NULL CHECK (secret_key_id ~ '^[a-z0-9_-]{1,32}$'),
    status            text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, project_id)
);

CREATE INDEX webhook_endpoints_project_status_idx ON webhook_endpoints (project_id, status);

-- +goose Down
DROP TABLE webhook_endpoints;
