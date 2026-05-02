-- Single-use WebSocket upgrade tickets. Issued by the wsTicket mutation
-- and consumed by the WS upgrade handler when the client cannot send the
-- Authorization header (browser WebSocket API). Stored as SHA-256 hash —
-- a leaked DB row cannot be replayed.
CREATE TABLE ws_tickets (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  bytea       NOT NULL UNIQUE,
    issued_at   timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz NULL
);

CREATE INDEX ws_tickets_user_idx    ON ws_tickets (user_id);
CREATE INDEX ws_tickets_expires_idx ON ws_tickets (expires_at);

-- Active (unconsumed, unexpired) ticket lookup; supports the verify path.
CREATE INDEX ws_tickets_active_idx
    ON ws_tickets (token_hash)
    WHERE consumed_at IS NULL;
