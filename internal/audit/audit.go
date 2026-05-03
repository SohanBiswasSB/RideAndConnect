// Package audit writes lifecycle events into the audit_events table.
// Calls are best-effort and fire-and-forget — a failed audit insert
// never blocks or fails the originating operation.
//
// Allowed metadata keys are validated against a deny-list lifted from
// docs/SECURITY.md §8.2 so an audit row can never accidentally carry
// raw GPS coordinates or secrets.
package audit

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// Event names. Centralized here so call sites don't drift.
const (
	EventUserRegistered   = "user.registered"
	EventUserLoggedIn     = "user.logged_in"
	EventRefreshRotated   = "auth.refresh_rotated"
	EventLogout           = "auth.logout"
	EventRideCreated      = "ride.created"
	EventRideEnded        = "ride.ended"
	EventMemberJoined     = "ride.member_joined"
	EventMemberLeft       = "ride.member_left"
	EventDestinationSet   = "ride.destination_set"
	EventPairCreated      = "ride.pair_created"
	EventPairDetached     = "ride.pair_detached"
	EventInvitationSent   = "ride.invitation_sent"
	EventInvitationAccept = "ride.invitation_accepted"
	EventEmergencyTrigger = "emergency.triggered"
	EventEmergencyResolve = "emergency.resolved"
)

// Recorder is the seam over the audit_events table. The struct is
// safe for concurrent use; pool concurrency rules apply.
type Recorder struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

// New constructs a Recorder. Returns nil on a nil pool so a non-DB dev
// instance still compiles and runs (callers nil-check).
func New(pool *pgxpool.Pool, log zerolog.Logger) *Recorder {
	if pool == nil {
		return nil
	}
	return &Recorder{pool: pool, log: log.With().Str("component", "audit").Logger()}
}

// Record fires an audit insert in a goroutine. Never blocks the caller.
// If r is nil the call is a no-op (lets callers chain without
// nil-checks).
func (r *Recorder) Record(actorUserID *uuid.UUID, rideID *uuid.UUID, eventType string, metadata map[string]any) {
	if r == nil {
		return
	}
	clean := sanitizeMetadata(metadata)
	body, err := json.Marshal(clean)
	if err != nil {
		body = []byte(`{}`)
	}
	go r.write(actorUserID, rideID, eventType, body)
}

func (r *Recorder) write(actorUserID *uuid.UUID, rideID *uuid.UUID, eventType string, metadataJSON []byte) {
	defer func() { _ = recover() }() // never crash a worker
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_events (actor_user_id, ride_id, event_type, metadata)
		VALUES ($1, $2, $3, $4)
	`, actorUserID, rideID, eventType, metadataJSON)
	if err != nil {
		r.log.Warn().Err(err).Str("event_type", eventType).Msg("audit insert failed")
	}
}

// denyKeys mirrors logging.Sanitize's deny-list — values matching these
// keys (case-insensitive, prefix-aware) are dropped from the metadata
// before persistence.
var denyKeys = []string{
	"latitude", "longitude", "lat", "lng", "coords", "coordinates",
	"encoded_polyline", "polyline",
	"password", "password_hash", "token", "refresh_token", "access_token", "authorization",
	"sdp", "candidate",
	"x_goog_api_key", "mapbox_access_token", "mapbox",
}

func sanitizeMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if isDenied(k) {
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			out[k] = sanitizeMetadata(nested)
			continue
		}
		out[k] = v
	}
	return out
}

func isDenied(name string) bool {
	lc := strings.ToLower(name)
	for _, k := range denyKeys {
		if lc == k || strings.HasPrefix(lc, k+"_") {
			return true
		}
	}
	return false
}
