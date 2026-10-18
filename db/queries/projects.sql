-- name: CreateProject :one
INSERT INTO projects (name) VALUES ($1)
RETURNING *;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY created_at;

-- name: SetProjectStatus :execrows
UPDATE projects SET status = $2 WHERE id = $1;

-- name: CreateAPIKey :one
INSERT INTO api_keys (project_id, prefix, secret_hash, scopes)
VALUES ($1, $2, $3, $4)
RETURNING id, project_id, prefix, scopes, created_at, revoked_at;

-- name: ListAPIKeys :many
SELECT id, project_id, prefix, scopes, created_at, revoked_at
FROM api_keys WHERE project_id = $1 ORDER BY created_at;

-- name: RevokeAPIKey :execrows
UPDATE api_keys SET revoked_at = now() WHERE prefix = $1 AND revoked_at IS NULL;

-- name: GetActiveKeyByPrefix :one
-- Returns a key only if it is not revoked and its project is active.
SELECT k.id, k.project_id, k.secret_hash, k.scopes
FROM api_keys k
JOIN projects p ON p.id = k.project_id
WHERE k.prefix = $1 AND k.revoked_at IS NULL AND p.status = 'active';

-- name: ProjectExists :one
SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1);
