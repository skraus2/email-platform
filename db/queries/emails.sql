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
       p.status AS project_status
FROM emails e
JOIN projects p ON p.id = e.project_id
LEFT JOIN email_contents c ON c.email_id = e.id AND c.project_id = e.project_id
WHERE e.id = $1 AND e.project_id = $2;

-- name: MarkSending :execrows
UPDATE emails SET status = 'sending', attempts = attempts + 1, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status IN ('queued', 'sending');

-- name: MarkDispatched :execrows
UPDATE emails
SET status = $3, provider = $4, provider_id = $5, last_error = NULL,
    finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'sending';

-- name: RequeueAfterError :exec
-- The provider certainly did not accept the message (connect failure or SMTP
-- rejection): back to queued, so the retry is not flagged as a duplicate.
UPDATE emails SET status = 'queued', last_error = $3, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'sending';

-- name: RecordAttemptError :exec
-- Outcome unknown (timeout/protocol error mid-send): stay "sending", so the
-- retry logs a possible duplicate.
UPDATE emails SET last_error = $3, updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'sending';

-- name: MarkFailed :execrows
UPDATE emails
SET status = 'failed', last_error = $3, finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status IN ('queued', 'sending');

-- name: DeleteEmailContent :exec
DELETE FROM email_contents WHERE email_id = $1 AND project_id = $2;

-- name: ExpireOpenEmails :execrows
-- Safety net: emails stuck before a terminal state past the retention limit.
UPDATE emails
SET status = 'failed', last_error = 'expired', finished_at = now(), updated_at = now()
WHERE status IN ('queued', 'sending') AND created_at < $1;

-- name: DeleteContentsBefore :execrows
DELETE FROM email_contents WHERE created_at < $1;
