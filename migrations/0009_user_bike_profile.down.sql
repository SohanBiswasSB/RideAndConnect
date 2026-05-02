DROP INDEX IF EXISTS users_license_plate_idx;
ALTER TABLE users
    DROP COLUMN IF EXISTS default_bike_role,
    DROP COLUMN IF EXISTS license_plate,
    DROP COLUMN IF EXISTS bike_model,
    DROP COLUMN IF EXISTS initials;
DROP TYPE IF EXISTS bike_role;
