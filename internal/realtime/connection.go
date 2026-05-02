package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// sendQueueCap is the bounded capacity of each connection's outbound
// queue. A full queue closes the connection with CloseSlowConsumer rather
// than blocking the room.
const sendQueueCap = 64

// pingInterval is the heartbeat cadence. idleTimeout closes a connection
// that hasn't sent any traffic (including pong) within the window.
const (
	pingInterval = 25 * time.Second
	idleTimeout  = 60 * time.Second
	maxFrameSize = 16 * 1024 // wire bytes; decoded payload still capped by client behavior
)

// Connection is one WS pipe to a single (user, ride). Read and write
// pumps run as goroutines coordinated by the connection's context.
type Connection struct {
	conn   *websocket.Conn
	userID uuid.UUID
	name   string
	room   *Room
	log    zerolog.Logger

	// pairedWithPillion is set when this user is the RIDER in an active
	// pair on this ride; their location.broadcast carries the pillion's
	// user_id so peers render co-located.
	pairedWithPillion uuid.UUID
	// pairedAsPillionFor is set when this user is the PILLION in an
	// active pair; their location.update is silently suppressed.
	pairedAsPillionFor uuid.UUID

	sendCh   chan Envelope
	closed   atomic.Bool
	closeCtx context.Context
	cancel   context.CancelFunc
}

// newConnection wires the goroutines and registers presence with the
// room. Caller is responsible for blocking on `Run` until the pumps
// complete.
func newConnection(parent context.Context, conn *websocket.Conn, room *Room, userID uuid.UUID, name string, pairedWith, pairedAs uuid.UUID, log zerolog.Logger) *Connection {
	ctx, cancel := context.WithCancel(parent)
	return &Connection{
		conn:               conn,
		userID:             userID,
		name:               name,
		room:               room,
		pairedWithPillion:  pairedWith,
		pairedAsPillionFor: pairedAs,
		sendCh:             make(chan Envelope, sendQueueCap),
		closeCtx:           ctx,
		cancel:             cancel,
		log: log.With().
			Str("component", "ws_conn").
			Str("user_id", userID.String()).
			Str("ride_id", room.rideID.String()).
			Logger(),
	}
}

// Run blocks until the connection terminates. Spawns the read and write
// pumps and waits for either to exit.
func (c *Connection) Run() {
	c.room.register(c)
	c.room.announcePresence(TypeRiderOnline, c.userID, c.name)
	defer func() {
		c.room.unregister(c)
		c.room.purgePresenceFor(c.userID)
		c.room.announcePresence(TypeRiderOffline, c.userID, c.name)
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
// Idle timeout is enforced by SetReadDeadline-equivalent: each Read uses
// a context with a fresh idleTimeout deadline.
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
			// Idle / read error → close cleanly.
			c.closeWith(CloseIdleTimeout, "idle timeout")
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
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

// writeLoop drains the send queue and emits pings on a timer. A full
// queue isn't possible here (drains as fast as we read), but we still
// honor closeCtx for graceful shutdown.
func (c *Connection) writeLoop() {
	pingT := time.NewTicker(pingInterval)
	defer pingT.Stop()
	for {
		select {
		case <-c.closeCtx.Done():
			return
		case env, ok := <-c.sendCh:
			if !ok {
				return
			}
			if err := c.writeEnvelope(env); err != nil {
				c.log.Debug().Err(err).Msg("write error")
				return
			}
		case <-pingT.C:
			env := Envelope{
				Type:      TypePing,
				RideID:    c.room.rideID,
				Timestamp: time.Now().UTC(),
				Payload:   json.RawMessage(`{}`),
			}
			if err := c.writeEnvelope(env); err != nil {
				return
			}
		}
	}
}

func (c *Connection) writeEnvelope(env Envelope) error {
	ctx, cancel := context.WithTimeout(c.closeCtx, 5*time.Second)
	defer cancel()
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return c.conn.Write(ctx, websocket.MessageText, body)
}

// enqueue tries to deliver an envelope; returns false on a full queue,
// in which case the connection is closed with CloseSlowConsumer.
func (c *Connection) enqueue(env Envelope) bool {
	if c.closed.Load() {
		return false
	}
	select {
	case c.sendCh <- env:
		return true
	default:
		c.closeWith(CloseSlowConsumer, "send queue full")
		return false
	}
}

// sendError emits a recoverable error envelope to this connection only.
// Used for VALIDATION_FAILED and similar non-terminal issues.
func (c *Connection) sendError(code, msg string) {
	body, _ := json.Marshal(ErrorPayload{Code: code, Message: msg})
	c.enqueue(Envelope{
		Type:      TypeError,
		RideID:    c.room.rideID,
		Timestamp: time.Now().UTC(),
		Payload:   body,
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
