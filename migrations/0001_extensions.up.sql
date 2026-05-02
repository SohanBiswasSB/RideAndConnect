-- Required extensions.
-- pgcrypto provides gen_random_uuid() (UUIDv4) used as a default until/unless the
-- pg_uuidv7 extension is provisioned in the target environment.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
