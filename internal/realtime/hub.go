package realtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Hub owns one Room per active ride. Rooms are created lazily on first
// connection and torn down ~30 s after the last connection closes.
type Hub struct {
	mu    sync.RWMutex
	rooms map[uuid.UUID]*Room
	log   zerolog.Logger
}

// NewHub builds a fresh Hub.
func NewHub(log zerolog.Logger) *Hub {
	return &Hub{
		rooms: make(map[uuid.UUID]*Room),
		log:   log.With().Str("component", "ws_hub").Logger(),
	}
}

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

// LookupPresence is the seam consumed by the routes service. Returns the
// last-known location for a (ride, user) pair, or false when no fresh
// sample is available.
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
// connected member of the ride (excluding the trigger themselves, who
// already knows). Called from the rides service after the row is
// committed.
func (h *Hub) BroadcastEmergency(rideID, triggeredBy uuid.UUID, eventID uuid.UUID, triggeredByName string, message *string) {
	body, err := json.Marshal(EmergencyPayload{
		EventID:         eventID,
		TriggeredBy:     triggeredBy,
		TriggeredByName: triggeredByName,
		Message:         message,
	})
	if err != nil {
		h.log.Error().Err(err).Msg("emergency: marshal payload")
		return
	}
	env := Envelope{
		Type:       TypeEmergencyTriggered,
		RideID:     rideID,
		Timestamp:  time.Now().UTC(),
		FromUserID: &triggeredBy,
		Payload:    body,
	}
	h.mu.RLock()
	r, ok := h.rooms[rideID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	r.broadcast(env, &triggeredBy)
}

// BroadcastDestination pushes destination.updated to every connected
// member. Called from rides.SetDestination after commit.
func (h *Hub) BroadcastDestination(rideID, byUser uuid.UUID, name string, lat, lng float64) {
	body, err := json.Marshal(map[string]any{
		"destination": map[string]any{
			"name":      name,
			"latitude":  lat,
			"longitude": lng,
		},
		"set_by_user_id": byUser,
	})
	if err != nil {
		return
	}
	env := Envelope{
		Type:      TypeDestinationUpdated,
		RideID:    rideID,
		Timestamp: time.Now().UTC(),
		Payload:   body,
	}
	h.mu.RLock()
	r, ok := h.rooms[rideID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	r.broadcast(env, nil)
}

// BroadcastRideEnded fans out a final ride.ended message and instructs
// every connection to close with the documented code. Called from
// rides.End after commit.
func (h *Hub) BroadcastRideEnded(rideID, endedBy uuid.UUID) {
	body, _ := json.Marshal(map[string]any{
		"ended_by_user_id": endedBy,
		"reason":           "owner_ended",
	})
	env := Envelope{
		Type:      TypeRideEnded,
		RideID:    rideID,
		Timestamp: time.Now().UTC(),
		Payload:   body,
	}
	h.mu.RLock()
	r, ok := h.rooms[rideID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	r.broadcast(env, nil)
	r.closeAll(CloseRideEnded, "ride ended")
}

// Stop closes every room and clears the registry. Called during
// graceful shutdown. Idempotent — calling twice is harmless.
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
