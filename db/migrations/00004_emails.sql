-- Send requests. One row per recipient. The message content lives in its own
-- table so it can be deleted as soon as the email reaches a terminal state,
-- while status and metadata stay.

-- +goose Up
CREATE TABLE emails (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id  uuid        NOT NULL REFERENCES projects (id),
    sender      text        NOT NULL CHECK (length(sender) BETWEEN 3 AND 254),
    recipient   text        NOT NULL CHECK (length(recipient) BETWEEN 3 AND 254),
    subject     text        NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 255),
    status      text        NOT NULL DEFAULT 'queued'
                            CHECK (status IN ('queued', 'sending', 'captured', 'submitted', 'failed')),
    provider    text,
    provider_id text,
    attempts    integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error  text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE (id, project_id),
    -- A terminal state always has its finish time; a non-terminal one never.
    CHECK ((status IN ('captured', 'submitted', 'failed')) = (finished_at IS NOT NULL))
);

CREATE INDEX emails_project_created_idx ON emails (project_id, created_at DESC);
CREATE INDEX emails_open_created_idx ON emails (created_at) WHERE status IN ('queued', 'sending');

CREATE TABLE email_contents (
    email_id   uuid        PRIMARY KEY,
    project_id uuid        NOT NULL,
    text_body  text,
    html_body  text,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (email_id, project_id) REFERENCES emails (id, project_id) ON DELETE CASCADE,
    CHECK (coalesce(text_body, '') <> '' OR coalesce(html_body, '') <> '')
);

CREATE INDEX email_contents_created_idx ON email_contents (created_at);

-- +goose Down
DROP TABLE email_contents;
DROP TABLE emails;
