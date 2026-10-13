-- Idempotency keys of send requests. A key is unique per project and API
-- operation; the row points at the email the first request created and holds
-- a hash of that request's payload, so a repeated request can be answered
-- with the original email or refused if the payload differs.

-- +goose Up
CREATE TABLE idempotency_keys (
    project_id  uuid        NOT NULL REFERENCES projects (id),
    operation   text        NOT NULL CHECK (operation IN ('create_email')),
    key         text        NOT NULL CHECK (octet_length(key) BETWEEN 1 AND 255),
    fingerprint bytea       NOT NULL CHECK (octet_length(fingerprint) = 32),
    email_id    uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, operation, key),
    FOREIGN KEY (email_id, project_id) REFERENCES emails (id, project_id) ON DELETE CASCADE
);

CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
