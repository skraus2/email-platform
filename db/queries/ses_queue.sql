-- name: MarkQueueDrained :exec
-- The queue was drained now (database clock).
INSERT INTO ses_queue_heartbeats (queue_key, drained_at)
VALUES (sqlc.arg(queue_key), now())
ON CONFLICT (queue_key) DO UPDATE SET drained_at = EXCLUDED.drained_at;

-- name: HoldQueueHealth :exec
-- A message was left for redelivery: the queue is not healthy for the next
-- hold_seconds, whatever other receives drain meanwhile.
INSERT INTO ses_queue_heartbeats (queue_key, held_until)
VALUES (sqlc.arg(queue_key), now() + make_interval(secs => sqlc.arg(hold_seconds)::float8))
ON CONFLICT (queue_key) DO UPDATE
SET held_until = GREATEST(ses_queue_heartbeats.held_until, EXCLUDED.held_until);

-- name: QueueDrainedWithin :one
-- Whether the queue was drained within the last within_seconds, is not held
-- and was drained after its last hold ended (database clock): a drain during
-- a hold proves nothing about the message held. No row or no drain: not
-- healthy. A drain time in the future (clock moved back) does not count.
SELECT EXISTS (
    SELECT 1 FROM ses_queue_heartbeats
    WHERE queue_key = sqlc.arg(queue_key)
      AND drained_at > now() - make_interval(secs => sqlc.arg(within_seconds)::float8)
      AND drained_at <= now() + interval '1 minute'
      AND (held_until IS NULL OR (held_until <= now() AND drained_at > held_until))
);
