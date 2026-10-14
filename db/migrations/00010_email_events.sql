-- Email events: immutable history of platform transitions (captured,
-- submitted, failed) and provider events (delivery, bounce, complaint, ...).
-- Provider events move a submitted email on to delivered, bounced or
-- complained by rank, never back. Those three statuses exist only for
-- emails sent through SES: the local test sink can never report delivery.

-- +goose Up
ALTER TABLE emails DROP CONSTRAINT emails_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_status_check CHECK (status IN (
    'queued', 'sending', 'captured', 'submitted', 'delivered', 'bounced', 'complained', 'failed'));

ALTER TABLE emails DROP CONSTRAINT emails_check;
ALTER TABLE emails ADD CONSTRAINT emails_finished_check CHECK (
    (status IN ('captured', 'submitted', 'delivered', 'bounced', 'complained', 'failed')) = (finished_at IS NOT NULL));

ALTER TABLE emails DROP CONSTRAINT emails_error_code_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_check CHECK (error_code IN (
    'provider_unreachable', 'provider_timeout', 'provider_protocol_error',
    'provider_deferred', 'provider_throttled', 'provider_rejected', 'provider_auth_failed',
    'sender_not_verified', 'message_invalid', 'unsafe_target',
    'suppressed', 'project_disabled', 'content_missing', 'expired', 'internal_error'));

ALTER TABLE emails DROP CONSTRAINT emails_error_code_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_status_check CHECK (
    (status <> 'failed' OR error_code IS NOT NULL)
    AND (status NOT IN ('captured', 'submitted', 'delivered', 'bounced', 'complained') OR error_code IS NULL));

ALTER TABLE emails ADD CONSTRAINT emails_provider_events_check CHECK (
    status NOT IN ('delivered', 'bounced', 'complained') OR provider IS NOT DISTINCT FROM 'ses');

-- Projection order: a status only ever moves to a higher rank.
-- +goose StatementBegin
CREATE FUNCTION email_status_rank(status text) RETURNS integer
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
RETURN CASE status
    WHEN 'submitted' THEN 0
    WHEN 'delivered' THEN 1
    WHEN 'bounced' THEN 2
    WHEN 'complained' THEN 3
END;
-- +goose StatementEnd

CREATE TABLE email_events (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id          uuid        NOT NULL,
    email_id            uuid        NOT NULL,
    origin              text        NOT NULL CHECK (origin IN ('platform', 'ses')),
    type                text        NOT NULL CHECK (type IN (
                            'captured', 'submitted', 'failed', 'send', 'delivered', 'bounced',
                            'complained', 'rejected', 'delivery_delayed')),
    provider            text,
    provider_event_id   text        CHECK (length(provider_event_id) BETWEEN 1 AND 512),
    provider_message_id text        CHECK (length(provider_message_id) BETWEEN 1 AND 512),
    occurred_at         timestamptz NOT NULL,
    received_at         timestamptz NOT NULL DEFAULT now(),
    details             jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details) = 'object'),
    FOREIGN KEY (email_id, project_id) REFERENCES emails (id, project_id) ON DELETE CASCADE,
    UNIQUE (id, project_id),
    UNIQUE (provider, provider_event_id),
    -- Provider events always carry their provider and dedup id; platform
    -- events never have a dedup id (one event per status change).
    CHECK ((origin = 'platform') = (provider_event_id IS NULL)),
    CHECK (origin = 'platform' OR provider = origin),
    CHECK ((origin = 'platform') = (type IN ('captured', 'submitted', 'failed')))
);

CREATE INDEX email_events_email_idx ON email_events (project_id, email_id, id);

-- +goose Down
DROP TABLE email_events;
DROP FUNCTION email_status_rank(text);

-- Lossy: provider statuses fall back to submitted, new codes to internal_error.
UPDATE emails SET status = 'submitted' WHERE status IN ('delivered', 'bounced', 'complained');
UPDATE emails SET error_code = 'internal_error'
WHERE error_code IN ('provider_throttled', 'provider_auth_failed', 'sender_not_verified');

ALTER TABLE emails DROP CONSTRAINT emails_provider_events_check;
ALTER TABLE emails DROP CONSTRAINT emails_error_code_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_status_check CHECK (
    (status <> 'failed' OR error_code IS NOT NULL)
    AND (status NOT IN ('captured', 'submitted') OR error_code IS NULL));
ALTER TABLE emails DROP CONSTRAINT emails_error_code_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_check CHECK (error_code IN (
    'provider_unreachable', 'provider_timeout', 'provider_protocol_error',
    'provider_deferred', 'provider_rejected', 'message_invalid', 'unsafe_target',
    'suppressed', 'project_disabled', 'content_missing', 'expired', 'internal_error'));
ALTER TABLE emails DROP CONSTRAINT emails_finished_check;
ALTER TABLE emails ADD CONSTRAINT emails_check CHECK (
    (status IN ('captured', 'submitted', 'failed')) = (finished_at IS NOT NULL));
ALTER TABLE emails DROP CONSTRAINT emails_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_status_check CHECK (status IN (
    'queued', 'sending', 'captured', 'submitted', 'failed'));
