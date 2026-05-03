// Package realtime implements the WebSocket Hub, Room, and Connection
// types that power the per-ride realtime channel. It exposes the
// presence store consumed by the routes service for peerRoute.
//
// Phase 7 status: the core (auth on upgrade, location.update + broadcast
// with pair awareness, voice signaling relay, heartbeat, emergency push)
// is implemented with a JSON codec. TOON, permessage-deflate, the
// strict-priority scheduler, and bandwidth tiers are designed in
// docs/WEBSOCKET_PROTOCOL.md and slot into the codec / scheduler seams.
package realtime

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Envelope is the shared shape for every WS message. See
// docs/WEBSOCKET_PROTOCOL.md §2 for the full contract.
type Envelope struct {
	Type        string          `json:"type"`
	RideID      uuid.UUID       `json:"ride_id"`
	Timestamp   time.Time       `json:"timestamp"`
	FromUserID  *uuid.UUID      `json:"from_user_id,omitempty"`
	ToUserID    *uuid.UUID      `json:"to_user_id,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

// Message types. Strings exactly match docs/WEBSOCKET_PROTOCOL.md §3.1.
const (
	TypePing                = "ping"
	TypePong                = "pong"
	TypeLocationUpdate      = "location.update"
	TypeLocationBroadcast   = "location.broadcast"
	TypeVoiceOffer          = "voice.offer"
	TypeVoiceAnswer         = "voice.answer"
	TypeVoiceICE            = "voice.ice_candidate"
	TypeRiderOnline         = "rider.online"
	TypeRiderOffline        = "rider.offline"
	TypeMemberJoined        = "member.joined"
	TypeMemberLeft          = "member.left"
	TypeRideEnded           = "ride.ended"
	TypeDestinationUpdated  = "destination.updated"
	TypeEmergencyTriggered  = "emergency.triggered"
	TypeSystemHello         = "system.hello"
	TypeError               = "error"
)

// HelloPayload is what the server sends immediately after a successful
// upgrade. Clients should respond by publishing their current location
// (rather than waiting for their next sample tick) so peerRoute and the
// map view come back online quickly after a reconnect.
type HelloPayload struct {
	UserID         uuid.UUID `json:"user_id"`
	PublishNow     bool      `json:"publish_now"`
	PingIntervalMS int       `json:"ping_interval_ms"`
}

// LocationPayload is what location.update / location.broadcast carry.
// Server-emitted broadcasts carry PairedUserIDs when the rider has an
// active pillion.
type LocationPayload struct {
	Latitude       float64     `json:"latitude"`
	Longitude      float64     `json:"longitude"`
	SpeedKmph      *float64    `json:"speed_kmph,omitempty"`
	Heading        *float64    `json:"heading,omitempty"`
	AccuracyMeters *float64    `json:"accuracy_meters,omitempty"`
	BatteryPercent *int        `json:"battery_percent,omitempty"`
	PairedUserIDs  []uuid.UUID `json:"paired_user_ids,omitempty"`
}

// VoicePayload is the SDP/ICE pass-through. Server validates schema +
// size only; never parses contents.
type VoicePayload struct {
	SDP             string `json:"sdp,omitempty"`
	Candidate       string `json:"candidate,omitempty"`
	SDPMid          string `json:"sdp_mid,omitempty"`
	SDPMLineIndex   int    `json:"sdp_m_line_index,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
}

// EmergencyPayload accompanies emergency.triggered messages.
type EmergencyPayload struct {
	EventID         uuid.UUID  `json:"event_id"`
	TriggeredBy     uuid.UUID  `json:"triggered_by_user_id"`
	TriggeredByName string     `json:"triggered_by_name,omitempty"`
	Message         *string    `json:"message,omitempty"`
}

// PresencePayload is the body of rider.online / rider.offline.
type PresencePayload struct {
	UserID uuid.UUID `json:"user_id"`
	Name   string    `json:"name,omitempty"`
}

// ErrorPayload is the body of `error` messages (recoverable issues).
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Close codes — the documented WebSocket close codes from
// docs/WEBSOCKET_PROTOCOL.md §4. Implementations use these constants
// rather than literals so the doc and code stay in lockstep.
const (
	CloseRideEnded         = 4001
	CloseUnauthorized      = 4002
	CloseForbidden         = 4003
	CloseRateLimited       = 4004
	CloseIdleTimeout       = 4006
	CloseSpoofedSender     = 4007
	CloseSlowConsumer      = 4008
	CloseSuperseded        = 4009
	CloseMalformed         = 4010
	CloseMessageTooLarge   = 4011
	CloseRideMismatch      = 4012
	CloseUnsupportedType   = 4014
)

// LastLocation is the in-memory presence record per (ride, user). Read by
// the routes service to power peerRoute.
type LastLocation struct {
	Latitude  float64
	Longitude float64
	At        time.Time
}
