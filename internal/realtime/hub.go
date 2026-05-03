package realtime

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ConnectionMetrics is the small surface the Hub uses to feed
// observability counters. Pass nil to disable.
type ConnectionMetrics interface {
	WSConnectionOpened()
	WSConnectionClosed()
	IncWSMessage(msgType, direction string)
}

// Hub owns one Room per active ride. Rooms are created lazily on first
// connection and torn down ~30 s after the last connection closes.
type Hub struct {
	mu      sync.RWMutex
	rooms   map[uuid.UUID]*Room
	log     zerolog.Logger
	metrics ConnectionMetrics
}

// NewHub builds a fresh Hub.
func NewHub(log zerolog.Logger) *Hub {
	return &Hub{
		rooms: make(map[uuid.UUID]*Room),
		log:   log.With().Str("component", "ws_hub").Logger(),
	}
}

// SetMetrics wires the observability counters. Optional — nil is a
// safe no-op.
func (h *Hub) SetMetrics(m ConnectionMetrics) { h.metrics = m }

// MetricsSink returns the connection metrics sink (or nil) so the
// upgrade handler can pass it to each Connection.
func (h *Hub) MetricsSink() ConnectionMetrics { return h.metrics }

// getOrCreateRoom returns the Room for a ride, creating it if absent.
func (h *Hub) getOrCreateRoom(rideID uuid.UUID) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.rooms[rideID]; ok {
		return r
	}
	r := newRoom(rideID, h.log)
	h.rooms[rideID] = r
	return r
}

// dropRoom removes the room for a ride from the registry. Called by Room
// once it has drained.
func (h *Hub) dropRoom(rideID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.rooms, rideID)
}

// LookupPresence is the seam consumed by the routes service.
func (h *Hub) LookupPresence(rideID, userID uuid.UUID) (LastLocation, bool) {
	h.mu.RLock()
	r, ok := h.rooms[rideID]
	h.mu.RUnlock()
	if !ok {
		return LastLocation{}, false
	}
	return r.lookupPresence(userID)
}

// BroadcastEmergency pushes an emergency.triggered envelope to every
// connected member of the ride (excluding the trigger themselves).
func (h *Hub) BroadcastEmergency(rideID, triggeredBy uuid.UUID, eventID uuid.UUID, triggeredByName string, message *string) {
	h.mu.RLock()
	r, ok := h.rooms[rideID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	env := Envelope{
		Type:       TypeEmergencyTriggered,
		RideID:     rideID,
		Timestamp:  time.Now().UTC(),
		FromUserID: &triggeredBy,
		Payload: EmergencyPayload{
			EventID:         eventID,
			TriggeredBy:     triggeredBy,
			TriggeredByName: triggeredByName,
			Message:         message,
		},
	}
	r.broadcast(env, &triggeredBy)
}

// BroadcastDestination pushes destination.updated to every connected
// member. Called from rides.SetDestination after commit.
func (h *Hub) BroadcastDestination(rideID, byUser uuid.UUID, name string, lat, lng float64) {
	h.mu.RLock()
	r, ok := h.rooms[rideID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	env := Envelope{
		Type:      TypeDestinationUpdated,
		RideID:    rideID,
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"destination": map[string]any{
				"name": name, "lat": lat, "lng": lng,
			},
			"by": byUser,
		},
	}
	r.broadcast(env, nil)
}

// BroadcastRideEnded fans out a final ride.ended message and instructs
// every connection to close with the documented code.
func (h *Hub) BroadcastRideEnded(rideID, endedBy uuid.UUID) {
	h.mu.RLock()
	r, ok := h.rooms[rideID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	env := Envelope{
		Type:      TypeRideEnded,
		RideID:    rideID,
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"by": endedBy, "reason": "owner_ended"},
	}
	r.broadcast(env, nil)
	r.closeAll(CloseRideEnded, "ride ended")
}

// Stop closes every room and clears the registry. Idempotent.
func (h *Hub) Stop(ctx context.Context) {
	h.mu.Lock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	h.rooms = make(map[uuid.UUID]*Room)
	h.mu.Unlock()
	for _, r := range rooms {
		r.closeAll(1001, "server shutdown")
	}
}
