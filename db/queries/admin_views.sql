-- Read model of the operator console: every project, each row carrying its
-- project_id. Lists page by id (UUIDv7, newest first) with an unconditional
-- bound (uuid.Max for the first page).

-- name: AdminListProjects :many
SELECT * FROM projects WHERE id < sqlc.arg(before_id)::uuid ORDER BY id DESC LIMIT sqlc.arg(row_limit);

-- name: AdminSearchEmails :many
SELECT sqlc.embed(e), p.name AS project_name
FROM emails e JOIN projects p ON p.id = e.project_id
WHERE e.id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(project_id)::uuid IS NULL OR e.project_id = sqlc.narg(project_id)::uuid)
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR e.status = ANY (sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(error_code)::text IS NULL OR e.error_code = sqlc.narg(error_code)::text)
  AND (sqlc.narg(recipient)::text IS NULL OR lower(e.recipient) = lower(sqlc.narg(recipient)::text))
  AND (sqlc.narg(template_id)::uuid IS NULL OR e.template_id = sqlc.narg(template_id)::uuid)
  AND (sqlc.narg(broadcast_id)::uuid IS NULL OR e.broadcast_id = sqlc.narg(broadcast_id)::uuid)
  AND (sqlc.narg(created_after)::timestamptz IS NULL OR e.created_at >= sqlc.narg(created_after)::timestamptz)
  AND (sqlc.narg(created_before)::timestamptz IS NULL OR e.created_at < sqlc.narg(created_before)::timestamptz)
  AND (sqlc.narg(email_id)::uuid IS NULL OR e.id = sqlc.narg(email_id)::uuid)
  AND (sqlc.narg(text_query)::text IS NULL OR e.provider_id = sqlc.narg(text_query)::text
       OR e.provider_id = sqlc.narg(text_query_alt)::text
       OR lower(e.recipient) = lower(sqlc.narg(text_query)::text))
ORDER BY e.id DESC
LIMIT sqlc.arg(row_limit);

-- name: AdminGetEmail :one
SELECT sqlc.embed(e), p.name AS project_name
FROM emails e JOIN projects p ON p.id = e.project_id
WHERE e.id = $1;

-- name: AdminEmailDeliveries :many
-- Webhook deliveries of the events of one email, oldest first.
SELECT d.id, d.endpoint_id, w.url AS endpoint_url, d.event_id, ev.type AS event_type, d.status, d.attempts,
       d.last_status_code, d.last_error_code, d.next_attempt_at, d.delivered_at, d.created_at, d.updated_at
FROM webhook_deliveries d
JOIN email_events ev ON ev.id = d.event_id AND ev.project_id = d.project_id
JOIN webhook_endpoints w ON w.id = d.endpoint_id AND w.project_id = d.project_id
WHERE ev.email_id = sqlc.arg(email_id) AND d.project_id = sqlc.arg(project_id) AND d.id > sqlc.arg(after_id)::uuid
ORDER BY d.id
LIMIT sqlc.arg(row_limit);

-- name: AdminListTemplates :many
SELECT * FROM templates
WHERE id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: AdminGetTemplate :one
SELECT * FROM templates WHERE id = $1;

-- name: AdminListSenders :many
SELECT * FROM sender_identities
WHERE id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: AdminListWebhookEndpoints :many
-- Never the secret.
SELECT id, project_id, url, event_types, status, created_at, updated_at FROM webhook_endpoints
WHERE id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: AdminListBroadcasts :many
SELECT sqlc.embed(b), p.name AS project_name
FROM broadcasts b JOIN projects p ON p.id = b.project_id
WHERE b.id < sqlc.arg(before_id)::uuid
  AND (sqlc.narg(project_id)::uuid IS NULL OR b.project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(status)::text IS NULL OR b.status = sqlc.narg(status)::text)
ORDER BY b.id DESC
LIMIT sqlc.arg(row_limit);

-- name: AdminGetBroadcast :one
SELECT sqlc.embed(b), p.name AS project_name
FROM broadcasts b JOIN projects p ON p.id = b.project_id
WHERE b.id = $1;
