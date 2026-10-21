-- Scheduled sends and cancelling: an email may carry the time it is due
-- (it stays queued until then), and a queued email can be cancelled. The
-- safety nets measure age from the due time. Rolling back fails while
-- cancelled emails or their events exist.

-- +goose Up
ALTER TABLE emails ADD COLUMN scheduled_at timestamptz;

ALTER TABLE emails DROP CONSTRAINT emails_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_status_check CHECK (status IN (
    'queued', 'sending', 'captured', 'submitted', 'delivered', 'bounced', 'complained', 'failed', 'canceled'));

ALTER TABLE emails DROP CONSTRAINT emails_finished_check;
ALTER TABLE emails ADD CONSTRAINT emails_finished_check CHECK (
    (status IN ('captured', 'submitted', 'delivered', 'bounced', 'complained', 'failed', 'canceled')) = (finished_at IS NOT NULL));

-- A cancelled email never failed: it carries no error code.
ALTER TABLE emails DROP CONSTRAINT emails_error_code_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_status_check CHECK (
    (status <> 'failed' OR error_code IS NOT NULL)
    AND (status NOT IN ('captured', 'submitted', 'delivered', 'bounced', 'complained', 'canceled') OR error_code IS NULL));

ALTER TABLE email_events DROP CONSTRAINT email_events_type_check;
ALTER TABLE email_events ADD CONSTRAINT email_events_type_check CHECK (type IN (
    'captured', 'submitted', 'failed', 'canceled', 'send', 'delivered', 'bounced',
    'complained', 'rejected', 'delivery_delayed'));
ALTER TABLE email_events DROP CONSTRAINT email_events_check2;
ALTER TABLE email_events ADD CONSTRAINT email_events_check2 CHECK (
    (origin = 'platform') = (type IN ('captured', 'submitted', 'failed', 'canceled')));

ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_event_types_check;
ALTER TABLE webhook_endpoints ADD CONSTRAINT webhook_endpoints_event_types_check CHECK (
    cardinality(event_types) > 0 AND event_types <@ ARRAY[
        'email.captured', 'email.submitted', 'email.failed',
        'email.delivered', 'email.bounced', 'email.complained', 'email.canceled']::text[]);

-- Open emails by due time, for the expiry safety net.
DROP INDEX emails_open_created_idx;
CREATE INDEX emails_open_due_idx ON emails ((coalesce(scheduled_at, created_at)))
    WHERE status IN ('queued', 'sending');

-- +goose Down
DROP INDEX emails_open_due_idx;
CREATE INDEX emails_open_created_idx ON emails (created_at) WHERE status IN ('queued', 'sending');

ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_event_types_check;
ALTER TABLE webhook_endpoints ADD CONSTRAINT webhook_endpoints_event_types_check CHECK (
    cardinality(event_types) > 0 AND event_types <@ ARRAY[
        'email.captured', 'email.submitted', 'email.failed',
        'email.delivered', 'email.bounced', 'email.complained']::text[]);

ALTER TABLE email_events DROP CONSTRAINT email_events_check2;
ALTER TABLE email_events ADD CONSTRAINT email_events_check2 CHECK (
    (origin = 'platform') = (type IN ('captured', 'submitted', 'failed')));
ALTER TABLE email_events DROP CONSTRAINT email_events_type_check;
ALTER TABLE email_events ADD CONSTRAINT email_events_type_check CHECK (type IN (
    'captured', 'submitted', 'failed', 'send', 'delivered', 'bounced',
    'complained', 'rejected', 'delivery_delayed'));

ALTER TABLE emails DROP CONSTRAINT emails_error_code_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_status_check CHECK (
    (status <> 'failed' OR error_code IS NOT NULL)
    AND (status NOT IN ('captured', 'submitted', 'delivered', 'bounced', 'complained') OR error_code IS NULL));

ALTER TABLE emails DROP CONSTRAINT emails_finished_check;
ALTER TABLE emails ADD CONSTRAINT emails_finished_check CHECK (
    (status IN ('captured', 'submitted', 'delivered', 'bounced', 'complained', 'failed')) = (finished_at IS NOT NULL));

ALTER TABLE emails DROP CONSTRAINT emails_status_check;
ALTER TABLE emails ADD CONSTRAINT emails_status_check CHECK (status IN (
    'queued', 'sending', 'captured', 'submitted', 'delivered', 'bounced', 'complained', 'failed'));

ALTER TABLE emails DROP COLUMN scheduled_at;
