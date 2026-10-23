-- Daily usage per project: what happened on each UTC day (emails accepted,
-- handed to the provider, failed, cancelled, delivered, bounced,
-- complained). The worker recomputes the most recent days from emails and
-- their events; older days stay as computed, so they survive the deletion of
-- the underlying rows by retention.

-- +goose Up
CREATE TABLE usage_daily (
    project_id  uuid        NOT NULL REFERENCES projects (id),
    day         date        NOT NULL,
    accepted    bigint      NOT NULL DEFAULT 0 CHECK (accepted >= 0),
    sent        bigint      NOT NULL DEFAULT 0 CHECK (sent >= 0),
    failed      bigint      NOT NULL DEFAULT 0 CHECK (failed >= 0),
    canceled    bigint      NOT NULL DEFAULT 0 CHECK (canceled >= 0),
    delivered   bigint      NOT NULL DEFAULT 0 CHECK (delivered >= 0),
    bounced     bigint      NOT NULL DEFAULT 0 CHECK (bounced >= 0),
    complained  bigint      NOT NULL DEFAULT 0 CHECK (complained >= 0),
    computed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, day)
);

-- +goose Down
DROP TABLE usage_daily;
