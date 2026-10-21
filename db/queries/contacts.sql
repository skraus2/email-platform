-- name: SetContactLockTimeout :exec
-- Bounds the wait for the project row lock in this transaction.
SET LOCAL lock_timeout = '3s';

-- name: SetContactStatementTimeout :exec
-- Bounds each statement (the cap count) while the project row is locked.
SET LOCAL statement_timeout = '5s';

-- name: LockProjectForContacts :one
-- Serialises contact creation per project so the contact cap holds. NO KEY
-- UPDATE does not block the KEY SHARE locks of other inserts.
SELECT id FROM projects WHERE id = $1 FOR NO KEY UPDATE;

-- name: CountContacts :one
SELECT count(*) FROM contacts WHERE project_id = $1;

-- name: InsertContact :one
INSERT INTO contacts (project_id, email, status, consent_source, attributes, unsubscribe_token)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, project_id, email, status, consent_source, status_changed_at, attributes, created_at, updated_at;

-- name: GetContact :one
-- Contact queries of the API never read the unsubscribe token.
SELECT id, project_id, email, status, consent_source, status_changed_at, attributes, created_at, updated_at FROM contacts WHERE id = $1 AND project_id = $2;

-- name: UpdateContact :one
-- Null arguments keep the stored value. status_changed_at moves only when
-- the status actually changes.
UPDATE contacts
SET status            = coalesce(sqlc.narg(status)::text, status),
    consent_source    = coalesce(sqlc.narg(consent_source)::text, consent_source),
    attributes        = coalesce(sqlc.narg(attributes)::jsonb, attributes),
    status_changed_at = CASE WHEN sqlc.narg(status)::text IS NOT NULL AND sqlc.narg(status)::text <> status
                             THEN now() ELSE status_changed_at END,
    updated_at        = now()
WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id)
RETURNING id, project_id, email, status, consent_source, status_changed_at, attributes, created_at, updated_at;

-- name: DeleteContact :execrows
DELETE FROM contacts WHERE id = $1 AND project_id = $2;

-- name: ListContacts :many
-- One page of a project's contacts, newest first (ids are UUIDv7);
-- before_id is the cursor (the maximal UUID for the first page).
SELECT id, project_id, email, status, consent_source, status_changed_at, attributes, created_at, updated_at FROM contacts
WHERE project_id = sqlc.arg(project_id)
  AND id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListContactsByEmail :many
-- ListContacts for one address (case-insensitive): at most one row.
SELECT id, project_id, email, status, consent_source, status_changed_at, attributes, created_at, updated_at FROM contacts
WHERE project_id = sqlc.arg(project_id)
  AND lower(email) = lower(sqlc.arg(email)::text)
  AND id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: UnsubscribeByToken :one
-- Marks the contact with this unsubscribe token unsubscribed; no row when
-- the token is unknown in the project or the contact already unsubscribed.
UPDATE contacts
SET status = 'unsubscribed', status_changed_at = now(), updated_at = now()
WHERE project_id = $1 AND unsubscribe_token = $2 AND status <> 'unsubscribed'
RETURNING id;

-- name: ContactIDByToken :one
SELECT id FROM contacts WHERE project_id = $1 AND unsubscribe_token = $2;
