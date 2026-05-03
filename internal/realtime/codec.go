package realtime

import (
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
	"github.com/fxamacker/cbor/v2"
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
