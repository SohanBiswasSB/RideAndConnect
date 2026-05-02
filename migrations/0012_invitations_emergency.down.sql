DROP INDEX IF EXISTS emergency_events_unresolved_idx;
DROP INDEX IF EXISTS emergency_events_ride_idx;
DROP INDEX IF EXISTS emergency_events_user_idx;
DROP TABLE IF EXISTS emergency_events;

DROP INDEX IF EXISTS ride_invitations_ride_idx;
DROP INDEX IF EXISTS ride_invitations_invited_user_idx;
DROP INDEX IF EXISTS ride_invitations_pending_uidx;
DROP TABLE IF EXISTS ride_invitations;
DROP TYPE IF EXISTS invitation_status;
