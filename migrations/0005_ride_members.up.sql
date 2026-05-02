CREATE TYPE ride_member_role AS ENUM ('owner', 'member');

CREATE TABLE ride_members (
    id              uuid              PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id         uuid              NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    user_id         uuid              NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    role            ride_member_role  NOT NULL,
    joined_at       timestamptz       NOT NULL DEFAULT now(),
    left_at         timestamptz       NULL
);

CREATE UNIQUE INDEX ride_members_active_uidx
    ON ride_members (ride_id, user_id)
    WHERE left_at IS NULL;

CREATE UNIQUE INDEX ride_members_owner_active_uidx
    ON ride_members (ride_id)
    WHERE role = 'owner' AND left_at IS NULL;

CREATE INDEX ride_members_user_active_idx
    ON ride_members (user_id)
    WHERE left_at IS NULL;
