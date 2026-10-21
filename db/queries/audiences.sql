-- name: CountAudiences :one
SELECT count(*) FROM audiences WHERE project_id = $1;

-- name: InsertAudience :one
INSERT INTO audiences (project_id, name) VALUES ($1, $2) RETURNING *;

-- name: GetAudience :one
SELECT * FROM audiences WHERE id = $1 AND project_id = $2;

-- name: ListAudiences :many
-- One page of a project's audiences, newest first; before_id is the cursor
-- (the maximal UUID for the first page).
SELECT * FROM audiences
WHERE project_id = sqlc.arg(project_id) AND id < sqlc.arg(before_id)::uuid
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: LockAudience :one
-- Serialises membership adds per audience so the member cap holds. NO KEY
-- UPDATE does not block the KEY SHARE locks of member inserts.
SELECT id FROM audiences WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE;

-- name: CountAudienceMembers :one
-- Callers have checked that the audience belongs to the project; the count
-- uses the primary key index alone.
SELECT count(*) FROM audience_members WHERE audience_id = $1;

-- name: CountProjectContactsIn :one
-- How many of the ids are contacts of the project.
SELECT count(*) FROM contacts WHERE project_id = $1 AND id = ANY (sqlc.arg(ids)::uuid[]);

-- name: CountAudienceMembersIn :one
-- How many of the ids already are members of the audience (callers have
-- checked that the audience belongs to the project).
SELECT count(*) FROM audience_members
WHERE audience_id = $1 AND contact_id = ANY (sqlc.arg(ids)::uuid[]);

-- name: AddAudienceMembers :execrows
-- Adds the contacts of the project among ids; existing members stay.
INSERT INTO audience_members (project_id, audience_id, contact_id)
SELECT c.project_id, sqlc.arg(audience_id)::uuid, c.id
FROM contacts c
WHERE c.project_id = sqlc.arg(project_id) AND c.id = ANY (sqlc.arg(ids)::uuid[])
ON CONFLICT DO NOTHING;

-- name: RemoveAudienceMember :exec
DELETE FROM audience_members WHERE audience_id = $1 AND contact_id = $2 AND project_id = $3;

-- name: ListAudienceContacts :many
-- One page of an audience's member contacts by contact id; after_id is the
-- cursor (the nil UUID for the first page).
SELECT c.id, c.project_id, c.email, c.status, c.consent_source, c.status_changed_at, c.attributes, c.created_at, c.updated_at
FROM audience_members m
JOIN contacts c ON c.id = m.contact_id AND c.project_id = m.project_id
WHERE m.audience_id = sqlc.arg(audience_id) AND m.project_id = sqlc.arg(project_id)
  AND m.contact_id > sqlc.arg(after_id)::uuid
ORDER BY m.contact_id
LIMIT sqlc.arg(row_limit);
