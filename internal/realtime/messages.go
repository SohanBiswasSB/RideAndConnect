// Package realtime implements the WebSocket Hub, Room, and Connection
// types that power the per-ride realtime channel. It exposes the
// presence store consumed by the routes service for peerRoute.
//
// Wire encoding: CBOR (default) or JSON (fallback) per WS subprotocol
// negotiation; permessage-deflate is layered on top in the upgrade
// handler. Field-name shortening on the location hot path keeps
// per-frame bytes small even with JSON.
package realtime

import (
	"time"

	"github.com/google/uuid"
)

// Envelope is the shared shape for every WS message. Field tags use
// short keys so JSON-encoded frames stay small (CBOR encodes by tag
// number internally and is already compact).
//
// Outbound (server→client) envelopes always carry timestamp and seq.
// Inbound (client→server) envelopes may omit them — the server stamps
// authoritatively on broadcast.
type Envelope struct {
	Type        string         `json:"t" cbor:"1,keyasint"`
	RideID      uuid.UUID      `json:"r" cbor:"2,keyasint"`
	Timestamp   time.Time      `json:"ts,omitempty" cbor:"3,keyasint,omitempty"`
	FromUserID  *uuid.UUID     `json:"f,omitempty"  cbor:"4,keyasint,omitempty"`
	ToUserID    *uuid.UUID     `json:"to,omitempty" cbor:"5,keyasint,omitempty"`
	Seq         uint64         `json:"s,omitempty"  cbor:"6,keyasint,omitempty"`
	Payload     any            `json:"p"            cbor:"7,keyasint"`
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
	TypeNetworkAdvisory     = "network.advisory"
	TypeSystemHello         = "system.hello"
	TypeError               = "error"
)

// LocationPayload carries position data on the hot path. Field tags are
// deliberately short — `lat` instead of `latitude`, etc. — to save ~25
// bytes per frame on JSON without changing semantics.
type LocationPayload struct {
	Latitude       float64     `json:"lat"            cbor:"1,keyasint"`
	Longitude      float64     `json:"lng"            cbor:"2,keyasint"`
	SpeedKmph      *float64    `json:"spd,omitempty"  cbor:"3,keyasint,omitempty"`
	Heading        *float64    `json:"hdg,omitempty"  cbor:"4,keyasint,omitempty"`
	AccuracyMeters *float64    `json:"acc,omitempty"  cbor:"5,keyasint,omitempty"`
	BatteryPercent *int        `json:"bat,omitempty"  cbor:"6,keyasint,omitempty"`
	PairedUserIDs  []uuid.UUID `json:"pair,omitempty" cbor:"7,keyasint,omitempty"`
}

// VoicePayload is the SDP/ICE pass-through. Server validates schema +
// size only; never parses contents.
type VoicePayload struct {
	SDP           string `json:"sdp,omitempty"   cbor:"1,keyasint,omitempty"`
	Candidate     string `json:"cand,omitempty"  cbor:"2,keyasint,omitempty"`
	SDPMid        string `json:"mid,omitempty"   cbor:"3,keyasint,omitempty"`
	SDPMLineIndex int    `json:"mli,omitempty"   cbor:"4,keyasint,omitempty"`
	SessionID     string `json:"sid,omitempty"   cbor:"5,keyasint,omitempty"`
}

// EmergencyPayload accompanies emergency.triggered messages.
type EmergencyPayload struct {
	EventID         uuid.UUID  `json:"id"               cbor:"1,keyasint"`
	TriggeredBy     uuid.UUID  `json:"by"               cbor:"2,keyasint"`
	TriggeredByName string     `json:"name,omitempty"   cbor:"3,keyasint,omitempty"`
	Message         *string    `json:"msg,omitempty"    cbor:"4,keyasint,omitempty"`
}

// PresencePayload is the body of rider.online / rider.offline.
type PresencePayload struct {
	UserID uuid.UUID `json:"u"            cbor:"1,keyasint"`
	Name   string    `json:"n,omitempty"  cbor:"2,keyasint,omitempty"`
}

// ErrorPayload is the body of `error` messages (recoverable issues).
type ErrorPayload struct {
	Code    string `json:"code"          cbor:"1,keyasint"`
	Message string `json:"msg,omitempty" cbor:"2,keyasint,omitempty"`
}

// HelloPayload is what the server sends immediately after a successful
// upgrade. CurrentSeq lets a reconnecting client see how many messages
// it might have missed; client compares against its last-seen seq and
// asks for a snapshot if the gap is non-zero.
type HelloPayload struct {
	UserID         uuid.UUID `json:"u"               cbor:"1,keyasint"`
	PublishNow     bool      `json:"publish"         cbor:"2,keyasint"`
	PingIntervalMS int       `json:"ping_ms"         cbor:"3,keyasint"`
	CurrentSeq     uint64    `json:"seq"             cbor:"4,keyasint"`
	Tier           string    `json:"tier"            cbor:"5,keyasint"`
}

// AdvisoryPayload accompanies network.advisory messages. Tier moves
// trigger client-side cadence changes; the server-side caps are
// enforced regardless.
type AdvisoryPayload struct {
	Tier                        string `json:"tier"          cbor:"1,keyasint"`
	RTTMSP95                    int    `json:"rtt_ms_p95"    cbor:"2,keyasint"`
	RecommendedLocationHz       float64 `json:"loc_hz"       cbor:"3,keyasint"`
	RecommendedVoiceBitrateKbps int    `json:"voice_kbps"    cbor:"4,keyasint"`
	VoiceSignalingAllowed       bool   `json:"voice_ok"      cbor:"5,keyasint"`
	Reason                      string `json:"reason"        cbor:"6,keyasint"`
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

// ─── tiers ──────────────────────────────────────────────────────────────

// Tier names for AdvisoryPayload.Tier and Connection.Tier.
const (
	TierHigh     = "high"
	TierMedium   = "medium"
	TierLow      = "low"
	TierCritical = "critical"
)
