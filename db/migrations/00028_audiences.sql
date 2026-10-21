-- Audiences: named lists of contacts of one project. A membership joins a
-- contact and an audience of the same project and disappears with either.

-- +goose Up
CREATE TABLE audiences (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid        NOT NULL REFERENCES projects (id),
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, project_id)
);

CREATE UNIQUE INDEX audiences_project_name_idx ON audiences (project_id, lower(name));
CREATE INDEX audiences_project_idx ON audiences (project_id, id DESC);

CREATE TABLE audience_members (
    project_id  uuid        NOT NULL,
    audience_id uuid        NOT NULL,
    contact_id  uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (audience_id, contact_id),
    FOREIGN KEY (audience_id, project_id) REFERENCES audiences (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (contact_id, project_id) REFERENCES contacts (id, project_id) ON DELETE CASCADE
);

CREATE INDEX audience_members_contact_idx ON audience_members (contact_id);

-- Contact lists filtered by consent status.
CREATE INDEX contacts_project_status_idx ON contacts (project_id, status, id DESC);

-- +goose Down
DROP INDEX contacts_project_status_idx;
DROP TABLE audience_members;
DROP TABLE audiences;
