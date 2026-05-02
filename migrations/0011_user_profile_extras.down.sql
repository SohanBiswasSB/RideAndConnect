DROP INDEX IF EXISTS users_emergency_contact_user_idx;
ALTER TABLE users
    DROP COLUMN IF EXISTS emergency_contact_user_id,
    DROP COLUMN IF EXISTS emergency_contact_email,
    DROP COLUMN IF EXISTS emergency_contact_phone,
    DROP COLUMN IF EXISTS emergency_contact_name,
    DROP COLUMN IF EXISTS tyre_rear_pillion_psi,
    DROP COLUMN IF EXISTS tyre_front_pillion_psi,
    DROP COLUMN IF EXISTS tyre_rear_solo_psi,
    DROP COLUMN IF EXISTS tyre_front_solo_psi,
    DROP COLUMN IF EXISTS weight_kg,
    DROP COLUMN IF EXISTS height_cm,
    DROP COLUMN IF EXISTS age,
    DROP COLUMN IF EXISTS blood_type;
DROP TYPE IF EXISTS blood_type;
