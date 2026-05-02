CREATE TYPE ride_status AS ENUM ('active', 'ended');

CREATE TABLE rides (
    id                          uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id               uuid             NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    invite_code                 text             NOT NULL,
    status                      ride_status      NOT NULL DEFAULT 'active',
    destination_name            text             NULL CHECK (destination_name IS NULL OR char_length(destination_name) BETWEEN 1 AND 120),
    destination_place_id        text             NULL CHECK (destination_place_id IS NULL OR char_length(destination_place_id) <= 256),
    destination_latitude        double precision NULL CHECK (destination_latitude IS NULL OR (destination_latitude BETWEEN -90 AND 90)),
    destination_longitude       double precision NULL CHECK (destination_longitude IS NULL OR (destination_longitude BETWEEN -180 AND 180)),
    started_at                  timestamptz      NOT NULL DEFAULT now(),
    ended_at                    timestamptz      NULL,
    created_at                  timestamptz      NOT NULL DEFAULT now(),
    updated_at                  timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT rides_dest_complete CHECK (
        (destination_latitude IS NULL AND destination_longitude IS NULL AND destination_name IS NULL) OR
        (destination_latitude IS NOT NULL AND destination_longitude IS NOT NULL AND destination_name IS NOT NULL)
    )
);

CREATE UNIQUE INDEX rides_invite_code_active_uidx
    ON rides (invite_code)
    WHERE status = 'active';

CREATE INDEX rides_owner_idx  ON rides (owner_user_id);
CREATE INDEX rides_status_idx ON rides (status);
