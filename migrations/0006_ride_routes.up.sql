CREATE TYPE ride_route_kind AS ENUM ('DESTINATION', 'PEER');

CREATE TABLE ride_routes (
    id                       uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id                  uuid             NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    user_id                  uuid             NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    route_kind               ride_route_kind  NOT NULL DEFAULT 'DESTINATION',
    peer_user_id             uuid             NULL REFERENCES users(id) ON DELETE RESTRICT,
    origin_latitude          double precision NOT NULL CHECK (origin_latitude BETWEEN -90 AND 90),
    origin_longitude         double precision NOT NULL CHECK (origin_longitude BETWEEN -180 AND 180),
    destination_latitude     double precision NOT NULL CHECK (destination_latitude BETWEEN -90 AND 90),
    destination_longitude    double precision NOT NULL CHECK (destination_longitude BETWEEN -180 AND 180),
    origin_geohash7          text             NOT NULL,
    destination_geohash7     text             NOT NULL,
    distance_meters          integer          NOT NULL CHECK (distance_meters >= 0),
    duration_seconds         integer          NOT NULL CHECK (duration_seconds >= 0),
    encoded_polyline         text             NOT NULL,
    provider                 text             NOT NULL,
    travel_mode              text             NOT NULL CHECK (travel_mode IN ('TWO_WHEELER', 'BICYCLE', 'WALK')),
    expires_at               timestamptz      NOT NULL,
    created_at               timestamptz      NOT NULL DEFAULT now(),
    -- Peer routes require peer_user_id; destination routes must not have one.
    CONSTRAINT ride_routes_peer_user_consistent CHECK (
        (route_kind = 'PEER'        AND peer_user_id IS NOT NULL AND peer_user_id <> user_id) OR
        (route_kind = 'DESTINATION' AND peer_user_id IS NULL)
    )
);

CREATE INDEX ride_routes_ride_user_idx
    ON ride_routes (ride_id, user_id, route_kind, expires_at DESC);

-- Destination cache lookup.
CREATE INDEX ride_routes_dest_cache_idx
    ON ride_routes (ride_id, user_id, origin_geohash7, destination_geohash7, travel_mode, expires_at DESC)
    WHERE route_kind = 'DESTINATION';

-- Peer cache lookup.
CREATE INDEX ride_routes_peer_cache_idx
    ON ride_routes (ride_id, user_id, peer_user_id, origin_geohash7, destination_geohash7, travel_mode, expires_at DESC)
    WHERE route_kind = 'PEER';
