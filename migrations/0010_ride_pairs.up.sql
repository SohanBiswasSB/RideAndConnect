-- ride_pairs models the "rider + pillion share one bike" relationship
-- within a single ride session. While paired:
--   - location.update from the pillion is suppressed (we use the rider's
--     last-known location for both),
--   - peerRoute(X, pillion) reuses the rider's location,
--   - location.broadcast carries a paired_user_ids field so the client
--     renders both icons on the rider's pin.
--
-- The pillion taps "detach" to leave the pair and resume independent
-- location sharing; this sets detached_at and the pillion starts being
-- treated as a separate entity for routing and broadcast.
CREATE TABLE ride_pairs (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id         uuid        NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    rider_user_id   uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    pillion_user_id uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    paired_at       timestamptz NOT NULL DEFAULT now(),
    detached_at     timestamptz NULL,
    detached_by     uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT ride_pairs_distinct CHECK (rider_user_id <> pillion_user_id)
);

-- A rider can host at most one active pillion per ride, and a pillion can
-- be in at most one active pair per ride.
CREATE UNIQUE INDEX ride_pairs_active_rider_uidx
    ON ride_pairs (ride_id, rider_user_id)
    WHERE detached_at IS NULL;

CREATE UNIQUE INDEX ride_pairs_active_pillion_uidx
    ON ride_pairs (ride_id, pillion_user_id)
    WHERE detached_at IS NULL;

-- Lookup index for "is user X currently in a pair on ride R?".
CREATE INDEX ride_pairs_lookup_idx
    ON ride_pairs (ride_id, detached_at);
