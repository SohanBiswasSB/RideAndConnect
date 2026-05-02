package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// handlePing replies pong with the same nonce. Heartbeat doubles as the
// RTT probe per docs/WEBSOCKET_PROTOCOL.md §10.5.
func (r *Room) handlePing(c *Connection, env Envelope) error {
	// Echo whatever payload was supplied (including nonce + send_ts).
	out := Envelope{
		Type:      TypePong,
		RideID:    r.rideID,
		Timestamp: time.Now().UTC(),
		Payload:   env.Payload,
	}
	c.enqueue(out)
	return nil
}

// handleLocationUpdate validates the position, stores it in presence,
// and fans out a location.broadcast to peers. If the sender is a paired
// pillion, the update is silently discarded — the rider's broadcasts
// already represent the pair.
func (r *Room) handleLocationUpdate(c *Connection, env Envelope) error {
	if env.FromUserID == nil || *env.FromUserID != c.userID {
		return wsErr(CloseSpoofedSender, "from_user_id mismatch")
	}
	var p LocationPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return wsErr(CloseMalformed, "bad location payload")
	}
	if err := validateLocation(p); err != nil {
		c.sendError("VALIDATION_FAILED", err.Error())
		return nil
	}

	// Pair-aware suppression: if this user is a paired pillion, accept
	// the position into our own state but DO NOT fan out (the rider's
	// updates carry the pair).
	if c.pairedAsPillionFor != uuid.Nil {
		// silent drop; a future revision may emit `error{DEGRADED}`.
		r.updatePresence(c.userID, LastLocation{Latitude: p.Latitude, Longitude: p.Longitude, At: time.Now().UTC()})
		return nil
	}

	r.updatePresence(c.userID, LastLocation{Latitude: p.Latitude, Longitude: p.Longitude, At: time.Now().UTC()})

	// If this user is the rider in an active pair, attach the pillion's
	// user_id so peers render both icons co-located.
	if c.pairedWithPillion != uuid.Nil {
		p.PairedUserIDs = []uuid.UUID{c.pairedWithPillion}
	}
	out, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal location: %w", err)
	}
	bcast := Envelope{
		Type:       TypeLocationBroadcast,
		RideID:     r.rideID,
		Timestamp:  time.Now().UTC(), // server-authoritative
		FromUserID: env.FromUserID,
		Payload:    out,
	}
	r.broadcast(bcast, &c.userID)
	return nil
}

// handleVoice forwards a SDP/ICE message verbatim to the addressed peer.
// Server validates schema, sender identity, and that the target is a
// connected member; never parses SDP/candidate body content.
func (r *Room) handleVoice(c *Connection, env Envelope) error {
	if env.FromUserID == nil || *env.FromUserID != c.userID {
		return wsErr(CloseSpoofedSender, "from_user_id mismatch")
	}
	if env.ToUserID == nil {
		c.sendError("VALIDATION_FAILED", "to_user_id required for voice messages")
		return nil
	}
	if *env.ToUserID == c.userID {
		c.sendError("VALIDATION_FAILED", "cannot signal yourself")
		return nil
	}
	var p VoicePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return wsErr(CloseMalformed, "bad voice payload")
	}
	// Size guards from docs/WEBSOCKET_PROTOCOL.md §3.11.
	if len(p.SDP) > 8*1024 {
		c.sendError("VALIDATION_FAILED", "sdp too large")
		return nil
	}
	if len(p.Candidate) > 1024 {
		c.sendError("VALIDATION_FAILED", "candidate too large")
		return nil
	}
	out := Envelope{
		Type:       env.Type,
		RideID:     r.rideID,
		Timestamp:  time.Now().UTC(),
		FromUserID: env.FromUserID,
		ToUserID:   env.ToUserID,
		Payload:    env.Payload,
	}
	if !r.sendTo(*env.ToUserID, out) {
		c.sendError("NOT_FOUND", "target peer not connected")
	}
	return nil
}

// validateLocation enforces the documented coordinate ranges and bike-
// reasonable speed/heading bounds. Out-of-range values are rejected so
// route compute won't spend a paid call on bad input later.
func validateLocation(p LocationPayload) error {
	if math.IsNaN(p.Latitude) || math.IsInf(p.Latitude, 0) || p.Latitude < -90 || p.Latitude > 90 {
		return errors.New("latitude out of range")
	}
	if math.IsNaN(p.Longitude) || math.IsInf(p.Longitude, 0) || p.Longitude < -180 || p.Longitude > 180 {
		return errors.New("longitude out of range")
	}
	if p.SpeedKmph != nil {
		v := *p.SpeedKmph
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 120 {
			return errors.New("speed_kmph out of range")
		}
	}
	if p.Heading != nil {
		v := *p.Heading
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= 360 {
			return errors.New("heading out of range")
		}
	}
	if p.AccuracyMeters != nil {
		v := *p.AccuracyMeters
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1000 {
			return errors.New("accuracy_meters out of range")
		}
	}
	if p.BatteryPercent != nil {
		b := *p.BatteryPercent
		if b < 0 || b > 100 {
			return errors.New("battery_percent out of range")
		}
	}
	return nil
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
