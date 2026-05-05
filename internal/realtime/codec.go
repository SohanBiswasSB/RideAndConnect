package realtime

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
)

// Codec wires the on-wire framing for one connection. Each connection
// runs exactly one codec for its lifetime; selection happens via WS
// subprotocol negotiation in the upgrade handler.
//
// CBOR (the default) ships ~30–50% fewer wire bytes than JSON for our
// envelope shape and avoids the JSON string-escape tax on the location
// hot path. JSON stays as the universal fallback for clients without a
// CBOR decoder. A future TOON binding plugs into this same interface.
type Codec interface {
	// MessageType returns the websocket frame type for this codec.
	// CBOR is binary; JSON is text.
	MessageType() websocket.MessageType

	// Marshal serializes v to wire bytes.
	Marshal(v any) ([]byte, error)

	// Unmarshal parses wire bytes into v.
	Unmarshal(data []byte, v any) error

	// DecodeEnvelope parses an inbound Envelope, capturing the raw
	// payload bytes in env.RawPayload instead of decoding Payload into
	// a generic value. This lets handlers call Unmarshal(env.RawPayload,
	// &typed) once, replacing the previous marshal-then-unmarshal
	// round-trip on every inbound message.
	DecodeEnvelope(data []byte, env *Envelope) error

	// Subprotocol returns the value the server echoed in
	// Sec-WebSocket-Protocol so clients know which codec to use.
	Subprotocol() string
}

// ─── codec names (subprotocol values) ───────────────────────────────────

const (
	SubprotoCBOR = "rideandconnect.cbor.v1"
	SubprotoJSON = "rideandconnect.json.v1"
)

// SupportedSubprotocols is the server's preference order: CBOR first,
// JSON fallback. The upgrade handler offers these to clients via the
// AcceptOptions.Subprotocols field; the negotiated value flows through
// to NewCodec.
var SupportedSubprotocols = []string{SubprotoCBOR, SubprotoJSON}

// NewCodec returns the Codec implementation for a negotiated
// subprotocol. An empty negotiated value falls back to CBOR (safer for
// the low-bandwidth target) — clients that didn't negotiate are
// expected to handle whatever the server picks.
func NewCodec(subprotocol string) Codec {
	switch subprotocol {
	case SubprotoJSON:
		return jsonCodec{}
	case SubprotoCBOR, "":
		return cborCodec{}
	default:
		return cborCodec{}
	}
}

// ─── JSON ────────────────────────────────────────────────────────────────

type jsonCodec struct{}

func (jsonCodec) MessageType() websocket.MessageType { return websocket.MessageText }
func (jsonCodec) Marshal(v any) ([]byte, error)       { return json.Marshal(v) }
func (jsonCodec) Unmarshal(b []byte, v any) error     { return json.Unmarshal(b, v) }
func (jsonCodec) Subprotocol() string                  { return SubprotoJSON }

// jsonHeader mirrors Envelope's header fields but keeps the payload as
// raw bytes; used by jsonCodec.DecodeEnvelope to avoid the
// generic-value round-trip the old payloadAs path required.
type jsonHeader struct {
	Type       string          `json:"t"`
	RideID     uuid.UUID       `json:"r"`
	Timestamp  time.Time       `json:"ts,omitempty"`
	FromUserID *uuid.UUID      `json:"f,omitempty"`
	ToUserID   *uuid.UUID      `json:"to,omitempty"`
	Seq        uint64          `json:"s,omitempty"`
	Payload    json.RawMessage `json:"p"`
}

func (jsonCodec) DecodeEnvelope(data []byte, env *Envelope) error {
	var h jsonHeader
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}
	env.Type = h.Type
	env.RideID = h.RideID
	env.Timestamp = h.Timestamp
	env.FromUserID = h.FromUserID
	env.ToUserID = h.ToUserID
	env.Seq = h.Seq
	env.Payload = nil
	if len(h.Payload) > 0 {
		env.RawPayload = append(env.RawPayload[:0], h.Payload...)
	} else {
		env.RawPayload = nil
	}
	return nil
}

// ─── CBOR ────────────────────────────────────────────────────────────────

// cborEncOpts are tuned for tight wire bytes: we use the canonical
// shortest-form integer encoding (CTAP2 style), drop nil map values, and
// keep CBOR's stable map-key sort so two equivalent envelopes round-trip
// to the same bytes (helpful for cache/digest paths if we add them).
var cborEncMode = func() cbor.EncMode {
	em, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("cbor encmode init: %v", err))
	}
	return em
}()

var cborDecMode = func() cbor.DecMode {
	dm, err := cbor.DecOptions{}.DecMode()
	if err != nil {
		panic(fmt.Sprintf("cbor decmode init: %v", err))
	}
	return dm
}()

type cborCodec struct{}

func (cborCodec) MessageType() websocket.MessageType { return websocket.MessageBinary }
func (cborCodec) Marshal(v any) ([]byte, error)       { return cborEncMode.Marshal(v) }
func (cborCodec) Unmarshal(b []byte, v any) error     { return cborDecMode.Unmarshal(b, v) }
func (cborCodec) Subprotocol() string                  { return SubprotoCBOR }

// cborHeader mirrors Envelope's keyasint layout but captures the
// payload as RawMessage so DecodeEnvelope avoids the generic-value
// round-trip the previous payloadAs path required.
type cborHeader struct {
	Type       string          `cbor:"1,keyasint"`
	RideID     uuid.UUID       `cbor:"2,keyasint"`
	Timestamp  time.Time       `cbor:"3,keyasint,omitempty"`
	FromUserID *uuid.UUID      `cbor:"4,keyasint,omitempty"`
	ToUserID   *uuid.UUID      `cbor:"5,keyasint,omitempty"`
	Seq        uint64          `cbor:"6,keyasint,omitempty"`
	Payload    cbor.RawMessage `cbor:"7,keyasint"`
}

func (cborCodec) DecodeEnvelope(data []byte, env *Envelope) error {
	var h cborHeader
	if err := cborDecMode.Unmarshal(data, &h); err != nil {
		return err
	}
	env.Type = h.Type
	env.RideID = h.RideID
	env.Timestamp = h.Timestamp
	env.FromUserID = h.FromUserID
	env.ToUserID = h.ToUserID
	env.Seq = h.Seq
	env.Payload = nil
	if len(h.Payload) > 0 {
		env.RawPayload = append(env.RawPayload[:0], h.Payload...)
	} else {
		env.RawPayload = nil
	}
	return nil
}
