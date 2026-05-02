# Database Schema — RideAndConnect

> PostgreSQL 16+ recommended. All tables use UUIDv7 primary keys (stored as `uuid`). Timestamps are `timestamptz` in UTC.
> Migrations: `golang-migrate/migrate/v4`. Files live under `migrations/`.
> Coding Standards: must comply with `coding_standards.json` (root).

## 1. Conventions
- Naming: `snake_case` tables and columns.
- Primary key column: `id uuid PRIMARY KEY DEFAULT uuidv7()` (a `uuidv7()` function ships in v16; otherwise install `pg_uuidv7` extension or generate IDs in app code).
- Created/updated timestamps: `created_at timestamptz NOT NULL DEFAULT now()`, `updated_at timestamptz NOT NULL DEFAULT now()` with a single `set_updated_at()` trigger.
- Soft-delete is not used; we use status fields and `left_at` instead.
- Foreign keys are `ON DELETE RESTRICT` unless explicitly stated.

## 2. Extensions

```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;       -- gen_random_uuid fallback
CREATE EXTENSION IF NOT EXISTS citext;         -- case-insensitive email
-- Optional: CREATE EXTENSION IF NOT EXISTS "pg_uuidv7"; if uuidv7() not built-in
```

## 3. Tables

### 3.1 `users`

```sql
CREATE TYPE bike_role AS ENUM ('RIDER', 'PILLION');

CREATE TABLE users (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
    email             citext      NULL UNIQUE,
    phone             text        NULL UNIQUE,
    password_hash     text        NOT NULL,
    initials          text        NULL CHECK (initials IS NULL OR char_length(initials) BETWEEN 1 AND 4),
    bike_model        text        NULL CHECK (bike_model IS NULL OR char_length(bike_model) <= 60),
    license_plate     text        NULL CHECK (license_plate IS NULL OR char_length(license_plate) <= 32),
    default_bike_role bike_role   NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_or_phone CHECK (email IS NOT NULL OR phone IS NOT NULL)
);

CREATE INDEX users_email_idx          ON users (email)         WHERE email IS NOT NULL;
CREATE INDEX users_phone_idx          ON users (phone)         WHERE phone IS NOT NULL;
CREATE INDEX users_license_plate_idx  ON users (license_plate) WHERE license_plate IS NOT NULL;
```

User profile fields:
- `initials` — short code (1–4 chars) for map markers, e.g. `SB`, `AM`, `SH`.
- `bike_model` — free text, e.g. `KTM 390 X Plus`.
- `license_plate` — normalized uppercase, used by the pair-suggestion logic (two users with the same plate are likely sharing one bike).
- `default_bike_role` — `RIDER` or `PILLION`. Per-ride pairing lives in `ride_pairs` (§3.7).

Notes:
- `email` is `citext` for case-insensitive uniqueness.
- `phone` is `text` (E.164 normalized at app layer); validation and uniqueness only enforced when present.
- `password_hash` stores argon2id encoded string (`$argon2id$...`).
- v1 ships email-only registration; the schema is phone-ready behind a feature flag.

### 3.2 `refresh_tokens`

```sql
CREATE TABLE refresh_tokens (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    user_id         uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      bytea       NOT NULL UNIQUE,         -- SHA-256 of opaque token
    issued_at       timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    revoked_at      timestamptz NULL,
    replaced_by     uuid        NULL REFERENCES refresh_tokens(id) ON DELETE SET NULL,
    user_agent      text        NULL,
    ip_address      inet        NULL
);

CREATE INDEX refresh_tokens_user_idx        ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_active_idx      ON refresh_tokens (user_id) WHERE revoked_at IS NULL;
CREATE INDEX refresh_tokens_expires_idx     ON refresh_tokens (expires_at);
```

Refresh tokens are single-use rotated. On refresh: insert a new row, set `revoked_at = now()` and `replaced_by` on the old row.

### 3.3 `rides`

```sql
CREATE TYPE ride_status AS ENUM ('active', 'ended');

CREATE TABLE rides (
    id                          uuid          PRIMARY KEY DEFAULT uuidv7(),
    owner_user_id               uuid          NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    invite_code                 text          NOT NULL,
    status                      ride_status   NOT NULL DEFAULT 'active',
    destination_name            text          NULL CHECK (destination_name IS NULL OR char_length(destination_name) BETWEEN 1 AND 120),
    destination_place_id        text          NULL CHECK (destination_place_id IS NULL OR char_length(destination_place_id) <= 256),
    destination_latitude        double precision NULL CHECK (destination_latitude IS NULL OR (destination_latitude BETWEEN -90 AND 90)),
    destination_longitude       double precision NULL CHECK (destination_longitude IS NULL OR (destination_longitude BETWEEN -180 AND 180)),
    started_at                  timestamptz   NOT NULL DEFAULT now(),
    ended_at                    timestamptz   NULL,
    created_at                  timestamptz   NOT NULL DEFAULT now(),
    updated_at                  timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT rides_dest_complete CHECK (
        (destination_latitude IS NULL AND destination_longitude IS NULL AND destination_name IS NULL) OR
        (destination_latitude IS NOT NULL AND destination_longitude IS NOT NULL AND destination_name IS NOT NULL)
    )
);

-- Active invite codes are unique while the ride is active. Once ended, the code is invalid.
CREATE UNIQUE INDEX rides_invite_code_active_uidx
    ON rides (invite_code)
    WHERE status = 'active';

CREATE INDEX rides_owner_idx  ON rides (owner_user_id);
CREATE INDEX rides_status_idx ON rides (status);
```

Status semantics:
- `active` — the ride accepts new members (subject to 3-cap), receives location and signaling traffic, and may have its destination updated by members.
- `ended` — terminal. No further state changes. Invite code is no longer unique-active and cannot be redeemed.

### 3.4 `ride_members`

```sql
CREATE TYPE ride_member_role AS ENUM ('owner', 'member');

CREATE TABLE ride_members (
    id              uuid              PRIMARY KEY DEFAULT uuidv7(),
    ride_id         uuid              NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    user_id         uuid              NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    role            ride_member_role  NOT NULL,
    joined_at       timestamptz       NOT NULL DEFAULT now(),
    left_at         timestamptz       NULL
);

-- A user can have at most one active membership per ride.
CREATE UNIQUE INDEX ride_members_active_uidx
    ON ride_members (ride_id, user_id)
    WHERE left_at IS NULL;

-- Owner uniqueness while membership is active.
CREATE UNIQUE INDEX ride_members_owner_active_uidx
    ON ride_members (ride_id)
    WHERE role = 'owner' AND left_at IS NULL;

CREATE INDEX ride_members_user_active_idx
    ON ride_members (user_id)
    WHERE left_at IS NULL;
```

3-member cap: enforced in code via `SELECT … FOR UPDATE` on the parent `rides` row, then `SELECT count(*) FROM ride_members WHERE ride_id = $1 AND left_at IS NULL`, then INSERT. A defensive trigger may be added (see §5).

### 3.5 `ride_routes` (cache for destination + peer routes)

```sql
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
    encoded_polyline         text             NOT NULL,                                   -- not logged
    provider                 text             NOT NULL,                                   -- 'google_routes' | 'mapbox'
    travel_mode              text             NOT NULL CHECK (travel_mode IN ('TWO_WHEELER','BICYCLE','WALK')),
    expires_at               timestamptz      NOT NULL,
    created_at               timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT ride_routes_peer_user_consistent CHECK (
        (route_kind = 'PEER'        AND peer_user_id IS NOT NULL AND peer_user_id <> user_id) OR
        (route_kind = 'DESTINATION' AND peer_user_id IS NULL)
    )
);

CREATE INDEX ride_routes_ride_user_idx
    ON ride_routes (ride_id, user_id, route_kind, expires_at DESC);

CREATE INDEX ride_routes_dest_cache_idx
    ON ride_routes (ride_id, user_id, origin_geohash7, destination_geohash7, travel_mode, expires_at DESC)
    WHERE route_kind = 'DESTINATION';

CREATE INDEX ride_routes_peer_cache_idx
    ON ride_routes (ride_id, user_id, peer_user_id, origin_geohash7, destination_geohash7, travel_mode, expires_at DESC)
    WHERE route_kind = 'PEER';
```

Cache strategy:
- Geohash precision 7 ≈ 153 m × 153 m bucket.
- **Destination route lookup**: latest non-expired `route_kind='DESTINATION'` row matching `(ride_id, user_id, origin_geohash7, destination_geohash7, travel_mode)`. TTL `ROUTE_CACHE_TTL` default 30 min.
- **Peer route lookup**: latest non-expired `route_kind='PEER'` row matching `(ride_id, user_id=caller, peer_user_id, origin_geohash7, destination_geohash7, travel_mode)`. TTL `PEER_ROUTE_CACHE_TTL` default 60 s (positions move).
- Insert-only (no UPDATE) to preserve provenance; periodic delete job removes rows where `expires_at < now() - interval '7 days'`.
- `LOCATION_PERSIST=false` does NOT affect this table — `ride_routes` stores only the origin/destination *of route requests*, not real-time samples.
- Setting/changing destination invalidates only `route_kind='DESTINATION'` rows for that ride; peer rows survive.

### 3.6 `ride_pairs` (rider + pillion same-bike)

Models the "two members share one bike, treat as one entity" relationship within a ride. While paired:

- The pillion's WebSocket suppresses outbound `location.update` (saves upstream bandwidth).
- The pillion's effective location is the rider's last-known location.
- `peerRoute(X, pillion)` reuses the rider's location instead of computing a separate provider call (saves API spend).
- `location.broadcast` carries `paired_user_ids` so clients render both icons on the rider's map pin.

The pillion taps "detach" (`detachFromRider` mutation) to leave the pair; `detached_at` is set and the pillion is treated as an independent entity from that moment.

```sql
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

CREATE UNIQUE INDEX ride_pairs_active_rider_uidx
    ON ride_pairs (ride_id, rider_user_id)   WHERE detached_at IS NULL;

CREATE UNIQUE INDEX ride_pairs_active_pillion_uidx
    ON ride_pairs (ride_id, pillion_user_id) WHERE detached_at IS NULL;

CREATE INDEX ride_pairs_lookup_idx
    ON ride_pairs (ride_id, detached_at);
```

Constraints enforced:
- A user cannot be both rider and pillion in the same pair (`ride_pairs_distinct`).
- A rider hosts at most one active pillion per ride.
- A pillion is in at most one active pair per ride.
- Both users must already be active members of the ride; the pair service verifies this transactionally before INSERT.

Authorization:
- Either the rider or the pillion (typically the pillion via the "detach" UX) initiates `detachFromRider` and stamps `detached_by`.
- Pairing is initiated by the pillion (`pairWithRider(rideId, riderUserId)`).

### 3.6b Extended user-profile columns (migration 0011)

```sql
CREATE TYPE blood_type AS ENUM ('A_POS','A_NEG','B_POS','B_NEG','AB_POS','AB_NEG','O_POS','O_NEG','UNKNOWN');

ALTER TABLE users
    ADD COLUMN blood_type                blood_type    NULL,
    ADD COLUMN age                       smallint      NULL CHECK (age IS NULL OR (age BETWEEN 5 AND 120)),
    ADD COLUMN height_cm                 smallint      NULL CHECK (height_cm IS NULL OR (height_cm BETWEEN 50 AND 250)),
    ADD COLUMN weight_kg                 smallint      NULL CHECK (weight_kg IS NULL OR (weight_kg BETWEEN 20 AND 500)),
    ADD COLUMN tyre_front_solo_psi       numeric(4,1)  NULL CHECK (tyre_front_solo_psi IS NULL OR (tyre_front_solo_psi BETWEEN 5 AND 100)),
    ADD COLUMN tyre_rear_solo_psi        numeric(4,1)  NULL CHECK (tyre_rear_solo_psi IS NULL OR (tyre_rear_solo_psi BETWEEN 5 AND 100)),
    ADD COLUMN tyre_front_pillion_psi    numeric(4,1)  NULL CHECK (tyre_front_pillion_psi IS NULL OR (tyre_front_pillion_psi BETWEEN 5 AND 100)),
    ADD COLUMN tyre_rear_pillion_psi     numeric(4,1)  NULL CHECK (tyre_rear_pillion_psi IS NULL OR (tyre_rear_pillion_psi BETWEEN 5 AND 100)),
    ADD COLUMN emergency_contact_name    text          NULL,
    ADD COLUMN emergency_contact_phone   text          NULL,
    ADD COLUMN emergency_contact_email   citext        NULL,
    ADD COLUMN emergency_contact_user_id uuid          NULL REFERENCES users(id) ON DELETE SET NULL;
```

All fields are optional. Tyre pressures cover two states (solo vs solo+pillion) at front + rear = 4 numeric values per user. Emergency contact may be either an in-system user (FK) or an external triple (name + phone + email); both can coexist.

### 3.6c `ride_invitations` (push-style invites)

```sql
CREATE TYPE invitation_status AS ENUM ('PENDING','ACCEPTED','DECLINED','REVOKED','EXPIRED');

CREATE TABLE ride_invitations (
    id              uuid              PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id         uuid              NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    invited_user_id uuid              NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_by      uuid              NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    identifier_used text              NOT NULL,
    status          invitation_status NOT NULL DEFAULT 'PENDING',
    created_at      timestamptz       NOT NULL DEFAULT now(),
    expires_at      timestamptz       NOT NULL,
    responded_at    timestamptz       NULL,
    CONSTRAINT ride_invitations_self_check CHECK (invited_user_id <> invited_by)
);

CREATE UNIQUE INDEX ride_invitations_pending_uidx
    ON ride_invitations (ride_id, invited_user_id) WHERE status = 'PENDING';
CREATE INDEX ride_invitations_invited_user_idx ON ride_invitations (invited_user_id, status);
CREATE INDEX ride_invitations_ride_idx ON ride_invitations (ride_id);
```

Coexists with the pull-style `inviteCode` flow on `rides`. A ride can have both a public invite code and direct push-invitations.

### 3.6d `emergency_events`

```sql
CREATE TABLE emergency_events (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    triggered_by      uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    ride_id           uuid        NULL REFERENCES rides(id) ON DELETE SET NULL,
    triggered_at      timestamptz NOT NULL DEFAULT now(),
    message           text        NULL,
    notified_user_ids uuid[]      NOT NULL DEFAULT '{}',
    resolved_at       timestamptz NULL,
    resolved_by       uuid        NULL REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX emergency_events_user_idx        ON emergency_events (triggered_by, triggered_at DESC);
CREATE INDEX emergency_events_ride_idx        ON emergency_events (ride_id, triggered_at DESC) WHERE ride_id IS NOT NULL;
CREATE INDEX emergency_events_unresolved_idx  ON emergency_events (triggered_by) WHERE resolved_at IS NULL;
```

`notified_user_ids` is the snapshot of active ride membership at trigger time — a later membership change does not retroactively rewrite the audit trail.

### 3.7 `audit_events` (lightweight)

```sql
CREATE TABLE audit_events (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    occurred_at     timestamptz NOT NULL DEFAULT now(),
    actor_user_id   uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    ride_id         uuid        NULL REFERENCES rides(id) ON DELETE SET NULL,
    event_type      text        NOT NULL,                    -- e.g. 'ride.created', 'ride.ended', 'member.joined'
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb -- never includes lat/lng/polyline
);

CREATE INDEX audit_events_ride_idx     ON audit_events (ride_id, occurred_at DESC);
CREATE INDEX audit_events_actor_idx    ON audit_events (actor_user_id, occurred_at DESC);
CREATE INDEX audit_events_type_idx     ON audit_events (event_type, occurred_at DESC);
```

A `metadata` JSON-validation policy prohibits the keys `latitude`, `longitude`, `lat`, `lng`, `coords`, `coordinates`, `encoded_polyline`, `polyline`, `sdp`, `candidate`, `password`, `password_hash`, `token`. Enforced at app layer.

## 4. Triggers

### 4.1 `set_updated_at`
Standard trigger to keep `updated_at` fresh on row UPDATEs:

```sql
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at  BEFORE UPDATE ON users  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_rides_updated_at  BEFORE UPDATE ON rides  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```

### 4.2 Defensive 3-member trigger (optional but recommended)

```sql
CREATE OR REPLACE FUNCTION enforce_ride_member_cap() RETURNS trigger AS $$
DECLARE
    active_count integer;
BEGIN
    IF (TG_OP = 'INSERT' AND NEW.left_at IS NULL) THEN
        SELECT count(*) INTO active_count
          FROM ride_members
         WHERE ride_id = NEW.ride_id AND left_at IS NULL;
        IF active_count >= 3 THEN
            RAISE EXCEPTION 'ride_full' USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ride_members_cap
    BEFORE INSERT ON ride_members
    FOR EACH ROW EXECUTE FUNCTION enforce_ride_member_cap();
```

Application code still performs the check inside a `SELECT … FOR UPDATE` transaction; the trigger is a defense-in-depth backstop.

## 5. Lifecycle Invariants
- Creating a ride inserts one `ride_members` row with `role='owner'`.
- Joining a ride inserts one `ride_members` row with `role='member'` after the cap check passes; emits `member.joined` over WS.
- Leaving a ride sets `left_at = now()` on the active membership row.
  - If the leaver is the owner and ≥1 active member remains: ownership transfers to the earliest joiner (`UPDATE … WHERE role='member' AND left_at IS NULL ORDER BY joined_at ASC LIMIT 1`). The owner-active uniqueness index is satisfied because the prior owner row's `role` is unchanged but `left_at` is now set.
  - If the leaver was the only active member: ride is auto-ended (`status='ended'`, `ended_at=now()`).
- Ending a ride sets `status='ended'`, `ended_at=now()`, then sets `left_at=now()` on all remaining active memberships; emits `ride.ended` over WS; closes the room.
- The unique partial index on `rides.invite_code WHERE status='active'` allows code reuse only across ended rides — once a ride ends, its code is no longer addressable.

## 6. Indexing Summary

| Index | Purpose |
|---|---|
| `users_email_idx` | login lookup |
| `users_phone_idx` | future phone login |
| `refresh_tokens_active_idx` | active refresh enumeration |
| `refresh_tokens_expires_idx` | expiry sweep |
| `rides_invite_code_active_uidx` | active invite uniqueness + lookup |
| `rides_owner_idx` | owner reverse lookup |
| `rides_status_idx` | active-rides queries |
| `ride_members_active_uidx` | one active membership per (ride, user) |
| `ride_members_owner_active_uidx` | one active owner per ride |
| `ride_members_user_active_idx` | "what ride am I in?" |
| `ride_routes_cache_lookup_idx` | cache hit path |

## 7. Migrations

```
migrations/
  0001_extensions.up.sql
  0001_extensions.down.sql
  0002_users.up.sql
  0002_users.down.sql
  0003_refresh_tokens.up.sql
  0003_refresh_tokens.down.sql
  0004_rides.up.sql
  0004_rides.down.sql
  0005_ride_members.up.sql
  0005_ride_members.down.sql
  0006_ride_routes.up.sql
  0006_ride_routes.down.sql
  0007_audit_events.up.sql
  0007_audit_events.down.sql
  0008_triggers.up.sql
  0008_triggers.down.sql
```

## 8. Capacity & Retention
- `users` and `rides` are unbounded but small for a 3-person product.
- `ride_routes` retained 7 days then pruned by a daily job.
- `audit_events` retained 90 days.
- `refresh_tokens` rows pruned 7 days after `expires_at`.

## 9. Privacy
- No real-time GPS samples are persisted in MVP. `LOCATION_PERSIST=true` (future) will add a `location_samples` table; until then, no schema is created for it.
- `ride_routes.encoded_polyline` is sensitive — never written to logs and never returned to non-members.
- Backups must be encrypted at rest (managed-DB feature).
