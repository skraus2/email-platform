-- API keys may carry the template scopes. Rolling back fails while a key
-- still holds one of them (revoke or narrow those keys first).

-- +goose Up
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY['emails:send', 'emails:read', 'templates:read', 'templates:write']::text[]);

-- +goose Down
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) > 0 AND scopes <@ ARRAY['emails:send', 'emails:read']::text[]);
