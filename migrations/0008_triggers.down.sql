DROP TRIGGER IF EXISTS trg_ride_members_cap ON ride_members;
DROP FUNCTION IF EXISTS enforce_ride_member_cap();

DROP TRIGGER IF EXISTS trg_rides_updated_at ON rides;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP FUNCTION IF EXISTS set_updated_at();
