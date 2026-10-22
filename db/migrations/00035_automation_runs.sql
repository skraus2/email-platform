-- Contact events reported by apps, and automation runs: one per automation
-- and contact start, with an explicit state, the index of the next step and
-- when it is due. Each due step is a job in the job queue; the run row is
-- the source of truth that makes a repeated job a no-op. Automation emails
-- reference their automation, run and step; a step creates at most one
-- email. Rolling back the error-code change fails while rows use it.

-- +goose Up
CREATE TABLE contact_events (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id      uuid        NOT NULL REFERENCES projects (id),
    contact_id      uuid        NOT NULL,
    name            text        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_.:-]{0,99}$'),
    idempotency_key text        CHECK (octet_length(idempotency_key) BETWEEN 1 AND 255),
    fingerprint     bytea       CHECK (octet_length(fingerprint) = 32),
    -- Wall-clock time of the insert (not the transaction start): conditions
    -- compare it with the creation time of runs.
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (id, project_id),
    -- NULLs are distinct: events without a key never conflict.
    UNIQUE (project_id, idempotency_key),
    FOREIGN KEY (contact_id, project_id) REFERENCES contacts (id, project_id) ON DELETE CASCADE,
    CHECK ((idempotency_key IS NULL) = (fingerprint IS NULL))
);

CREATE INDEX contact_events_condition_idx ON contact_events (contact_id, name, created_at);
CREATE INDEX contact_events_created_idx ON contact_events (created_at);

-- Event rate limit per project: one counter row per project and minute.
CREATE TABLE contact_event_windows (
    project_id   uuid        NOT NULL REFERENCES projects (id),
    window_start timestamptz NOT NULL,
    count        integer     NOT NULL CHECK (count >= 0),
    PRIMARY KEY (project_id, window_start)
);

CREATE INDEX contact_event_windows_start_idx ON contact_event_windows (window_start);

CREATE TABLE automation_runs (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id       uuid        NOT NULL REFERENCES projects (id),
    automation_id    uuid        NOT NULL,
    contact_id       uuid        NOT NULL,
    trigger_event_id uuid,
    status           text        NOT NULL DEFAULT 'active'
                                 CHECK (status IN ('active', 'completed', 'exited', 'canceled', 'failed')),
    reason           text,
    -- Index of the next step to execute.
    step             smallint    NOT NULL DEFAULT 0 CHECK (step BETWEEN 0 AND 20),
    next_step_at     timestamptz,
    -- The trigger event's data for rendering, deleted when the run ends.
    trigger_data     jsonb       CHECK (jsonb_typeof(trigger_data) = 'object' AND octet_length(trigger_data::text) <= 16384),
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    UNIQUE (id, project_id),
    FOREIGN KEY (automation_id, project_id) REFERENCES automations (id, project_id),
    FOREIGN KEY (contact_id, project_id) REFERENCES contacts (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (trigger_event_id, project_id) REFERENCES contact_events (id, project_id) ON DELETE SET NULL (trigger_event_id),
    CHECK ((status = 'active') = (finished_at IS NULL)),
    CHECK ((status = 'active') = (next_step_at IS NOT NULL)),
    CHECK (status = 'active' OR trigger_data IS NULL),
    CHECK (CASE status
        WHEN 'active' THEN reason IS NULL
        WHEN 'completed' THEN reason IS NULL
        WHEN 'exited' THEN reason IN ('condition_not_met', 'unsubscribed', 'suppressed', 'project_disabled')
        WHEN 'canceled' THEN reason IN ('canceled', 'automation_archived')
        WHEN 'failed' THEN reason IN ('render_failed', 'internal_error')
    END)
);

-- At most one active run per automation and contact.
CREATE UNIQUE INDEX automation_runs_active_key ON automation_runs (automation_id, contact_id) WHERE status = 'active';
CREATE INDEX automation_runs_reentry_idx ON automation_runs (automation_id, contact_id);
CREATE INDEX automation_runs_status_idx ON automation_runs (automation_id, status);
CREATE INDEX automation_runs_project_idx ON automation_runs (project_id, automation_id, id DESC);
CREATE INDEX automation_runs_contact_idx ON automation_runs (contact_id);
CREATE INDEX automation_runs_trigger_idx ON automation_runs (trigger_event_id) WHERE trigger_event_id IS NOT NULL;
CREATE INDEX automation_runs_due_idx ON automation_runs (next_step_at) WHERE status = 'active';

ALTER TABLE emails
    ADD COLUMN automation_id uuid,
    ADD COLUMN automation_run_id uuid,
    ADD COLUMN automation_step smallint,
    ADD CONSTRAINT emails_automation_fk FOREIGN KEY (automation_id, project_id) REFERENCES automations (id, project_id),
    ADD CONSTRAINT emails_automation_run_fk FOREIGN KEY (automation_run_id, project_id)
        REFERENCES automation_runs (id, project_id) ON DELETE SET NULL (automation_run_id),
    -- One email per run and step (NULLs are distinct).
    ADD CONSTRAINT emails_automation_run_step_key UNIQUE (automation_run_id, automation_step),
    ADD CONSTRAINT emails_automation_check CHECK (
        (automation_id IS NULL) = (automation_step IS NULL)
        AND (automation_run_id IS NULL OR automation_id IS NOT NULL)
        AND (broadcast_id IS NULL OR automation_id IS NULL));


CREATE INDEX emails_automation_idx ON emails (automation_id) WHERE automation_id IS NOT NULL;

-- The personal List-Unsubscribe URL of an automation email, deleted with
-- the content.
ALTER TABLE email_contents ADD COLUMN list_unsubscribe_url text CHECK (length(list_unsubscribe_url) BETWEEN 1 AND 978);

ALTER TABLE emails DROP CONSTRAINT emails_error_code_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_check CHECK (error_code IN (
    'provider_unreachable', 'provider_timeout', 'provider_protocol_error',
    'provider_deferred', 'provider_throttled', 'provider_rejected', 'provider_auth_failed',
    'sender_not_verified', 'message_invalid', 'unsafe_target',
    'suppressed', 'project_disabled', 'content_missing', 'expired', 'internal_error',
    'unsubscribed', 'contact_deleted', 'broadcast_canceled', 'render_failed', 'automation_canceled'));

-- +goose Down
ALTER TABLE emails DROP CONSTRAINT emails_error_code_check;
ALTER TABLE emails ADD CONSTRAINT emails_error_code_check CHECK (error_code IN (
    'provider_unreachable', 'provider_timeout', 'provider_protocol_error',
    'provider_deferred', 'provider_throttled', 'provider_rejected', 'provider_auth_failed',
    'sender_not_verified', 'message_invalid', 'unsafe_target',
    'suppressed', 'project_disabled', 'content_missing', 'expired', 'internal_error',
    'unsubscribed', 'contact_deleted', 'broadcast_canceled', 'render_failed'));

ALTER TABLE email_contents DROP COLUMN list_unsubscribe_url;

DROP INDEX emails_automation_idx;

ALTER TABLE emails
    DROP CONSTRAINT emails_automation_check,
    DROP CONSTRAINT emails_automation_run_step_key,
    DROP CONSTRAINT emails_automation_run_fk,
    DROP CONSTRAINT emails_automation_fk,
    DROP COLUMN automation_step,
    DROP COLUMN automation_run_id,
    DROP COLUMN automation_id;

DROP TABLE automation_runs;
DROP TABLE contact_event_windows;
DROP TABLE contact_events;
