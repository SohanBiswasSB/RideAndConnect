CREATE TABLE users (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
    email           citext      NULL UNIQUE,
    phone           text        NULL UNIQUE,
    password_hash   text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_or_phone CHECK (email IS NOT NULL OR phone IS NOT NULL)
);

CREATE INDEX users_email_idx ON users (email) WHERE email IS NOT NULL;
CREATE INDEX users_phone_idx ON users (phone) WHERE phone IS NOT NULL;
