-- Contacts of a project: an address with the person's consent status for
-- broadcasts, where that consent came from, attributes for personalising
-- broadcasts, and the token that identifies the contact in unsubscribe
-- links. Addresses are unique per project, case-insensitively. Rolling back
-- the scope change fails while a key still holds a contacts scope.

-- +goose Up
CREATE TABLE contacts (
    id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id        uuid        NOT NULL REFERENCES projects (id),
    email             text        NOT NULL CHECK (length(email) BETWEEN 3 AND 254),
    status            text        NOT NULL CHECK (status IN ('subscribed', 'unsubscribed')),
    consent_source    text        CHECK (char_length(consent_source) BETWEEN 1 AND 100),
    status_changed_at timestamptz NOT NULL DEFAULT now(),
    -- The application limits attributes to 8 KiB of compact JSON; this is
    -- only a backstop (jsonb's text form is longer).
    attributes        jsonb       NOT NULL DEFAULT '{}'
                                  CHECK (jsonb_typeof(attributes) = 'object' AND octet_length(attributes::text) <= 16384),
    unsubscribe_token text        NOT NULL UNIQUE CHECK (unsubscribe_token ~ '^[A-Za-z0-9_-]{43}$'),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, project_id),
    -- A subscribed contact always records where the consent came from.
    CHECK (status <> 'subscribed' OR consent_source IS NOT NULL)
);

CREATE UNIQUE INDEX contacts_project_email_idx ON contacts (project_id, lower(email));
CREATE INDEX contacts_project_idx ON contacts (project_id, id DESC);

ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['emails:send', 'emails:read', 'templates:read', 'templates:write', 'webhooks:write',
                        'contacts:read', 'contacts:write']::text[]);

-- +goose Down
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['emails:send', 'emails:read', 'templates:read', 'templates:write', 'webhooks:write']::text[]);

DROP TABLE contacts;
