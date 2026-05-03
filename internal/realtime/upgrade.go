package realtime

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// AuthVerifier is the surface upgrade needs to authenticate the caller.
type AuthVerifier interface {
	VerifyAccessToken(token string) (uuid.UUID, error)
	VerifyWSTicket(token string) (uuid.UUID, error)
}

// MembershipChecker verifies the caller is an active member of the ride
// AND tells us their pair role.
type MembershipChecker interface {
	IsActiveMember(ctx context.Context, rideID, userID uuid.UUID) (bool, error)
	IsPillionOf(ctx context.Context, rideID, userID uuid.UUID) (uuid.UUID, bool, error)
	IsRiderWithPillion(ctx context.Context, rideID, userID uuid.UUID) (uuid.UUID, bool, error)
}

// UserNamer fetches a user's display name for presence broadcasts.
type UserNamer interface {
	NameFor(ctx context.Context, userID uuid.UUID) (string, error)
}

// UpgradeHandler returns an http.Handler for /api/v1/ws/rides/{rideId}.
//
// Negotiates:
//   - Subprotocol (CBOR preferred, JSON fallback) via Sec-WebSocket-Protocol
//   - permessage-deflate with context takeover for ~50% wire shrink
//
// Auth: Bearer header preferred, ?access_token= ws-ticket fallback.
func UpgradeHandler(hub *Hub, auth AuthVerifier, members MembershipChecker, namer UserNamer, log zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rideStr := chi.URLParam(r, "rideId")
		rideID, err := uuid.Parse(rideStr)
		if err != nil {
			http.Error(w, "bad ride id", http.StatusBadRequest)
			return
		}

		userID, err := authenticate(r, auth)
		if err != nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ok, err := members.IsActiveMember(r.Context(), rideID, userID)
		if err != nil {
			log.Warn().Err(err).Msg("ws upgrade: membership lookup")
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		var pairedWith, pairedAs uuid.UUID
		if rider, paired, err := members.IsPillionOf(r.Context(), rideID, userID); err == nil && paired {
			pairedAs = rider
		} else if pillion, paired, err := members.IsRiderWithPillion(r.Context(), rideID, userID); err == nil && paired {
			pairedWith = pillion
		}

		name, _ := namer.NameFor(r.Context(), userID)

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: SupportedSubprotocols,
			// Context takeover keeps the deflate dictionary across
			// frames within a single connection — biggest wire-shrink
			// win for our repetitive location.broadcast hot path.
			CompressionMode:      websocket.CompressionContextTakeover,
			CompressionThreshold: 64,
			OriginPatterns:       []string{"*"},
		})
		if err != nil {
			log.Warn().Err(err).Msg("ws upgrade: accept failed")
			return
		}

		codec := NewCodec(conn.Subprotocol())
		log.Info().
			Str("user_id", userID.String()).
			Str("ride_id", rideID.String()).
			Str("subprotocol", conn.Subprotocol()).
			Str("codec", codec.Subprotocol()).
			Msg("ws: upgraded")

		room := hub.getOrCreateRoom(rideID)
		c := newConnection(r.Context(), conn, codec, room, userID, name, pairedWith, pairedAs, log, hub.MetricsSink())
		c.Run()
	}
}

// authenticate prefers the Authorization header then the access_token
// query param (ws-ticket).
func authenticate(r *http.Request, v AuthVerifier) (uuid.UUID, error) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if id, err := v.VerifyAccessToken(tok); err == nil {
			return id, nil
		}
	}
	if tok := r.URL.Query().Get("access_token"); tok != "" {
		return v.VerifyWSTicket(tok)
	}
	return uuid.Nil, errors.New("no credentials")
}
