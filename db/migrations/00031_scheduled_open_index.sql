-- Planned emails that are still waiting, per project (the cap on open
-- scheduled sends counts them).

-- +goose Up
CREATE INDEX emails_project_scheduled_idx ON emails (project_id)
    WHERE status = 'queued' AND scheduled_at IS NOT NULL;

-- +goose Down
DROP INDEX emails_project_scheduled_idx;
