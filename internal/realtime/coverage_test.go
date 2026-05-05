package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// newCoverageServer returns a chi-mounted httptest.Server that maps the
// Authorization bearer to a stable uuid per name. Reused by the
// supersede and voice-shed tests below.
func newCoverageServer(t *testing.T, hub *Hub, ids map[string]uuid.UUID) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/ws/rides/{rideId}", func(w http.ResponseWriter, req *http.Request) {
		bearer := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		uid, ok := ids[bearer]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		req.Header.Set("Authorization", "Bearer good")
		UpgradeHandler(hub, stubAuth{uid: uid}, stubMembers{}, stubNamer{name: bearer}, zerolog.Nop()).ServeHTTP(w, req)
	})
	return httptest.NewServer(r)
}

func dialBearer(t *testing.T, srv *httptest.Server, rideID uuid.UUID, name string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws/rides/" + rideID.String()
	c, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + name}},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", name, err)
	}
	return c
}

// TestWS_ConnectionSupersede verifies that when the same user opens a
// second connection to the same ride, the server closes the first
// connection with WS code 4009 (CloseSuperseded). Documented in
// docs/WEBSOCKET_PROTOCOL.md §1.2 — was previously untested.
func TestWS_ConnectionSupersede(t *testing.T) {
	rideID := uuid.New()
	alice := uuid.New()
	hub := NewHub(zerolog.Nop())
	srv := newCoverageServer(t, hub, map[string]uuid.UUID{"alice": alice})
	defer srv.Close()

	c1 := dialBearer(t, srv, rideID, "alice")
	defer c1.Close(websocket.StatusNormalClosure, "")

	// Wait until the first connection is registered with the room.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		room := hub.getOrCreateRoom(rideID)
		room.mu.RLock()
		_, ok := room.conns[alice]
		room.mu.RUnlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c2 := dialBearer(t, srv, rideID, "alice")
	defer c2.Close(websocket.StatusNormalClosure, "")

	// c1 must observe a close with code 4009 within a reasonable window.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, _, err := c1.Read(ctx)
		if err == nil {
			continue // drain announcements / hello
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("c1 read: expected CloseError, got %v", err)
		}
		if int(ce.Code) != CloseSuperseded {
			t.Fatalf("c1 close code = %d, want %d", ce.Code, CloseSuperseded)
		}
		return
	}
}

// TestWS_VoiceShedAtCriticalTier verifies that voice signaling is
// dropped when the recipient's bandwidth tier is `critical`. Documented
// in docs/WEBSOCKET_PROTOCOL.md §10.4 — was previously asserted only by
// math, never end-to-end.
func TestWS_VoiceShedAtCriticalTier(t *testing.T) {
	rideID := uuid.New()
	alice := uuid.New()
	bob := uuid.New()
	hub := NewHub(zerolog.Nop())
	srv := newCoverageServer(t, hub, map[string]uuid.UUID{
		"alice": alice,
		"bob":   bob,
	})
	defer srv.Close()

	cAlice := dialBearer(t, srv, rideID, "alice")
	defer cAlice.Close(websocket.StatusNormalClosure, "")
	cBob := dialBearer(t, srv, rideID, "bob")
	defer cBob.Close(websocket.StatusNormalClosure, "")

	drainUntil(t, cAlice, TypeRiderOnline, 2*time.Second)

	// Force bob's server-side connection into the critical tier so any
	// voice frame addressed to him is shed by enqueue. Reaching into
	// the room here is fine — same package, white-box test.
	room := hub.getOrCreateRoom(rideID)
	deadline := time.Now().Add(2 * time.Second)
	var bobConn *Connection
	for time.Now().Before(deadline) {
		room.mu.RLock()
		bobConn = room.conns[bob]
		room.mu.RUnlock()
		if bobConn != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if bobConn == nil {
		t.Fatal("bob connection never registered")
	}
	bobConn.tier.Store(TierCritical)

	// Alice sends a voice.offer addressed to bob.
	codec := NewCodec(cAlice.Subprotocol())
	body, _ := codec.Marshal(Envelope{
		Type:       TypeVoiceOffer,
		RideID:     rideID,
		FromUserID: &alice,
		ToUserID:   &bob,
		Timestamp:  time.Now().UTC(),
		Payload:    VoicePayload{SDP: "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\n"},
	})
	if err := cAlice.Write(context.Background(), codec.MessageType(), body); err != nil {
		t.Fatalf("alice write: %v", err)
	}

	// Bob must NOT receive the voice.offer (P2 shed at critical tier).
	if got := readType(cBob, TypeVoiceOffer, 400*time.Millisecond); got {
		t.Fatal("bob received voice.offer despite critical tier shed")
	}

	// Alice should receive a NOT_FOUND error in response (sendTo
	// returned false because bob's enqueue refused the P2 message).
	gotErr := readType(cAlice, TypeError, 400*time.Millisecond)
	if !gotErr {
		t.Fatal("alice did not receive expected error frame for shed voice")
	}
}
