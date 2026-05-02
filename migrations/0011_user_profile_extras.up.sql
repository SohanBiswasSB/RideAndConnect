-- Extended user profile: bio data, tyre pressures, emergency contact.
-- All fields are optional — accounts may register with just name+phone+password.

CREATE TYPE blood_type AS ENUM (
    'A_POS', 'A_NEG',
    'B_POS', 'B_NEG',
    'AB_POS', 'AB_NEG',
    'O_POS', 'O_NEG',
    'UNKNOWN'
);

ALTER TABLE users
    ADD COLUMN blood_type                   blood_type        NULL,
    ADD COLUMN age                          smallint          NULL CHECK (age IS NULL OR (age BETWEEN 5 AND 120)),
    ADD COLUMN height_cm                    smallint          NULL CHECK (height_cm IS NULL OR (height_cm BETWEEN 50 AND 250)),
    ADD COLUMN weight_kg                    smallint          NULL CHECK (weight_kg IS NULL OR (weight_kg BETWEEN 20 AND 500)),

    -- Tyre pressures in PSI to one decimal. Solo = rider only; pillion =
    -- rider with passenger (typically higher rear pressure).
    ADD COLUMN tyre_front_solo_psi          numeric(4, 1)     NULL CHECK (tyre_front_solo_psi    IS NULL OR (tyre_front_solo_psi    BETWEEN 5 AND 100)),
    ADD COLUMN tyre_rear_solo_psi           numeric(4, 1)     NULL CHECK (tyre_rear_solo_psi     IS NULL OR (tyre_rear_solo_psi     BETWEEN 5 AND 100)),
    ADD COLUMN tyre_front_pillion_psi       numeric(4, 1)     NULL CHECK (tyre_front_pillion_psi IS NULL OR (tyre_front_pillion_psi BETWEEN 5 AND 100)),
    ADD COLUMN tyre_rear_pillion_psi        numeric(4, 1)     NULL CHECK (tyre_rear_pillion_psi  IS NULL OR (tyre_rear_pillion_psi  BETWEEN 5 AND 100)),

    -- Emergency contact: either an in-system user (FK) OR external triple
    -- (name + phone + email). Both forms can coexist; clients pick which
    -- to surface. No CHECK constraint — all fields are optional.
    ADD COLUMN emergency_contact_name       text              NULL CHECK (emergency_contact_name  IS NULL OR char_length(emergency_contact_name)  BETWEEN 1 AND 80),
    ADD COLUMN emergency_contact_phone      text              NULL CHECK (emergency_contact_phone IS NULL OR char_length(emergency_contact_phone) <= 32),
    ADD COLUMN emergency_contact_email      citext            NULL,
    ADD COLUMN emergency_contact_user_id    uuid              NULL REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX users_emergency_contact_user_idx
    ON users (emergency_contact_user_id)
    WHERE emergency_contact_user_id IS NOT NULL;
