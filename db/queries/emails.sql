-- name: InsertEmail :one
INSERT INTO emails (project_id, sender, recipient, subject, template_id, template_version, scheduled_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: InsertEmailContent :exec
INSERT INTO email_contents (email_id, project_id, text_body, html_body)
VALUES ($1, $2, $3, $4);

-- name: GetEmail :one
SELECT * FROM emails WHERE id = $1 AND project_id = $2;

-- name: GetEmailForDispatch :one
-- Content columns are NULL once the content has been deleted. A broadcast
-- email has no content: it carries its broadcast's template, shared
-- variables and unsubscribe URL, and its contact's current state (with the
-- unsubscribe token for the link).
SELECT e.id, e.project_id, e.sender, e.recipient, e.subject, e.status,
       c.text_body, c.html_body, (c.email_id IS NOT NULL)::boolean AS has_content,
       e.broadcast_id, e.template_id, e.template_version,
       b.status AS broadcast_status, b.variables AS broadcast_variables, b.unsubscribe_url AS broadcast_unsubscribe_url,
       (ct.id IS NOT NULL)::boolean AS contact_exists, ct.status AS contact_status,
       ct.attributes AS contact_attributes, ct.unsubscribe_token AS contact_token,
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
LEFT JOIN broadcasts b ON b.id = e.broadcast_id AND b.project_id = e.project_id
LEFT JOIN contacts ct ON ct.id = e.contact_id AND ct.project_id = e.project_id
WHERE e.id = $1 AND e.project_id = $2;

-- name: MarkSending :execrows
-- Claims the email for a send attempt. Project status, suppression, (when
-- required) a verified sender identity of the project for the exact address
-- or its exact domain, and for broadcast emails the broadcast and contact
-- state are checked as part of the claim, so a change made before this
-- point is always honoured.
UPDATE emails SET status = 'sending', attempts = attempts + 1, updated_at = now()
WHERE emails.id = sqlc.arg(id) AND emails.project_id = sqlc.arg(project_id) AND emails.status IN ('queued', 'sending')
  AND EXISTS (SELECT 1 FROM projects p WHERE p.id = emails.project_id AND p.status = 'active')
  AND NOT EXISTS (SELECT 1 FROM suppressions s
                  WHERE s.project_id = emails.project_id AND lower(s.recipient) = lower(emails.recipient))
  AND (NOT sqlc.arg(require_verified_sender)::boolean
       OR EXISTS (SELECT 1 FROM sender_identities si
                  WHERE si.project_id = emails.project_id AND si.status = 'verified'
                    AND ((si.kind = 'address' AND si.value = lower(emails.sender))
                         OR (si.kind = 'domain' AND si.value = lower(substring(emails.sender FROM '@([^@]+)$'))))))
  -- A broadcast email: its broadcast is not cancelled and its contact still
  -- exists and is subscribed.
  AND (emails.broadcast_id IS NULL
       OR (EXISTS (SELECT 1 FROM broadcasts b
                   WHERE b.id = emails.broadcast_id AND b.project_id = emails.project_id AND b.status <> 'canceled')
           AND EXISTS (SELECT 1 FROM contacts ct
                       WHERE ct.id = emails.contact_id AND ct.project_id = emails.project_id AND ct.status = 'subscribed')));

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
-- Safety net: emails stuck before a terminal state past the retention limit,
-- counted from when they were due.
UPDATE emails
SET status = 'failed', last_error = 'expired', error_code = 'expired', finished_at = now(), updated_at = now()
WHERE status IN ('queued', 'sending') AND coalesce(scheduled_at, created_at) < sqlc.arg(cutoff)::timestamptz
RETURNING id, project_id;

-- name: DeleteContentsBefore :execrows
-- Safety net: content older than the retention limit, counted from when its
-- email was due (a scheduled email keeps its content until then).
DELETE FROM email_contents c
USING emails e
WHERE e.id = c.email_id AND e.project_id = c.project_id
  AND c.created_at < sqlc.arg(cutoff)::timestamptz AND coalesce(e.scheduled_at, e.created_at) < sqlc.arg(cutoff)::timestamptz;

-- name: CancelEmail :one
-- Cancels an email that no attempt has claimed yet. A queued email may carry
-- the error of a failed earlier attempt; a cancelled one never failed.
UPDATE emails SET status = 'canceled', error_code = NULL, last_error = NULL, finished_at = now(), updated_at = now()
WHERE id = $1 AND project_id = $2 AND status = 'queued'
RETURNING *;

-- name: ListEmails :many
-- Admin failure inspection: newest first, optionally one status only.
SELECT * FROM emails
WHERE project_id = $1 AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: ListProjectEmails :many
-- One page of a project's emails, newest first (ids are UUIDv7). The id
-- bounds are unconditional so that even a generic plan bounds the index
-- scan: before_id is the cursor (the maximal UUID for the first page),
-- min_id the smallest id a created_after filter can match (the nil UUID
-- without one). An empty status list means any status.
SELECT * FROM emails
WHERE project_id = sqlc.arg(project_id)
  AND id < sqlc.arg(before_id)::uuid
  AND id >= sqlc.arg(min_id)::uuid
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR status = ANY (sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(created_after)::timestamptz IS NULL OR created_at >= sqlc.narg(created_after)::timestamptz)
  AND (sqlc.narg(created_before)::timestamptz IS NULL OR created_at < sqlc.narg(created_before)::timestamptz)
  AND (sqlc.narg(template_id)::uuid IS NULL OR template_id = sqlc.narg(template_id)::uuid)
  AND (sqlc.narg(broadcast_id)::uuid IS NULL OR broadcast_id = sqlc.narg(broadcast_id)::uuid)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListProjectEmailsByRecipient :many
-- ListProjectEmails for one recipient address (case-insensitive).
SELECT * FROM emails
WHERE project_id = sqlc.arg(project_id)
  AND lower(recipient) = lower(sqlc.arg(recipient)::text)
  AND id < sqlc.arg(before_id)::uuid
  AND id >= sqlc.arg(min_id)::uuid
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR status = ANY (sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(created_after)::timestamptz IS NULL OR created_at >= sqlc.narg(created_after)::timestamptz)
  AND (sqlc.narg(created_before)::timestamptz IS NULL OR created_at < sqlc.narg(created_before)::timestamptz)
  AND (sqlc.narg(template_id)::uuid IS NULL OR template_id = sqlc.narg(template_id)::uuid)
  AND (sqlc.narg(broadcast_id)::uuid IS NULL OR broadcast_id = sqlc.narg(broadcast_id)::uuid)
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

-- name: LockOldEmails :many
-- Retention (opt-in), step 1 of a batch: lock finished emails older than the
-- cutoff that have no pending webhook delivery. SKIP LOCKED leaves emails an
-- event ingest is working on for a later run.
SELECT e.id FROM emails e
WHERE e.finished_at < sqlc.arg(before)::timestamptz
  AND NOT EXISTS (SELECT 1 FROM email_events ev
                  JOIN webhook_deliveries wd ON wd.event_id = ev.id AND wd.project_id = ev.project_id
                  WHERE ev.email_id = e.id AND ev.project_id = e.project_id AND wd.status = 'pending')
LIMIT sqlc.arg(batch_size)
FOR UPDATE OF e SKIP LOCKED;

-- name: DeleteLockedEmails :execrows
-- Retention step 2, a new statement and therefore a fresh snapshot: deletes
-- the locked emails with their content, idempotency keys, events and
-- deliveries (cascade), re-checking for a pending delivery committed after
-- step 1 read.
DELETE FROM emails e
WHERE e.id = ANY (sqlc.arg(ids)::uuid[])
  AND NOT EXISTS (SELECT 1 FROM email_events ev
                  JOIN webhook_deliveries wd ON wd.event_id = ev.id AND wd.project_id = ev.project_id
                  WHERE ev.email_id = e.id AND ev.project_id = e.project_id AND wd.status = 'pending');

-- name: CountOpenScheduled :one
-- Planned emails of the project that are still waiting.
SELECT count(*) FROM emails WHERE project_id = $1 AND status = 'queued' AND scheduled_at IS NOT NULL;
