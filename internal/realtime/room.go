package realtime

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Room is a per-ride fan-out point. It owns the connection registry and
// the in-memory presence store consumed by peerRoute.
type Room struct {
	rideID uuid.UUID
	log    zerolog.Logger

	mu       sync.RWMutex
	conns    map[uuid.UUID]*Connection // user_id → connection
	presence map[uuid.UUID]LastLocation
}

func newRoom(rideID uuid.UUID, log zerolog.Logger) *Room {
	return &Room{
		rideID:   rideID,
		log:      log.With().Str("ride_id", rideID.String()).Logger(),
		conns:    make(map[uuid.UUID]*Connection),
		presence: make(map[uuid.UUID]LastLocation),
	}
}

// register adds a new connection. If a previous connection exists for the
// same user, it is closed with CloseSuperseded — the latest connection
// wins per docs/WEBSOCKET_PROTOCOL.md §1.2.
func (r *Room) register(c *Connection) {
	r.mu.Lock()
	prev, ok := r.conns[c.userID]
	r.conns[c.userID] = c
	r.mu.Unlock()
	if ok && prev != nil {
		prev.closeWith(CloseSuperseded, "superseded by newer connection")
	}
}

// unregister removes a connection. Idempotent.
func (r *Room) unregister(c *Connection) {
	r.mu.Lock()
	if cur, ok := r.conns[c.userID]; ok && cur == c {
		delete(r.conns, c.userID)
	}
	r.mu.Unlock()
}

// updatePresence stores the latest position for a user.
func (r *Room) updatePresence(userID uuid.UUID, loc LastLocation) {
	r.mu.Lock()
	r.presence[userID] = loc
	r.mu.Unlock()
}

// lookupPresence returns the latest known position for a user.
func (r *Room) lookupPresence(userID uuid.UUID) (LastLocation, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	loc, ok := r.presence[userID]
	return loc, ok
}

// broadcast sends an envelope to every connection except the optional
// excludeUser. Best-effort: a slow consumer is dropped (Connection's
// send queue is bounded) rather than blocking the room.
func (r *Room) broadcast(env Envelope, excludeUser *uuid.UUID) {
	r.mu.RLock()
	targets := make([]*Connection, 0, len(r.conns))
	for uid, c := range r.conns {
		if excludeUser != nil && uid == *excludeUser {
			continue
		}
		targets = append(targets, c)
	}
	r.mu.RUnlock()
	for _, c := range targets {
		c.enqueue(env)
	}
}

// sendTo delivers a targeted envelope (e.g. voice signaling). Returns
// true if the target was found and the message was queued.
func (r *Room) sendTo(toUserID uuid.UUID, env Envelope) bool {
	r.mu.RLock()
	c, ok := r.conns[toUserID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return c.enqueue(env)
}

// closeAll terminates every connection in the room. Called from
// hub.BroadcastRideEnded and on shutdown.
func (r *Room) closeAll(code int, reason string) {
	r.mu.Lock()
	conns := make([]*Connection, 0, len(r.conns))
	for _, c := range r.conns {
		conns = append(conns, c)
	}
	r.conns = map[uuid.UUID]*Connection{}
	r.mu.Unlock()
	for _, c := range conns {
		c.closeWith(code, reason)
	}
}

// announcePresence is called when a connection completes its handshake
// (rider.online) or terminates (rider.offline). The body carries just
// the user_id; clients can fetch the name from rideMembers.
func (r *Room) announcePresence(presenceType string, userID uuid.UUID, name string) {
	body := PresencePayload{UserID: userID, Name: name}
	bodyBytes, _ := jsonMarshal(body)
	env := Envelope{
		Type:      presenceType,
		RideID:    r.rideID,
		Timestamp: time.Now().UTC(),
		Payload:   bodyBytes,
	}
	r.broadcast(env, &userID)
}

// peers returns the user_ids currently connected to this room.
func (r *Room) peers() []uuid.UUID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]uuid.UUID, 0, len(r.conns))
	for uid := range r.conns {
		out = append(out, uid)
	}
	return out
}

// purgePresenceFor drops the presence record for a user. Called on
// disconnect so peerRoute doesn't return stale positions for absent
// peers.
func (r *Room) purgePresenceFor(userID uuid.UUID) {
	r.mu.Lock()
	delete(r.presence, userID)
	r.mu.Unlock()
}

// idleEmptyDuration the room waits after the last connection leaves
// before the Hub may garbage-collect it. Currently unused — Rooms live
// for the process lifetime in v1; a sweeper goroutine will reclaim them
// in a future pass.
const idleEmptyDuration = 30 * time.Second

// dispatch is invoked by Connection.read on every inbound envelope. It
// validates membership and authorization, then either updates state or
// fans out, depending on type.
func (r *Room) dispatch(ctx context.Context, c *Connection, env Envelope) error {
	if env.RideID != r.rideID {
		return wsErr(CloseRideMismatch, "ride id mismatch")
	}
	switch env.Type {
	case TypePing:
		return r.handlePing(c, env)
	case TypeLocationUpdate:
		return r.handleLocationUpdate(c, env)
	case TypeVoiceOffer, TypeVoiceAnswer, TypeVoiceICE:
		return r.handleVoice(c, env)
	default:
		return wsErr(CloseUnsupportedType, "unsupported type")
	}
}
