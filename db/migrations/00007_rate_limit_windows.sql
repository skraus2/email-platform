-- Send rate limit per project: one counter row per project and minute.
-- The send transaction increments the row of the current minute and rolls
-- back when the limit is exceeded, so refused requests do not count.

-- +goose Up
CREATE TABLE rate_limit_windows (
    project_id   uuid        NOT NULL REFERENCES projects (id),
    window_start timestamptz NOT NULL,
    count        integer     NOT NULL CHECK (count >= 0),
    PRIMARY KEY (project_id, window_start)
);

CREATE INDEX rate_limit_windows_start_idx ON rate_limit_windows (window_start);

-- +goose Down
DROP TABLE rate_limit_windows;
