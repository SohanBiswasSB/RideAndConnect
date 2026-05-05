package realtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Connection is one WS pipe to a single (user, ride). Read and write
// pumps run as goroutines coordinated by the connection's context.
type Connection struct {
	conn    *websocket.Conn
	codec   Codec
	userID  uuid.UUID
	name    string
	room    *Room
	log     zerolog.Logger
	metrics ConnectionMetrics

	// pairedWithPillion is set when this user is the RIDER in an active
	// pair; their broadcast carries the pillion's user_id.
	pairedWithPillion uuid.UUID
	// pairedAsPillionFor is set when this user is the PILLION in an
	// active pair; their location.update is silently suppressed.
	pairedAsPillionFor uuid.UUID

	// Priority queues replace the previous single sendCh. P0 always
	// wins over P1 which always wins over P2; voice is shed in
	// `critical` tier.
	p0, p1, p2 *priorityBuffer

	// Tier state. Stored as atomic.Value so the writer goroutine can
	// adjust per-tier behaviour (ping interval, location-rate limit)
	// without locks. Initial value is "high" — a fresh connection
	// gets the benefit of the doubt until the first RTT sample lands.
	tier atomic.Value // string

	// rttSamplesMu protects rttEWMA + nonceMap. The map is small (one
	// entry per outstanding ping) so contention is negligible.
	rttSamplesMu sync.Mutex
	rttEWMA      time.Duration            // exponentially-weighted RTT
	pingNonces   map[string]time.Time     // nonce → send time

	// inboundLoc is the per-connection token bucket for inbound
	// location.update messages. Cap and refill scale with tier.
	inboundLoc *tokenBucket

	closed   atomic.Bool
	closeCtx context.Context
	cancel   context.CancelFunc
}

// sendQueueCap left for backwards compatibility with prior tests; the
// real caps live in priority.go.
const sendQueueCap = capP0 + capP1 + capP2

// idleTimeout closes a connection that hasn't sent any traffic
// (including pong) within the window. Wire frames are capped at
// maxFrameSize (decompressed; compressed wire size is capped by the
// permessage-deflate negotiation).
const (
	idleTimeout  = 60 * time.Second
	maxFrameSize = 16 * 1024
)

// pingIntervalForTier returns the heartbeat cadence per the active
// tier (matches WS_PING_INTERVAL{,_LOW,_CRITICAL} in .env.example).
// At the 1 kbps floor, halving the heartbeat saves ~0.5 B/s — small
// but measurable when the budget is 125 B/s.
func pingIntervalForTier(t string) time.Duration {
	switch t {
	case TierCritical:
		return 60 * time.Second
	case TierLow:
		return 30 * time.Second
	default:
		return 25 * time.Second
	}
}

// inboundLocationRateForTier returns (capacity, refill_per_sec) for the
// per-connection inbound location.update token bucket. Excess publishes
// are silently dropped server-side rather than erroring, so the client
// doesn't waste bytes on the error response.
func inboundLocationRateForTier(t string) (cap int, refillPerSec float64) {
	switch t {
	case TierCritical:
		return 1, 0.2
	case TierLow:
		return 2, 0.5
	case TierMedium:
		return 2, 1.0
	default:
		return 4, 2.0
	}
}

// newConnection wires the goroutines and registers presence with the
// room. Caller is responsible for blocking on `Run` until the pumps
// complete.
func newConnection(parent context.Context, conn *websocket.Conn, codec Codec, room *Room, userID uuid.UUID, name string, pairedWith, pairedAs uuid.UUID, log zerolog.Logger, metrics ConnectionMetrics) *Connection {
	ctx, cancel := context.WithCancel(parent)
	c := &Connection{
		conn:               conn,
		codec:              codec,
		userID:             userID,
		name:               name,
		room:               room,
		metrics:            metrics,
		pairedWithPillion:  pairedWith,
		pairedAsPillionFor: pairedAs,
		p0:                 newPriorityBuffer(capP0),
		p1:                 newPriorityBuffer(capP1),
		p2:                 newPriorityBuffer(capP2),
		pingNonces:         make(map[string]time.Time, 4),
		closeCtx:           ctx,
		cancel:             cancel,
		log: log.With().
			Str("component", "ws_conn").
			Str("user_id", userID.String()).
			Str("ride_id", room.rideID.String()).
			Logger(),
	}
	c.tier.Store(TierHigh)
	cap, refill := inboundLocationRateForTier(TierHigh)
	c.inboundLoc = newTokenBucket(cap, refill)
	return c
}

// Run blocks until the connection terminates.
func (c *Connection) Run() {
	c.room.register(c)
	c.room.announcePresence(TypeRiderOnline, c.userID, c.name)
	c.sendHello()
	if c.metrics != nil {
		c.metrics.WSConnectionOpened()
	}
	defer func() {
		c.room.unregister(c)
		c.room.purgePresenceFor(c.userID)
		c.room.announcePresence(TypeRiderOffline, c.userID, c.name)
		if c.metrics != nil {
			c.metrics.WSConnectionClosed()
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.readLoop()
	}()
	c.writeLoop()
	c.cancel()
	<-done
}

// readLoop drains inbound frames; the docs cap wire size at maxFrameSize.
// Idle timeout is enforced via a per-Read context.
func (c *Connection) readLoop() {
	c.conn.SetReadLimit(maxFrameSize)
	for {
		ctx, cancel := context.WithTimeout(c.closeCtx, idleTimeout)
		_, data, err := c.conn.Read(ctx)
		cancel()
		if err != nil {
			if c.closeCtx.Err() != nil {
				return
			}
			c.closeWith(CloseIdleTimeout, "idle timeout")
			return
		}
		var env Envelope
		if err := c.codec.DecodeEnvelope(data, &env); err != nil {
			c.closeWith(CloseMalformed, "malformed envelope")
			return
		}
		if err := c.room.dispatch(c.closeCtx, c, env); err != nil {
			var werr wsClose
			if asWSClose(err, &werr) {
				c.closeWith(werr.code, werr.reason)
				return
			}
			c.log.Warn().Err(err).Msg("dispatch error")
		}
	}
}

// writeLoop is the strict-priority scheduler. Each iteration: fully
// drain P0, then P1, then P2. When all three are empty, block on any
// signal channel + ping timer + close.
//
// The non-blocking-then-blocking pattern keeps the implementation
// allocation-free and gives correct strict-priority semantics: P0
// always preempts P1/P2, and a steady stream of P0 messages means P2
// never starves location traffic.
func (c *Connection) writeLoop() {
	pingTimer := time.NewTimer(pingIntervalForTier(c.Tier()))
	defer pingTimer.Stop()

	for {
		if c.closeCtx.Err() != nil {
			return
		}

		// Strict priority: try the higher queues first non-blocking.
		if env, ok := c.p0.Pop(); ok {
			c.writeEnvelope(env)
			continue
		}
		if env, ok := c.p1.Pop(); ok {
			c.writeEnvelope(env)
			continue
		}
		if env, ok := c.p2.Pop(); ok {
			c.writeEnvelope(env)
			continue
		}

		// All empty — block on any signal + ping + close.
		select {
		case <-c.closeCtx.Done():
			return
		case <-c.p0.Signal():
		case <-c.p1.Signal():
		case <-c.p2.Signal():
		case <-pingTimer.C:
			c.emitPing()
			pingTimer.Reset(pingIntervalForTier(c.Tier()))
		}
	}
}

// emitPing builds a ping with a fresh nonce + send_ts and pushes it on
// P1. The pong handler uses the nonce to match and computes RTT.
func (c *Connection) emitPing() {
	nonce := uuid.NewString()[:8]
	now := time.Now().UTC()
	c.rttSamplesMu.Lock()
	c.pingNonces[nonce] = now
	// Drop stale entries: anything older than 2× idle timeout never
	// got a pong; remove to bound the map.
	for n, t := range c.pingNonces {
		if now.Sub(t) > 2*idleTimeout {
			delete(c.pingNonces, n)
		}
	}
	c.rttSamplesMu.Unlock()
	c.enqueue(Envelope{
		Type:      TypePing,
		RideID:    c.room.rideID,
		Timestamp: now,
		Payload:   map[string]any{"nonce": nonce, "send_ts": now.UnixMilli()},
	})
}

// recordPongRTT is called by the room dispatcher when a `pong` arrives.
// Looks up the nonce, computes round-trip, updates EWMA, possibly
// triggers a tier change.
func (c *Connection) recordPongRTT(nonce string) {
	now := time.Now().UTC()
	c.rttSamplesMu.Lock()
	sent, ok := c.pingNonces[nonce]
	if ok {
		delete(c.pingNonces, nonce)
	}
	c.rttSamplesMu.Unlock()
	if !ok {
		return
	}
	rtt := now.Sub(sent)
	// EWMA with α=0.3 — responsive enough for tier changes within ~3
	// pings, smoothing one-off spikes.
	c.rttSamplesMu.Lock()
	if c.rttEWMA == 0 {
		c.rttEWMA = rtt
	} else {
		c.rttEWMA = (c.rttEWMA*7 + rtt*3) / 10
	}
	current := c.rttEWMA
	c.rttSamplesMu.Unlock()
	c.maybeChangeTier(current)
}

// Tier returns the current bandwidth tier name.
func (c *Connection) Tier() string {
	if v := c.tier.Load(); v != nil {
		return v.(string)
	}
	return TierHigh
}

// maybeChangeTier applies the documented thresholds (ARCHITECTURE/PRD).
// A tier change re-tunes the inbound location bucket and emits a
// network.advisory to the client.
func (c *Connection) maybeChangeTier(rtt time.Duration) {
	var next string
	ms := rtt.Milliseconds()
	switch {
	case ms >= 1500:
		next = TierCritical
	case ms >= 600:
		next = TierLow
	case ms >= 250:
		next = TierMedium
	default:
		next = TierHigh
	}
	prev := c.Tier()
	if next == prev {
		return
	}
	c.tier.Store(next)
	if c.metrics != nil {
		c.metrics.IncWSTierTransition(prev, next)
	}
	cap, refill := inboundLocationRateForTier(next)
	c.inboundLoc.retune(cap, refill)
	c.log.Info().Str("tier", next).Int64("rtt_ms", ms).Msg("ws: tier changed")
	c.emitAdvisory(next, int(ms), "rtt_change")
}

// emitAdvisory pushes a network.advisory frame on P0 (it's lifecycle).
// Throttled by call-sites — emit at most once per tier change.
func (c *Connection) emitAdvisory(tier string, rttMS int, reason string) {
	body := AdvisoryPayload{
		Tier:                  tier,
		RTTMSP95:              rttMS,
		RecommendedLocationHz: recommendedLocHz(tier),
		RecommendedVoiceBitrateKbps: recommendedVoiceKbps(tier),
		VoiceSignalingAllowed: tier != TierCritical,
		Reason:                reason,
	}
	c.enqueue(Envelope{
		Type:      TypeNetworkAdvisory,
		RideID:    c.room.rideID,
		Timestamp: time.Now().UTC(),
		Payload:   body,
	})
}

func recommendedLocHz(tier string) float64 {
	switch tier {
	case TierCritical:
		return 0.2
	case TierLow:
		return 0.5
	case TierMedium:
		return 1.0
	default:
		return 2.0
	}
}

func recommendedVoiceKbps(tier string) int {
	switch tier {
	case TierCritical:
		return 0
	case TierLow:
		return 8
	case TierMedium:
		return 12
	default:
		return 16
	}
}

// writeEnvelope marshals + writes a single envelope using the
// negotiated codec. Each call carries its own ~5 s deadline so a
// stuck network doesn't pin the goroutine forever.
func (c *Connection) writeEnvelope(env Envelope) {
	ctx, cancel := context.WithTimeout(c.closeCtx, 5*time.Second)
	defer cancel()
	body, err := c.codec.Marshal(env)
	if err != nil {
		c.log.Warn().Err(err).Str("type", env.Type).Msg("marshal envelope")
		return
	}
	if err := c.conn.Write(ctx, c.codec.MessageType(), body); err != nil {
		c.log.Debug().Err(err).Msg("write error")
		c.cancel()
	}
	if c.metrics != nil {
		c.metrics.IncWSMessage(env.Type, "out")
	}
}

// enqueue routes the envelope to the right priority buffer. Returns
// false only when the buffer is genuinely full and no coalescing
// helped — caller treats that as a slow-consumer condition.
//
// In `critical` tier, voice messages are refused outright (P2
// shedding) — the byte budget can't accommodate signaling at <3 kbps.
func (c *Connection) enqueue(env Envelope) bool {
	if c.closed.Load() {
		return false
	}
	pri := typeToPriority(env.Type)
	if pri == priorityP2 && c.Tier() == TierCritical {
		// Voice signaling shed at critical tier per WS protocol §10.4.
		return false
	}

	var buf *priorityBuffer
	switch pri {
	case priorityP0:
		buf = c.p0
	case priorityP1:
		buf = c.p1
	default:
		buf = c.p2
	}
	if !buf.Push(env) {
		c.closeWith(CloseSlowConsumer, "send queue full")
		return false
	}
	return true
}

// sendError emits a recoverable error envelope on P0 (it's lifecycle).
func (c *Connection) sendError(code, msg string) {
	c.enqueue(Envelope{
		Type:      TypeError,
		RideID:    c.room.rideID,
		Timestamp: time.Now().UTC(),
		Payload:   ErrorPayload{Code: code, Message: msg},
	})
}

// sendHello is the first server-emitted frame after a successful
// upgrade. CurrentSeq lets the client see whether it missed broadcasts
// since the last connection (compared to its locally-cached seq).
func (c *Connection) sendHello() {
	c.enqueue(Envelope{
		Type:      TypeSystemHello,
		RideID:    c.room.rideID,
		Timestamp: time.Now().UTC(),
		Payload: HelloPayload{
			UserID:         c.userID,
			PublishNow:     true,
			PingIntervalMS: int(pingIntervalForTier(c.Tier()).Milliseconds()),
			CurrentSeq:     c.room.currentSeq(),
			Tier:           c.Tier(),
		},
	})
}

// closeWith terminates the connection with the documented close code +
// reason. Idempotent.
func (c *Connection) closeWith(code int, reason string) {
	if c.closed.Swap(true) {
		return
	}
	_ = c.conn.Close(websocket.StatusCode(code), reason)
	c.cancel()
}

// payloadAs decodes the inbound payload bytes captured by
// Codec.DecodeEnvelope directly into out — no marshal round-trip. The
// fmt import is retained for other call sites that wrap codec errors.
func (c *Connection) payloadAs(env Envelope, out any) error {
	if len(env.RawPayload) == 0 {
		return nil
	}
	if err := c.codec.Unmarshal(env.RawPayload, out); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	return nil
}
