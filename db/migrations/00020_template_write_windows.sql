-- Per-project, per-minute counter of template writes (create and publish
-- attempts), separate from the send rate limit.

-- +goose Up
CREATE TABLE template_write_windows (
    project_id   uuid        NOT NULL REFERENCES projects (id),
    window_start timestamptz NOT NULL,
    count        integer     NOT NULL CHECK (count >= 0),
    PRIMARY KEY (project_id, window_start)
);

CREATE INDEX template_write_windows_start_idx ON template_write_windows (window_start);

-- +goose Down
DROP TABLE template_write_windows;
