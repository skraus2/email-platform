-- Listing a project's emails pages by id (UUIDv7, so newest first) and can
-- filter by exact recipient.

-- +goose Up
CREATE INDEX emails_project_id_idx ON emails (project_id, id DESC);
CREATE INDEX emails_project_recipient_idx ON emails (project_id, lower(recipient), id DESC);

-- +goose Down
DROP INDEX emails_project_recipient_idx;
DROP INDEX emails_project_id_idx;
