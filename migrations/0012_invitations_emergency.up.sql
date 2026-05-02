-- Direct (push) invitations addressed to a known user, plus emergency
-- events that fan out to the active ride at trigger time.

CREATE TYPE invitation_status AS ENUM ('PENDING', 'ACCEPTED', 'DECLINED', 'REVOKED', 'EXPIRED');

CREATE TABLE ride_invitations (
    id                uuid                PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id           uuid                NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    invited_user_id   uuid                NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_by        uuid                NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    -- Audit trail of what the inviter actually typed (an email, a phone
    -- string, a license plate). Lowercased/normalized at app level.
    identifier_used   text                NOT NULL CHECK (char_length(identifier_used) BETWEEN 1 AND 128),
    status            invitation_status   NOT NULL DEFAULT 'PENDING',
    created_at        timestamptz         NOT NULL DEFAULT now(),
    expires_at        timestamptz         NOT NULL,
    responded_at      timestamptz         NULL,
    CONSTRAINT ride_invitations_self_check CHECK (invited_user_id <> invited_by)
);

-- One pending invitation per (ride, invited_user) at a time.
CREATE UNIQUE INDEX ride_invitations_pending_uidx
    ON ride_invitations (ride_id, invited_user_id)
    WHERE status = 'PENDING';

CREATE INDEX ride_invitations_invited_user_idx
    ON ride_invitations (invited_user_id, status);

CREATE INDEX ride_invitations_ride_idx
    ON ride_invitations (ride_id);

-- Emergency events. Notification is delivered to the active ride at
-- trigger time (snapshot stored as notified_user_ids); the WS push to
-- those users is a Phase 7 concern.
CREATE TABLE emergency_events (
    id                  uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    triggered_by        uuid          NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    ride_id             uuid          NULL REFERENCES rides(id) ON DELETE SET NULL,
    triggered_at        timestamptz   NOT NULL DEFAULT now(),
    message             text          NULL CHECK (message IS NULL OR char_length(message) <= 280),
    -- Snapshot of who was in the riding group at trigger time. Stored as
    -- a uuid array so a later membership change doesn't rewrite history.
    notified_user_ids   uuid[]        NOT NULL DEFAULT '{}',
    resolved_at         timestamptz   NULL,
    resolved_by         uuid          NULL REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX emergency_events_user_idx
    ON emergency_events (triggered_by, triggered_at DESC);

CREATE INDEX emergency_events_ride_idx
    ON emergency_events (ride_id, triggered_at DESC)
    WHERE ride_id IS NOT NULL;

-- Active emergency lookup (unresolved).
CREATE INDEX emergency_events_unresolved_idx
    ON emergency_events (triggered_by)
    WHERE resolved_at IS NULL;
