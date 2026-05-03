package realtime

import (
	"sync"

	"github.com/google/uuid"
)

// priority is the connection-level send priority assigned by typeToPriority.
// P0 (location + lifecycle) ALWAYS wins over P1 (control) which ALWAYS
// wins over P2 (voice signaling) — never round-robin. At the 1 kbps
// tier, location is what keeps clients useful; voice can be shed.
type priority int

const (
	priorityP0 priority = 0 // location, lifecycle, advisories, errors, hello
	priorityP1 priority = 1 // control: destination/route updates, ping/pong, presence
	priorityP2 priority = 2 // voice signaling
)

// typeToPriority maps a message Type to its scheduler priority.
// Anything unrecognised lands in P1 — safe default; not silently
// elevated above location traffic.
func typeToPriority(msgType string) priority {
	switch msgType {
	case TypeLocationBroadcast, TypeLocationUpdate,
		TypeRideEnded, TypeMemberJoined, TypeMemberLeft,
		TypeNetworkAdvisory, TypeError, TypeSystemHello:
		return priorityP0
	case TypeVoiceOffer, TypeVoiceAnswer, TypeVoiceICE:
		return priorityP2
	default: // ping, pong, destination.updated, route.updated, rider.online/offline
		return priorityP1
	}
}

// queueCaps from docs/WEBSOCKET_PROTOCOL.md §10.2.
const (
	capP0 = 32
	capP1 = 16
	capP2 = 16
)

// priorityBuffer is a slice-backed FIFO with two non-FIFO behaviours:
//
//  1. When a `location.broadcast` is pushed and the buffer is over the
//     coalesce threshold, an existing `location.broadcast` from the same
//     `from_user_id` is replaced in-place rather than appended. This
//     keeps the latest position from each peer without growing the
//     queue.
//
//  2. When voice frames arrive at `critical` tier, the upstream code
//     refuses the push outright — buffer never sees them.
//
// Channels can't iterate, hence slice + mutex. The signal channel is
// unbuffered-but-non-blocking so multiple producers collapse to a
// single readiness signal that the writeLoop can select on.
type priorityBuffer struct {
	mu     sync.Mutex
	items  []Envelope
	cap    int
	signal chan struct{}
}

func newPriorityBuffer(cap int) *priorityBuffer {
	return &priorityBuffer{
		items:  make([]Envelope, 0, cap),
		cap:    cap,
		signal: make(chan struct{}, 1),
	}
}

// Push enqueues env. Returns false if the buffer is full and no
// coalescing was possible — the caller treats that as a slow-consumer
// condition and closes the connection.
//
// The coalesceThreshold is intentionally lower than cap so we start
// dropping stale broadcasts before the queue actually fills.
func (b *priorityBuffer) Push(env Envelope) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Coalesce: when ≥ 75% full and the inbound is a location.broadcast,
	// look for an older broadcast from the same sender and replace it
	// in-place. This keeps `latest` semantics without queue growth.
	if env.Type == TypeLocationBroadcast && len(b.items) >= b.cap*3/4 {
		for i := range b.items {
			if b.items[i].Type == TypeLocationBroadcast && eqUserPtr(b.items[i].FromUserID, env.FromUserID) {
				b.items[i] = env
				b.signalNonBlocking()
				return true
			}
		}
	}

	if len(b.items) >= b.cap {
		return false
	}
	b.items = append(b.items, env)
	b.signalNonBlocking()
	return true
}

// Pop removes and returns the head. Returns ok=false on empty queue.
func (b *priorityBuffer) Pop() (Envelope, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return Envelope{}, false
	}
	env := b.items[0]
	// Avoid memory retention for large items: shift instead of using
	// items[1:] (which keeps the underlying array's first slot reachable).
	copy(b.items, b.items[1:])
	b.items = b.items[:len(b.items)-1]
	return env, true
}

// Len returns the current queue depth (held under the mutex).
func (b *priorityBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// Signal returns a channel that receives a non-blocking notification
// when an item is available. Spurious wakeups are possible (caller
// must Pop to confirm).
func (b *priorityBuffer) Signal() <-chan struct{} { return b.signal }

func (b *priorityBuffer) signalNonBlocking() {
	select {
	case b.signal <- struct{}{}:
	default:
	}
}

func eqUserPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
