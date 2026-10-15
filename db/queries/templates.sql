-- name: LockProjectForTemplates :one
-- Serialises template creation per project so the template cap holds. NO KEY
-- UPDATE does not block the KEY SHARE locks of email inserts.
SELECT id FROM projects WHERE id = $1 FOR NO KEY UPDATE;

-- name: CountTemplates :one
SELECT count(*) FROM templates WHERE project_id = $1;

-- name: InsertTemplate :one
INSERT INTO templates (project_id, slug, name) VALUES ($1, $2, $3)
RETURNING *;

-- name: GetTemplate :one
SELECT * FROM templates WHERE id = $1 AND project_id = $2;

-- name: TemplateStorageBytes :one
-- Bytes of template source and schemas a project stores (all versions).
SELECT coalesce(sum(octet_length(subject) + octet_length(text_body) + coalesce(octet_length(html_body), 0)
                    + octet_length(variables::text)), 0)::bigint
FROM template_versions WHERE project_id = $1;

-- name: NextTemplateVersion :one
-- Takes the template row lock, so concurrent publishers number their
-- versions without gaps; refuses beyond the version cap.
UPDATE templates SET latest_version = latest_version + 1
WHERE id = $1 AND project_id = $2 AND latest_version < sqlc.arg(max_versions)::integer
RETURNING latest_version;

-- name: InsertTemplateVersion :one
INSERT INTO template_versions (template_id, project_id, version, subject, text_body, html_body, variables)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetTemplateVersion :one
SELECT * FROM template_versions WHERE template_id = $1 AND project_id = $2 AND version = $3;

-- name: ListTemplateVersions :many
SELECT version, created_at FROM template_versions
WHERE template_id = $1 AND project_id = $2
ORDER BY version DESC;
