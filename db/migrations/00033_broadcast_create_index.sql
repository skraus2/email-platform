-- Recent broadcast creates of a project (the hourly create limit).

-- +goose Up
CREATE INDEX broadcasts_project_created_idx ON broadcasts (project_id, created_at);

-- +goose Down
DROP INDEX broadcasts_project_created_idx;
