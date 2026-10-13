-- Recipients a project must never send to again (bounce, complaint,
-- unsubscribe, or added by an operator). Addresses match case-insensitively.

-- +goose Up
CREATE TABLE suppressions (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid        NOT NULL REFERENCES projects (id),
    recipient  text        NOT NULL CHECK (length(recipient) BETWEEN 3 AND 254),
    reason     text        NOT NULL CHECK (reason IN ('bounce', 'complaint', 'unsubscribe', 'manual')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX suppressions_project_recipient_idx ON suppressions (project_id, lower(recipient));

-- +goose Down
DROP TABLE suppressions;
