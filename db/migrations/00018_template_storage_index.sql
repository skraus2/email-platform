-- Publishing checks the template storage a project uses.

-- +goose Up
CREATE INDEX template_versions_project_idx ON template_versions (project_id);

-- +goose Down
DROP INDEX template_versions_project_idx;
