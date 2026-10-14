-- Sender identities a project may send from: a single address or a whole
-- domain. Only verified identities authorize a sender; values are stored in
-- lower case and match case-insensitively.

-- +goose Up
CREATE TABLE sender_identities (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id  uuid        NOT NULL REFERENCES projects (id),
    kind        text        NOT NULL CHECK (kind IN ('address', 'domain')),
    value       text        NOT NULL CHECK (length(value) BETWEEN 3 AND 254 AND value = lower(value)),
    status      text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified')),
    verified_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, value),
    CHECK ((status = 'verified') = (verified_at IS NOT NULL)),
    CHECK ((kind = 'address') = (strpos(value, '@') > 0))
);

-- +goose Down
DROP TABLE sender_identities;
