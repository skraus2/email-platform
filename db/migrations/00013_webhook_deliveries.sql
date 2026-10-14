-- One row per (endpoint, event): the delivery state of a webhook
-- notification, visible to operators. Attempts are counted here; the job
-- queue only schedules them.

-- +goose Up
CREATE TABLE webhook_deliveries (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id       uuid        NOT NULL,
    endpoint_id      uuid        NOT NULL,
    event_id         uuid        NOT NULL,
    status           text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts         integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_status_code integer     CHECK (last_status_code BETWEEN 100 AND 599),
    last_error_code  text        CHECK (last_error_code ~ '^[a-z0-9_]{1,40}$'),
    next_attempt_at  timestamptz,
    delivered_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (endpoint_id, event_id),
    FOREIGN KEY (endpoint_id, project_id) REFERENCES webhook_endpoints (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (event_id, project_id) REFERENCES email_events (id, project_id) ON DELETE CASCADE,
    CHECK ((status = 'delivered') = (delivered_at IS NOT NULL)),
    CHECK (status <> 'failed' OR last_error_code IS NOT NULL)
);

CREATE INDEX webhook_deliveries_project_idx ON webhook_deliveries (project_id, created_at DESC);
CREATE INDEX webhook_deliveries_pending_idx ON webhook_deliveries (created_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE webhook_deliveries;
