-- name: InsertEmail :one
INSERT INTO emails (project_id, sender, recipient, subject)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: InsertEmailContent :exec
INSERT INTO email_contents (email_id, project_id, text_body, html_body)
VALUES ($1, $2, $3, $4);

-- name: GetEmail :one
SELECT * FROM emails WHERE id = $1 AND project_id = $2;

-- name: GetEmailForDispatch :one
-- Content columns are NULL once the content has been deleted.
SELECT e.id, e.project_id, e.sender, e.recipient, e.subject, e.status,
       c.text_body, c.html_body, (c.email_id IS NOT NULL)::boolean AS has_content,
       p.status AS project_status,
       EXISTS (SELECT 1 FROM suppressions s
               WHERE s.project_id = e.project_id AND lower(s.recipient) = lower(e.recipient))::boolean AS suppressed,
       EXISTS (SELECT 1 FROM sender_identities si
               WHERE si.project_id = e.project_id AND si.status = 'verified'
                 AND ((si.kind = 'address' AND si.value = lower(e.sender))
                      OR (si.kind = 'domain' AND si.value = lower(substring(e.sender FROM '@([^@]+)$')))))::boolean AS sender_verified
FROM emails e
JOIN projects p ON p.id = e.project_id
LEFT JOIN email_contents c ON c.email_id = e.id AND c.project_id = e.project_id
WHERE e.id = $1 AND e.project_id = $2;

-- name: MarkSending :execrows
-- Claims the email for a send attempt. Project status, suppression and (when
-- required) a verified sender identity of the project for the exact address
-- or its exact domain are checked as part of the claim, so a change made
-- before this point is always honoured.
UPDATE emails SET status = 'sending', attempts = attempts + 1, updated_at = now()
WHERE emails.id = sqlc.arg(id) AND emails.project_id = sqlc.arg(project_id) AND emails.status IN ('queued', 'sending')
  AND EXISTS (SELECT 1 FROM projects p WHERE p.id = emails.project_id AND p.status = 'active')
  AND NOT EXISTS (SELECT 1 FROM suppressions s
                  WHERE s.project_id = emails.project_id AND lower(s.recipient) = lower(emails.recipient))
  AND (NOT sqlc.arg(require_verified_sender)::boolean
       OR EXISTS (SELECT 1 FROM sender_identities si
                  WHERE si.project_id = emails.project_id AND si.status = 'verified'
                    AND ((si.kind = 'address' AND si.value = lower(emails.sender))
                         OR (si.kind = 'domain' AND si.value = lower(substring(emails.sender FROM '@([^@]+)$'))))));

-- name: MarkDispatched :execrows
UPDATE emails
SET status = $3, provider = $4, provider_id = $5, last_error = NULL, error_code = NULL,
    finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'sending';

-- name: RequeueAfterError :exec
-- The provider certainly did not accept the message (connect failure, SMTP
-- rejection or API refusal): back to queued, so the retry is not flagged as a
-- duplicate.
UPDATE emails SET status = 'queued', last_error = $3, error_code = $4, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'sending';

-- name: RecordAttemptError :exec
-- Outcome unknown (timeout/protocol error mid-send): stay "sending", so the
-- retry logs a possible duplicate.
UPDATE emails SET last_error = $3, error_code = $4, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'sending';

-- name: MarkFailed :execrows
UPDATE emails
SET status = 'failed', last_error = $3, error_code = $4, finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status IN ('queued', 'sending');

-- name: MarkQueuedFailed :execrows
-- Like MarkFailed, but only for an email no attempt can have sent yet.
UPDATE emails
SET status = 'failed', last_error = $3, error_code = $4, finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'queued';

-- name: DeleteEmailContent :exec
DELETE FROM email_contents WHERE email_id = $1 AND project_id = $2;

-- name: ExpireOpenEmails :many
-- Safety net: emails stuck before a terminal state past the retention limit.
UPDATE emails
SET status = 'failed', last_error = 'expired', error_code = 'expired', finished_at = now(), updated_at = now()
WHERE status IN ('queued', 'sending') AND created_at < $1
RETURNING id, project_id;

-- name: DeleteContentsBefore :execrows
DELETE FROM email_contents WHERE created_at < $1;

-- name: ListEmails :many
-- Admin failure inspection: newest first, optionally one status only.
SELECT * FROM emails
WHERE project_id = $1 AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: ListProjectEmails :many
-- One page of a project's emails, newest first (ids are UUIDv7). The nil
-- UUID as cursor means "from the newest"; an empty status list means any.
SELECT * FROM emails
WHERE project_id = sqlc.arg(project_id)
  AND (sqlc.arg(after)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR id < sqlc.arg(after)::uuid)
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR status = ANY (sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(recipient)::text IS NULL OR lower(recipient) = lower(sqlc.narg(recipient)::text))
  AND (sqlc.narg(created_after)::timestamptz IS NULL OR created_at >= sqlc.narg(created_after)::timestamptz)
  AND (sqlc.narg(created_before)::timestamptz IS NULL OR created_at < sqlc.narg(created_before)::timestamptz)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountProviderHandoffs :one
-- Messages handed to the provider within the last 24 hours (daily cap).
SELECT count(*) FROM emails
WHERE provider = $1 AND finished_at > now() - interval '24 hours';

-- name: HandoffLeavesWindowIn :one
-- Seconds until the hand-off at position $2 (0 = oldest) of the last 24 hours
-- leaves the window, freeing a slot under the daily cap.
SELECT extract(epoch FROM (finished_at + interval '24 hours' - now()))::float8 AS seconds
FROM emails
WHERE provider = $1 AND finished_at > now() - interval '24 hours'
ORDER BY finished_at
OFFSET $2 LIMIT 1;
