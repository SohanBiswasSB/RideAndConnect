CREATE TABLE audit_events (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at     timestamptz NOT NULL DEFAULT now(),
    actor_user_id   uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    ride_id         uuid        NULL REFERENCES rides(id) ON DELETE SET NULL,
    event_type      text        NOT NULL,
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX audit_events_ride_idx     ON audit_events (ride_id, occurred_at DESC);
CREATE INDEX audit_events_actor_idx    ON audit_events (actor_user_id, occurred_at DESC);
CREATE INDEX audit_events_type_idx     ON audit_events (event_type, occurred_at DESC);
