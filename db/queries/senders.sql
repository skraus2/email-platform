-- name: AddSenderIdentity :one
INSERT INTO sender_identities (project_id, kind, value)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: VerifySenderIdentity :execrows
UPDATE sender_identities SET status = 'verified', verified_at = now()
WHERE id = $1 AND status = 'pending';

-- name: RemoveSenderIdentity :execrows
DELETE FROM sender_identities WHERE id = $1;

-- name: ListSenderIdentities :many
SELECT * FROM sender_identities WHERE project_id = $1 ORDER BY value LIMIT $2;

-- name: IsSenderVerified :one
-- A verified identity of the project for the exact address or its exact
-- domain (no subdomain match).
SELECT EXISTS (
    SELECT 1 FROM sender_identities
    WHERE project_id = $1 AND status = 'verified'
      AND ((kind = 'address' AND value = lower(sqlc.arg(address)))
           OR (kind = 'domain' AND value = lower(sqlc.arg(domain))))
)::boolean;
