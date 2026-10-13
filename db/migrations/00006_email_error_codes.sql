-- Stable failure codes. error_code is the client-visible reason of the last
-- failed attempt (or of the final failure); last_error keeps the sanitized
-- detail (failure class and SMTP reply code) for operators and is not
-- changed here. Existing rows get their code derived from last_error.

-- +goose Up
ALTER TABLE emails ADD COLUMN error_code text
    CONSTRAINT emails_error_code_check CHECK (error_code IN (
        'provider_unreachable', 'provider_timeout', 'provider_protocol_error',
        'provider_deferred', 'provider_rejected', 'message_invalid', 'unsafe_target',
        'suppressed', 'project_disabled', 'content_missing', 'expired', 'internal_error'));

UPDATE emails SET error_code = CASE
    WHEN last_error = 'delivery failed: connect' THEN 'provider_unreachable'
    WHEN last_error = 'delivery failed: timeout' THEN 'provider_timeout'
    WHEN last_error = 'delivery failed: protocol' THEN 'provider_protocol_error'
    WHEN last_error ~ '^delivery failed: rejected \(smtp 4[0-9][0-9]\)$' THEN 'provider_deferred'
    WHEN last_error LIKE 'delivery failed: rejected%' THEN 'provider_rejected'
    WHEN last_error LIKE 'delivery failed: invalid%' THEN 'message_invalid'
    WHEN last_error LIKE 'delivery failed: unsafe_target%' THEN 'unsafe_target'
    WHEN last_error IN ('project_disabled', 'content_missing', 'expired') THEN last_error
    ELSE 'internal_error'
END
WHERE (last_error IS NOT NULL OR status = 'failed') AND status NOT IN ('captured', 'submitted');

-- A failed email always says why; a dispatched one carries no error.
ALTER TABLE emails ADD CONSTRAINT emails_error_code_status_check CHECK (
    (status <> 'failed' OR error_code IS NOT NULL)
    AND (status NOT IN ('captured', 'submitted') OR error_code IS NULL));

-- +goose Down
ALTER TABLE emails DROP COLUMN error_code;
