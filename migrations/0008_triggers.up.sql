-- updated_at maintenance.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_rides_updated_at
    BEFORE UPDATE ON rides
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Defensive 3-member cap. App code performs the canonical check inside
-- a SELECT ... FOR UPDATE transaction; this trigger is the backstop.
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
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ride_members_cap
    BEFORE INSERT ON ride_members
    FOR EACH ROW EXECUTE FUNCTION enforce_ride_member_cap();
