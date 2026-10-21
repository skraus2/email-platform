-- Broadcasts: one template version sent to the subscribed members of an
-- audience, one email per recipient. Recipient emails reference their
-- broadcast and contact; a contact can get at most one email per broadcast.
-- Rolling back the scope and error-code changes fails while rows use them.

-- +goose Up
CREATE TABLE broadcasts (
    id                   uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id           uuid        NOT NULL REFERENCES projects (id),
    name                 text        NOT NULL DEFAULT '' CHECK (char_length(name) <= 200),
    audience_id          uuid        NOT NULL,
    sender               text        NOT NULL CHECK (length(sender) BETWEEN 3 AND 254),
    template_id          uuid        NOT NULL,
    template_version     integer     NOT NULL,
    -- Shared values for the version's variables (checked by the application,
    -- at most 16 KiB of compact JSON; this limit is a backstop).
    variables            jsonb       NOT NULL DEFAULT '{}'
                                     CHECK (jsonb_typeof(variables) = 'object' AND octet_length(variables::text) <= 32768),
    unsubscribe_url      text        NOT NULL CHECK (length(unsubscribe_url) BETWEEN 1 AND 900),
    status               text        NOT NULL DEFAULT 'draft'
                                     CHECK (status IN ('draft', 'scheduled', 'sending', 'completed', 'canceled')),
    scheduled_at         timestamptz,
    started_at           timestamptz,
    finished_at          timestamptz,
    -- Fan-out position: members with a larger contact id are still to come.
    cursor_contact_id    uuid,
    recipients           integer     NOT NULL DEFAULT 0 CHECK (recipients >= 0),
    skipped_unsubscribed integer     NOT NULL DEFAULT 0 CHECK (skipped_unsubscribed >= 0),
    skipped_suppressed   integer     NOT NULL DEFAULT 0 CHECK (skipped_suppressed >= 0),
    skipped_invalid      integer     NOT NULL DEFAULT 0 CHECK (skipped_invalid >= 0),
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, project_id),
    FOREIGN KEY (audience_id, project_id) REFERENCES audiences (id, project_id),
    FOREIGN KEY (template_id, project_id, template_version) REFERENCES template_versions (template_id, project_id, version),
    CHECK (status NOT IN ('scheduled', 'sending', 'completed') OR scheduled_at IS NOT NULL),
    CHECK ((status IN ('completed', 'canceled')) = (finished_at IS NOT NULL))
);

CREATE INDEX broadcasts_project_idx ON broadcasts (project_id, id DESC);
CREATE INDEX broadcasts_project_open_idx ON broadcasts (project_id) WHERE status IN ('draft', 'scheduled', 'sending');

ALTER TABLE emails
    ADD COLUMN broadcast_id uuid,
    ADD COLUMN contact_id uuid,
    ADD CONSTRAINT emails_broadcast_fk FOREIGN KEY (broadcast_id, project_id) REFERENCES broadcasts (id, project_id),
    ADD CONSTRAINT emails_contact_fk FOREIGN KEY (contact_id, project_id)
        REFERENCES contacts (id, project_id) ON DELETE SET NULL (contact_id),
    -- One email per contact and broadcast (NULLs are distinct: emails of
    -- deleted contacts and transactional emails never conflict).
    ADD CONSTRAINT emails_broadcast_contact_key UNIQUE (broadcast_id, contact_id);

CREATE INDEX emails_broadcast_idx ON emails (broadcast_id, id) WHERE broadcast_id IS NOT NULL;
CREATE INDEX emails_contact_idx ON emails (contact_id) WHERE contact_id IS NOT NULL;

ALTER TABLE emails DROP CONSTRAINT emails_error_code_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_check CHECK (error_code IN (
    'provider_unreachable', 'provider_timeout', 'provider_protocol_error',
    'provider_deferred', 'provider_throttled', 'provider_rejected', 'provider_auth_failed',
    'sender_not_verified', 'message_invalid', 'unsafe_target',
    'suppressed', 'project_disabled', 'content_missing', 'expired', 'internal_error',
    'unsubscribed', 'contact_deleted', 'broadcast_canceled', 'render_failed'));

ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['emails:send', 'emails:read', 'templates:read', 'templates:write', 'webhooks:write',
                        'contacts:read', 'contacts:write', 'broadcasts:read', 'broadcasts:write']::text[]);

-- +goose Down
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['emails:send', 'emails:read', 'templates:read', 'templates:write', 'webhooks:write',
                        'contacts:read', 'contacts:write']::text[]);

ALTER TABLE emails DROP CONSTRAINT emails_error_code_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_check CHECK (error_code IN (
    'provider_unreachable', 'provider_timeout', 'provider_protocol_error',
    'provider_deferred', 'provider_throttled', 'provider_rejected', 'provider_auth_failed',
    'sender_not_verified', 'message_invalid', 'unsafe_target',
    'suppressed', 'project_disabled', 'content_missing', 'expired', 'internal_error'));

DROP INDEX emails_contact_idx;
DROP INDEX emails_broadcast_idx;
ALTER TABLE emails
    DROP CONSTRAINT emails_broadcast_contact_key,
    DROP CONSTRAINT emails_contact_fk,
    DROP CONSTRAINT emails_broadcast_fk,
    DROP COLUMN contact_id,
    DROP COLUMN broadcast_id;

DROP TABLE broadcasts;
