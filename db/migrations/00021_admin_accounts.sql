-- Local operator accounts for the dashboard and their server-side sessions.
-- Platform-level data, not tenant data: no project_id.

-- +goose Up
CREATE TABLE admin_users (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    username            text        NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9._-]{2,63}$'),
    password_hash       text        NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    disabled_at         timestamptz,
    failed_logins       integer     NOT NULL DEFAULT 0 CHECK (failed_logins >= 0),
    locked_until        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    password_changed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE admin_sessions (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    admin_user_id uuid        NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    token_hash    bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    CHECK (expires_at > created_at)
);

CREATE INDEX admin_sessions_user_idx ON admin_sessions (admin_user_id);
CREATE INDEX admin_sessions_expires_idx ON admin_sessions (expires_at);

-- +goose Down
DROP TABLE admin_sessions;
DROP TABLE admin_users;
