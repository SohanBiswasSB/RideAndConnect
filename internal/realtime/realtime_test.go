package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// stubAuth always returns the configured user. Replaces the real
// auth.Service for tests so we don't need a DB.
type stubAuth struct{ uid uuid.UUID }

func (s stubAuth) VerifyAccessToken(token string) (uuid.UUID, error) {
	if token == "good" {
		return s.uid, nil
	}
	return uuid.Nil, errStub("invalid")
}
func (s stubAuth) VerifyWSTicket(token string) (uuid.UUID, error) {
	return uuid.Nil, errStub("not used in this test")
}

// stubMembers reports everyone as an active member; pair lookups return
// "not paired".
type stubMembers struct{}

func (stubMembers) IsActiveMember(_ context.Context, _, _ uuid.UUID) (bool, error) { return true, nil }
func (stubMembers) IsPillionOf(_ context.Context, _, _ uuid.UUID) (uuid.UUID, bool, error) {
	return uuid.Nil, false, nil
}
func (stubMembers) IsRiderWithPillion(_ context.Context, _, _ uuid.UUID) (uuid.UUID, bool, error) {
	return uuid.Nil, false, nil
}

type stubNamer struct{ name string }

func (s stubNamer) NameFor(_ context.Context, _ uuid.UUID) (string, error) { return s.name, nil }

type errStub string

func (e errStub) Error() string { return string(e) }

// TestWS_LifecycleAndBroadcast wires a real chi router + Hub +
// UpgradeHandler with stubbed dependencies, then connects two clients
// and verifies that:
//
//   - rider.online is announced when each connects,
//   - alice's location.update is broadcast to bob as location.broadcast,
//   - the broadcast carries alice's user_id as from_user_id,
//   - the broadcast does NOT echo back to alice.
func TestWS_LifecycleAndBroadcast(t *testing.T) {
	rideID := uuid.New()
	alice := uuid.New()
	bob := uuid.New()

	hub := NewHub(zerolog.Nop())

	r := chi.NewRouter()
	r.Get("/api/v1/ws/rides/{rideId}", func(w http.ResponseWriter, req *http.Request) {
		// Route the upgrade through a custom handler for each user. We
		// pull the user from the Authorization header value so each
		// dial uses a different identity.
		bearer := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		var uid uuid.UUID
		switch bearer {
		case "alice":
			uid = alice
		case "bob":
			uid = bob
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Re-attach as Bearer "good" so the stub auth accepts it.
		req.Header.Set("Authorization", "Bearer good")
		UpgradeHandler(hub, stubAuth{uid: uid}, stubMembers{}, stubNamer{name: bearer}, zerolog.Nop()).ServeHTTP(w, req)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws/rides/" + rideID.String()

	dial := func(name string) *websocket.Conn {
		c, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": []string{"Bearer " + name}},
		})
		if err != nil {
			t.Fatalf("dial %s: %v", name, err)
		}
		return c
	}

	cAlice := dial("alice")
	defer cAlice.Close(websocket.StatusNormalClosure, "")
	cBob := dial("bob")
	defer cBob.Close(websocket.StatusNormalClosure, "")

	// Drain rider.online announcements both sides will see (alice's
	// connect predates bob's so bob will receive nothing from alice's
	// online event — but alice will receive bob's online).
	drainUntil(t, cAlice, TypeRiderOnline, 2*time.Second)

	// Alice publishes a location.update.
	loc, _ := json.Marshal(LocationPayload{Latitude: 12.9716, Longitude: 77.5946})
	env := Envelope{
		Type:       TypeLocationUpdate,
		RideID:     rideID,
		FromUserID: &alice,
		Timestamp:  time.Now().UTC(),
		Payload:    loc,
	}
	body, _ := json.Marshal(env)
	if err := cAlice.Write(context.Background(), websocket.MessageText, body); err != nil {
		t.Fatalf("alice write: %v", err)
	}

	// Bob should observe a location.broadcast carrying alice's id.
	got := readUntil(t, cBob, TypeLocationBroadcast, 2*time.Second)
	if got.FromUserID == nil || *got.FromUserID != alice {
		t.Errorf("broadcast from_user_id = %v, want %v", got.FromUserID, alice)
	}

	// Alice should NOT see her own broadcast back (server excludes
	// the originating connection from fan-out).
	if echoed := readType(cAlice, TypeLocationBroadcast, 200*time.Millisecond); echoed {
		t.Errorf("alice's connection received echo of her own location.broadcast")
	}
}

// readType returns true if the connection delivers a frame whose
// envelope.type matches `want` within timeout. Bool, not Envelope, so
// callers can use it as a "did NOT receive" assertion.
func readType(c *websocket.Conn, want string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var hit atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var env Envelope
			if json.Unmarshal(data, &env) == nil && env.Type == want {
				hit.Store(true)
				return
			}
		}
	}()
	<-done
	return hit.Load()
}

// readUntil blocks until a frame of the requested type arrives or the
// timeout fires. Fails the test on timeout.
func readUntil(t *testing.T, c *websocket.Conn, want string, timeout time.Duration) Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read (waiting for %s): %v", want, err)
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.Type == want {
			return env
		}
	}
}

// drainUntil reads frames until one of the requested type appears, then
// returns. Soft assertion; useful to consume noise (e.g. presence
// announcements) before the test's real check.
func drainUntil(t *testing.T, c *websocket.Conn, want string, timeout time.Duration) {
	t.Helper()
	_ = readUntil(t, c, want, timeout)
}
