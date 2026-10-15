-- name: CreateAdminUser :one
INSERT INTO admin_users (username, password_hash) VALUES ($1, $2)
RETURNING *;

-- name: GetAdminUserByUsername :one
-- locked and the seconds left come from the database clock, like the lock.
SELECT sqlc.embed(admin_users),
       coalesce(locked_until > now(), false)::boolean AS locked,
       coalesce(ceil(extract(epoch FROM locked_until - now())), 0)::integer AS lock_seconds
FROM admin_users WHERE username = $1;

-- name: ListAdminUsers :many
SELECT * FROM admin_users ORDER BY username;

-- name: SetAdminUserDisabled :execrows
-- Disabling also clears the lock; enabling starts with a clean counter.
UPDATE admin_users
SET disabled_at = CASE WHEN sqlc.arg(disabled)::boolean THEN coalesce(disabled_at, now()) END,
    failed_logins = 0, locked_until = NULL
WHERE username = sqlc.arg(username);

-- name: SetAdminPassword :execrows
UPDATE admin_users
SET password_hash = $2, password_changed_at = now(), failed_logins = 0, locked_until = NULL
WHERE username = $1;

-- name: RehashAdminPassword :exec
-- Upgrades the hash parameters after a successful login; the password itself
-- is unchanged, so sessions stay.
UPDATE admin_users SET password_hash = $2 WHERE id = $1 AND password_hash = sqlc.arg(old_hash);

-- name: UnlockAdminUser :execrows
UPDATE admin_users SET failed_logins = 0, locked_until = NULL WHERE username = $1;

-- name: RecordAdminLoginFailure :one
-- One more consecutive failure; from the fifth on, the account is locked for
-- 1, 2, 4 … minutes, at most 15.
UPDATE admin_users
SET failed_logins = failed_logins + 1,
    locked_until = CASE WHEN failed_logins + 1 >= 5
        THEN now() + least(interval '1 minute' * power(2, least(failed_logins + 1 - 5, 10)), interval '15 minutes')
        ELSE locked_until END
WHERE id = $1
RETURNING failed_logins,
    coalesce(ceil(extract(epoch FROM locked_until - now())), 0)::integer AS lock_seconds;

-- name: RecordAdminLoginSuccess :exec
UPDATE admin_users SET failed_logins = 0, locked_until = NULL WHERE id = $1;

-- name: InsertAdminSession :one
INSERT INTO admin_sessions (admin_user_id, token_hash, expires_at) VALUES ($1, $2, $3)
RETURNING *;

-- name: GetAdminSession :one
-- A live session of an enabled user: not past its absolute expiry and used
-- within the idle timeout.
SELECT s.id, s.admin_user_id, s.last_seen_at, s.expires_at, u.username
FROM admin_sessions s JOIN admin_users u ON u.id = s.admin_user_id
WHERE s.token_hash = $1 AND u.disabled_at IS NULL
  AND s.expires_at > now() AND s.last_seen_at > now() - make_interval(secs => sqlc.arg(idle_seconds)::float8);

-- name: TouchAdminSession :exec
-- Slides the idle timeout, at most once a minute per session.
UPDATE admin_sessions SET last_seen_at = now()
WHERE id = $1 AND last_seen_at < now() - interval '1 minute';

-- name: DeleteAdminSession :execrows
DELETE FROM admin_sessions WHERE token_hash = $1;

-- name: DeleteAdminSessionsOfUser :execrows
DELETE FROM admin_sessions WHERE admin_user_id = (SELECT id FROM admin_users WHERE username = $1);
