-- Whether SES events reach the event store, per SES event queue: when the
-- queue was last drained (a receive returned less than a full batch and
-- every message of it was handled for good), and until when it counts as
-- unhealthy anyway because a message was left for redelivery (it is hidden
-- from every receive until its visibility timeout ends). Written by every
-- worker's poller; the dispatcher reads it to tell whether a missing SES
-- event proves anything. Platform-wide (no project data); the key is a hash
-- of the queue URL.

-- +goose Up
CREATE TABLE ses_queue_heartbeats (
    queue_key  text        PRIMARY KEY CHECK (queue_key ~ '^[0-9a-f]{64}$'),
    drained_at timestamptz,
    held_until timestamptz
);

-- +goose Down
DROP TABLE ses_queue_heartbeats;
