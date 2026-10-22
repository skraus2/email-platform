-- Automations: a trigger event name and an immutable, linear list of steps
-- (wait, condition, send) executed per contact in runs. Only the name and
-- the status change after create; archived is final. Rolling back the scope
-- change fails while a key still holds an automations or events scope.

-- +goose Up
CREATE TABLE automations (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id    uuid        NOT NULL REFERENCES projects (id),
    name          text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    status        text        NOT NULL CHECK (status IN ('active', 'paused', 'archived')),
    trigger_event text        NOT NULL CHECK (trigger_event ~ '^[a-z0-9][a-z0-9_.:-]{0,99}$'),
    allow_reentry boolean     NOT NULL DEFAULT false,
    -- Validated by the application (at most 64 KiB of compact JSON); this
    -- limit is a backstop.
    steps         jsonb       NOT NULL CHECK (jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 1 AND 20
                                              AND octet_length(steps::text) <= 131072),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    archived_at   timestamptz,
    UNIQUE (id, project_id),
    CHECK ((status = 'archived') = (archived_at IS NOT NULL))
);

CREATE INDEX automations_project_idx ON automations (project_id, id DESC);
CREATE INDEX automations_trigger_idx ON automations (project_id, trigger_event) WHERE status = 'active';

ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['emails:send', 'emails:read', 'templates:read', 'templates:write', 'webhooks:write',
                        'contacts:read', 'contacts:write', 'broadcasts:read', 'broadcasts:write',
                        'automations:read', 'automations:write', 'events:write']::text[]);

-- +goose Down
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['emails:send', 'emails:read', 'templates:read', 'templates:write', 'webhooks:write',
                        'contacts:read', 'contacts:write', 'broadcasts:read', 'broadcasts:write']::text[]);

DROP TABLE automations;
