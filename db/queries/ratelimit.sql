-- name: CountSend :one
-- Counts one send in the current minute of the project and returns the new
-- count and the seconds until the window ends (by the database clock).
INSERT INTO rate_limit_windows AS w (project_id, window_start, count)
VALUES ($1, date_trunc('minute', now()), 1)
ON CONFLICT (project_id, window_start) DO UPDATE SET count = w.count + 1
RETURNING w.count,
    ceil(extract(epoch FROM (w.window_start + interval '1 minute') - clock_timestamp()))::integer AS retry_after_seconds;

-- name: DeleteRateLimitWindowsBefore :execrows
-- Maintenance (all projects): windows that ended long ago.
DELETE FROM rate_limit_windows WHERE window_start < now() - interval '1 hour';

-- name: GetSendCount :one
-- Read-only pre-check: sends already committed in the current minute of the
-- project, and the seconds until that window ends.
SELECT coalesce((SELECT w.count FROM rate_limit_windows w
                 WHERE w.project_id = $1 AND w.window_start = date_trunc('minute', now())), 0)::integer AS count,
       ceil(extract(epoch FROM date_trunc('minute', now()) + interval '1 minute' - clock_timestamp()))::integer AS retry_after_seconds;
