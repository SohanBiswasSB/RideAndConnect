-- User-level bike profile. Captures the user's preferred role on their
-- usual bike. The per-ride pairing lives in ride_pairs (see 0010).
CREATE TYPE bike_role AS ENUM ('RIDER', 'PILLION');

ALTER TABLE users
    ADD COLUMN initials          text       NULL CHECK (initials IS NULL OR char_length(initials) BETWEEN 1 AND 4),
    ADD COLUMN bike_model        text       NULL CHECK (bike_model IS NULL OR char_length(bike_model) <= 60),
    ADD COLUMN license_plate     text       NULL CHECK (license_plate IS NULL OR char_length(license_plate) <= 32),
    ADD COLUMN default_bike_role bike_role  NULL;

-- Plate index used by the pair-suggestion logic ("who shares this bike?").
-- Plates are user-entered and normalized in app code (uppercase, trimmed).
CREATE INDEX users_license_plate_idx
    ON users (license_plate)
    WHERE license_plate IS NOT NULL;
