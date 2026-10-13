-- Projects and their API keys. Keys are stored only as SHA-256 of the secret;
-- the public prefix is used for lookup.

-- +goose Up
CREATE TABLE projects (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    status     text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id  uuid        NOT NULL REFERENCES projects (id),
    prefix      text        NOT NULL UNIQUE CHECK (prefix ~ '^[a-z2-7]{12}$'),
    secret_hash bytea       NOT NULL CHECK (length(secret_hash) = 32),
    scopes      text[]      NOT NULL CHECK (cardinality(scopes) > 0
                                            AND scopes <@ ARRAY['emails:send', 'emails:read']::text[]),
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz
);

CREATE INDEX api_keys_project_id_idx ON api_keys (project_id);

-- +goose Down
DROP TABLE api_keys;
DROP TABLE projects;
