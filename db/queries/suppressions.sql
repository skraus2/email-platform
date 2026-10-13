-- name: AddSuppression :execrows
-- Zero rows: the address is already suppressed in this project.
INSERT INTO suppressions (project_id, recipient, reason)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: ListSuppressions :many
SELECT * FROM suppressions WHERE project_id = $1 ORDER BY created_at DESC, id DESC;

-- name: RemoveSuppression :execrows
DELETE FROM suppressions WHERE project_id = $1 AND lower(recipient) = lower(sqlc.arg(recipient));

-- name: IsSuppressed :one
SELECT EXISTS (
    SELECT 1 FROM suppressions WHERE project_id = $1 AND lower(recipient) = lower(sqlc.arg(recipient))
)::boolean;
