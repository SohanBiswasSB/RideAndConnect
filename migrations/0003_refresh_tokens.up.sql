CREATE TABLE refresh_tokens (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      bytea       NOT NULL UNIQUE,
    issued_at       timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    revoked_at      timestamptz NULL,
    replaced_by     uuid        NULL REFERENCES refresh_tokens(id) ON DELETE SET NULL,
    user_agent      text        NULL,
    ip_address      inet        NULL
);

CREATE INDEX refresh_tokens_user_idx    ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_active_idx  ON refresh_tokens (user_id) WHERE revoked_at IS NULL;
CREATE INDEX refresh_tokens_expires_idx ON refresh_tokens (expires_at);
