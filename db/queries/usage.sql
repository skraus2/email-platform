-- name: LastUsageRollup :one
-- When the rollup last stored a day (the epoch before the first run).
SELECT coalesce(max(computed_at), 'epoch'::timestamptz)::timestamptz AS computed_at FROM usage_daily;

-- name: RollupUsage :execrows
-- Recomputes the usage of every project for the UTC days in [from, until)
-- from emails (accepted) and email events (everything else). Counts only
-- grow: an older snapshot that commits last, or events removed by
-- retention, never lower a stored count. Rows are written in key order so
-- concurrent runs cannot deadlock.
WITH acc AS (
    SELECT project_id, (created_at AT TIME ZONE 'UTC')::date AS day, count(*) AS accepted
    FROM emails
    WHERE created_at >= sqlc.arg(from_time)::timestamptz AND created_at < sqlc.arg(until_time)::timestamptz
    GROUP BY 1, 2
), evd AS (
    SELECT DISTINCT project_id, (received_at AT TIME ZONE 'UTC')::date AS day, email_id, type
    FROM email_events
    WHERE received_at >= sqlc.arg(from_time)::timestamptz AND received_at < sqlc.arg(until_time)::timestamptz
      AND (type IN ('captured', 'submitted', 'failed', 'canceled', 'delivered', 'complained')
           OR (type = 'bounced' AND details->>'bounce_type' = 'Permanent'))
), ev AS (
    SELECT project_id, day,
           count(*) FILTER (WHERE type IN ('captured', 'submitted')) AS sent,
           count(*) FILTER (WHERE type = 'failed') AS failed,
           count(*) FILTER (WHERE type = 'canceled') AS canceled,
           count(*) FILTER (WHERE type = 'delivered') AS delivered,
           count(*) FILTER (WHERE type = 'bounced') AS bounced,
           count(*) FILTER (WHERE type = 'complained') AS complained
    FROM evd
    GROUP BY 1, 2
)
INSERT INTO usage_daily (project_id, day, accepted, sent, failed, canceled, delivered, bounced, complained)
SELECT project_id, day, coalesce(acc.accepted, 0), coalesce(ev.sent, 0), coalesce(ev.failed, 0),
       coalesce(ev.canceled, 0), coalesce(ev.delivered, 0), coalesce(ev.bounced, 0), coalesce(ev.complained, 0)
FROM acc FULL JOIN ev USING (project_id, day)
ORDER BY project_id, day
ON CONFLICT (project_id, day) DO UPDATE SET
    accepted    = GREATEST(usage_daily.accepted, EXCLUDED.accepted),
    sent        = GREATEST(usage_daily.sent, EXCLUDED.sent),
    failed      = GREATEST(usage_daily.failed, EXCLUDED.failed),
    canceled    = GREATEST(usage_daily.canceled, EXCLUDED.canceled),
    delivered   = GREATEST(usage_daily.delivered, EXCLUDED.delivered),
    bounced     = GREATEST(usage_daily.bounced, EXCLUDED.bounced),
    complained  = GREATEST(usage_daily.complained, EXCLUDED.complained),
    computed_at = GREATEST(usage_daily.computed_at, now());

-- name: ListUsageDaily :many
SELECT day, accepted, sent, failed, canceled, delivered, bounced, complained
FROM usage_daily
WHERE project_id = $1 AND day >= sqlc.arg(from_day)::date AND day <= sqlc.arg(to_day)::date
ORDER BY day;
