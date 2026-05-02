package rides

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
)

// RouteResult is the destination-route response surfaced to resolvers.
type RouteResult struct {
	RideID          uuid.UUID
	UserID          uuid.UUID
	Origin          routes.Point
	Destination     routes.Point
	DestinationName string
	DistanceMeters  int
	DurationSeconds int
	EncodedPolyline string
	Provider        string
	TravelMode      routes.TravelMode
	ComputedAt      time.Time
	CacheHit        bool
}

// PeerRouteResult is the peer-to-peer route response surfaced to
// resolvers. Includes the location timestamps so callers can render
// freshness indicators.
type PeerRouteResult struct {
	RideID          uuid.UUID
	FromUserID      uuid.UUID
	ToUserID        uuid.UUID
	From            routes.Point
	To              routes.Point
	FromAt          time.Time
	ToAt            time.Time
	DistanceMeters  int
	DurationSeconds int
	EncodedPolyline string
	Provider        string
	TravelMode      routes.TravelMode
	ComputedAt      time.Time
	CacheHit        bool
}

// ComputeRouteInput is what the GraphQL resolver passes down for
// destination routes.
type ComputeRouteInput struct {
	Origin     routes.Point
	TravelMode routes.TravelMode
}

// PeerRouteInput is what the GraphQL resolver passes down for peer
// routes.
type PeerRouteInput struct {
	TravelMode     routes.TravelMode
	OriginOverride *routes.Point // optional fresher caller position
}

// PresenceLookup is the seam over the realtime Hub. The routes service
// reads in-memory last-known locations through it for peer-route.
type PresenceLookup interface {
	LookupPresence(rideID, userID uuid.UUID) (LastLocation, bool)
}

// LastLocation mirrors realtime.LastLocation but lives in the rides
// package so routes doesn't import realtime (avoids a dependency cycle).
// realtime.LastLocation values convert losslessly via the LookupPresence
// adapter.
type LastLocation struct {
	Latitude  float64
	Longitude float64
	At        time.Time
}

// RouteEngine adds the route compute methods. It hangs off the same
// Service value so callers don't have to thread two services.
//
// A small circuit breaker is wrapped around provider calls; exceeded
// failure thresholds trip the breaker into Open for cooldown seconds.
type RouteEngine struct {
	mu             sync.Mutex
	failures       atomic.Int32
	openedAt       time.Time
	cooldown       time.Duration
	failThreshold  int32
	throttleUser   *throttleStore // per (user, ride) for computeRoute
	throttleRide   *throttleStore // per ride for computeRoute
	throttlePair   *throttleStore // per (caller, peer) for peerRoute
	throttlePeerR  *throttleStore // per ride for peerRoute
	staleTTL       time.Duration
	cacheTTL       time.Duration
	peerCacheTTL   time.Duration
}

// EngineConfig is what main.go passes to wire defaults from
// internal/config.
type EngineConfig struct {
	StaleTTL     time.Duration
	CacheTTL     time.Duration
	PeerCacheTTL time.Duration
}

// AttachEngine adds the route engine to the service. Idempotent.
func (s *Service) AttachEngine(cfg EngineConfig) {
	if s.engine != nil {
		return
	}
	if cfg.StaleTTL == 0 {
		cfg.StaleTTL = 30 * time.Second
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 30 * time.Minute
	}
	if cfg.PeerCacheTTL == 0 {
		cfg.PeerCacheTTL = 60 * time.Second
	}
	s.engine = &RouteEngine{
		cooldown:       30 * time.Second,
		failThreshold:  5,
		throttleUser:   newThrottle(2, 1.0/30.0),  // 1 / 30s with burst 2
		throttleRide:   newThrottle(6, 1.0/100.0), // 6 / 10min
		throttlePair:   newThrottle(2, 1.0/30.0),
		throttlePeerR:  newThrottle(12, 12.0/600.0),
		staleTTL:       cfg.StaleTTL,
		cacheTTL:       cfg.CacheTTL,
		peerCacheTTL:   cfg.PeerCacheTTL,
	}
}

// breakerAllow returns false when the breaker is Open.
func (e *RouteEngine) breakerAllow(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failures.Load() < e.failThreshold {
		return true
	}
	// Open: stay open for cooldown duration, then half-open (one probe).
	if now.Sub(e.openedAt) < e.cooldown {
		return false
	}
	// Half-open probe — let one request through; reset failures so a
	// success closes the breaker.
	e.failures.Store(e.failThreshold - 1)
	return true
}

func (e *RouteEngine) breakerNote(success bool) {
	if success {
		e.failures.Store(0)
		return
	}
	if e.failures.Add(1) >= e.failThreshold {
		e.mu.Lock()
		e.openedAt = time.Now().UTC()
		e.mu.Unlock()
	}
}

// ComputeRoute is the destination-route flow with cache lookup, throttle,
// breaker, and provider call. Returns ErrThrottled-wrapped errors via
// fmt.Errorf %w when the caller has hit a rate limit.
func (s *Service) ComputeRoute(ctx context.Context, provider routes.Provider, rideID, callerID uuid.UUID, in ComputeRouteInput) (*RouteResult, error) {
	if s.engine == nil {
		return nil, fmt.Errorf("rides: route engine not initialised")
	}
	if provider == nil {
		return nil, fmt.Errorf("rides: route provider not configured")
	}
	if err := in.Origin.Validate(); err != nil {
		return nil, fmt.Errorf("%w: origin: %v", ErrValidation, err)
	}
	if err := in.TravelMode.Validate(); err != nil {
		return nil, fmt.Errorf("%w: travel_mode", ErrValidation)
	}
	if err := ensureMemberPool(ctx, s.pool, rideID, callerID); err != nil {
		return nil, err
	}

	// Throttle.
	if ok, wait := s.engine.throttleUser.allow(throttleKey(callerID, rideID)); !ok {
		return nil, throttleErr(wait)
	}
	if ok, wait := s.engine.throttleRide.allow(throttleKey(rideID)); !ok {
		return nil, throttleErr(wait)
	}

	// Snapshot the destination.
	var (
		destName *string
		destLat  *float64
		destLng  *float64
		status   string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT status::text, destination_name, destination_latitude, destination_longitude
		  FROM rides WHERE id = $1
	`, rideID).Scan(&status, &destName, &destLat, &destLng)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rides: read ride: %w", err)
	}
	if status != string(StatusActive) {
		return nil, ErrEnded
	}
	if destName == nil || destLat == nil || destLng == nil {
		return nil, fmt.Errorf("%w: destination not set", ErrValidation)
	}

	originGH := Geohash7(in.Origin.Latitude, in.Origin.Longitude)
	destGH := Geohash7(*destLat, *destLng)

	// Cache lookup.
	if cached, ok, err := s.lookupRouteCache(ctx, "DESTINATION", rideID, callerID, nil, originGH, destGH, string(in.TravelMode)); err == nil && ok {
		return &RouteResult{
			RideID:          rideID,
			UserID:          callerID,
			Origin:          in.Origin,
			Destination:     routes.Point{Latitude: *destLat, Longitude: *destLng},
			DestinationName: *destName,
			DistanceMeters:  cached.DistanceMeters,
			DurationSeconds: cached.DurationSeconds,
			EncodedPolyline: cached.EncodedPolyline,
			Provider:        cached.Provider,
			TravelMode:      routes.TravelMode(cached.TravelMode),
			ComputedAt:      cached.ComputedAt,
			CacheHit:        true,
		}, nil
	}

	if !s.engine.breakerAllow(time.Now()) {
		return nil, fmt.Errorf("%w: provider circuit open", ErrUpstream)
	}

	resp, err := provider.Compute(ctx, routes.ComputeRequest{
		Origin:      in.Origin,
		Destination: routes.Point{Latitude: *destLat, Longitude: *destLng},
		TravelMode:  in.TravelMode,
	})
	if err != nil {
		s.engine.breakerNote(false)
		return nil, fmt.Errorf("rides: provider compute: %w", err)
	}
	s.engine.breakerNote(true)

	// Cache insert (best-effort).
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO ride_routes (
			ride_id, user_id, route_kind,
			origin_latitude, origin_longitude,
			destination_latitude, destination_longitude,
			origin_geohash7, destination_geohash7,
			distance_meters, duration_seconds, encoded_polyline,
			provider, travel_mode, expires_at
		) VALUES ($1, $2, 'DESTINATION', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		rideID, callerID,
		in.Origin.Latitude, in.Origin.Longitude, *destLat, *destLng,
		originGH, destGH,
		resp.DistanceMeters, resp.DurationSeconds, resp.EncodedPolyline,
		resp.Provider, string(resp.TravelMode), time.Now().UTC().Add(s.engine.cacheTTL),
	)

	return &RouteResult{
		RideID:          rideID,
		UserID:          callerID,
		Origin:          in.Origin,
		Destination:     routes.Point{Latitude: *destLat, Longitude: *destLng},
		DestinationName: *destName,
		DistanceMeters:  resp.DistanceMeters,
		DurationSeconds: resp.DurationSeconds,
		EncodedPolyline: resp.EncodedPolyline,
		Provider:        resp.Provider,
		TravelMode:      resp.TravelMode,
		ComputedAt:      resp.ComputedAt,
		CacheHit:        false,
	}, nil
}

// PeerRoute is the rider→rider distance flow. Honors the rider/pillion
// short-circuit (returns provider=self_pair, distance 0) and the
// staleness guard.
func (s *Service) PeerRoute(ctx context.Context, provider routes.Provider, presence PresenceLookup, rideID, callerID, peerID uuid.UUID, in PeerRouteInput) (*PeerRouteResult, error) {
	if callerID == peerID {
		return nil, fmt.Errorf("%w: peer must differ from caller", ErrValidation)
	}
	if s.engine == nil {
		return nil, fmt.Errorf("rides: route engine not initialised")
	}
	if presence == nil {
		return nil, fmt.Errorf("rides: presence not wired")
	}
	if err := in.TravelMode.Validate(); err != nil {
		return nil, fmt.Errorf("%w: travel_mode", ErrValidation)
	}
	if in.OriginOverride != nil {
		if err := in.OriginOverride.Validate(); err != nil {
			return nil, fmt.Errorf("%w: origin_override: %v", ErrValidation, err)
		}
	}

	// Both must be active members of the ride.
	if err := ensureMemberPool(ctx, s.pool, rideID, callerID); err != nil {
		return nil, err
	}
	memberOK, err := s.IsActiveMember(ctx, rideID, peerID)
	if err != nil {
		return nil, err
	}
	if !memberOK {
		return nil, ErrNotMember
	}

	// Pair short-circuit: if caller and peer are paired together, we
	// don't compute or cache anything; just return zeroed result with
	// the sentinel provider.
	paired, err := s.ArePaired(ctx, rideID, callerID, peerID)
	if err != nil {
		return nil, err
	}
	if paired {
		now := time.Now().UTC()
		return &PeerRouteResult{
			RideID:     rideID,
			FromUserID: callerID,
			ToUserID:   peerID,
			Provider:   "self_pair",
			TravelMode: in.TravelMode,
			ComputedAt: now,
			CacheHit:   false,
			FromAt:     now,
			ToAt:       now,
		}, nil
	}

	// Throttle.
	if ok, wait := s.engine.throttlePair.allow(throttleKey(rideID, callerID, peerID)); !ok {
		return nil, throttleErr(wait)
	}
	if ok, wait := s.engine.throttlePeerR.allow(throttleKey(rideID)); !ok {
		return nil, throttleErr(wait)
	}

	// Resolve effective locations.
	now := time.Now().UTC()
	var fromLoc, toLoc LastLocation
	if in.OriginOverride != nil {
		fromLoc = LastLocation{Latitude: in.OriginOverride.Latitude, Longitude: in.OriginOverride.Longitude, At: now}
	} else {
		l, ok := presence.LookupPresence(rideID, callerID)
		if !ok {
			return nil, fmt.Errorf("%w: caller", ErrPeerLocationMissing)
		}
		fromLoc = l
	}

	// If the peer is a paired pillion, route to their rider's location.
	effectivePeer := peerID
	if rider, isPil, err := s.IsPillionOf(ctx, rideID, peerID); err == nil && isPil {
		effectivePeer = rider
	}
	pl, ok := presence.LookupPresence(rideID, effectivePeer)
	if !ok {
		return nil, fmt.Errorf("%w: peer", ErrPeerLocationMissing)
	}
	toLoc = pl

	// Staleness guard.
	if now.Sub(fromLoc.At) > s.engine.staleTTL {
		return nil, fmt.Errorf("%w: caller", ErrPeerLocationStale)
	}
	if now.Sub(toLoc.At) > s.engine.staleTTL {
		return nil, fmt.Errorf("%w: peer", ErrPeerLocationStale)
	}

	originGH := Geohash7(fromLoc.Latitude, fromLoc.Longitude)
	destGH := Geohash7(toLoc.Latitude, toLoc.Longitude)

	if cached, ok, err := s.lookupRouteCache(ctx, "PEER", rideID, callerID, &peerID, originGH, destGH, string(in.TravelMode)); err == nil && ok {
		return &PeerRouteResult{
			RideID:          rideID,
			FromUserID:      callerID,
			ToUserID:        peerID,
			From:            routes.Point{Latitude: fromLoc.Latitude, Longitude: fromLoc.Longitude},
			To:              routes.Point{Latitude: toLoc.Latitude, Longitude: toLoc.Longitude},
			FromAt:          fromLoc.At,
			ToAt:            toLoc.At,
			DistanceMeters:  cached.DistanceMeters,
			DurationSeconds: cached.DurationSeconds,
			EncodedPolyline: cached.EncodedPolyline,
			Provider:        cached.Provider,
			TravelMode:      routes.TravelMode(cached.TravelMode),
			ComputedAt:      cached.ComputedAt,
			CacheHit:        true,
		}, nil
	}

	if !s.engine.breakerAllow(now) {
		return nil, fmt.Errorf("%w: provider circuit open", ErrUpstream)
	}
	if provider == nil {
		return nil, fmt.Errorf("%w: provider not configured", ErrUpstream)
	}
	resp, err := provider.Compute(ctx, routes.ComputeRequest{
		Origin:      routes.Point{Latitude: fromLoc.Latitude, Longitude: fromLoc.Longitude},
		Destination: routes.Point{Latitude: toLoc.Latitude, Longitude: toLoc.Longitude},
		TravelMode:  in.TravelMode,
	})
	if err != nil {
		s.engine.breakerNote(false)
		return nil, fmt.Errorf("rides: peer provider compute: %w", err)
	}
	s.engine.breakerNote(true)

	_, _ = s.pool.Exec(ctx, `
		INSERT INTO ride_routes (
			ride_id, user_id, route_kind, peer_user_id,
			origin_latitude, origin_longitude,
			destination_latitude, destination_longitude,
			origin_geohash7, destination_geohash7,
			distance_meters, duration_seconds, encoded_polyline,
			provider, travel_mode, expires_at
		) VALUES ($1, $2, 'PEER', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`,
		rideID, callerID, peerID,
		fromLoc.Latitude, fromLoc.Longitude, toLoc.Latitude, toLoc.Longitude,
		originGH, destGH,
		resp.DistanceMeters, resp.DurationSeconds, resp.EncodedPolyline,
		resp.Provider, string(resp.TravelMode), now.Add(s.engine.peerCacheTTL),
	)

	return &PeerRouteResult{
		RideID:          rideID,
		FromUserID:      callerID,
		ToUserID:        peerID,
		From:            routes.Point{Latitude: fromLoc.Latitude, Longitude: fromLoc.Longitude},
		To:              routes.Point{Latitude: toLoc.Latitude, Longitude: toLoc.Longitude},
		FromAt:          fromLoc.At,
		ToAt:            toLoc.At,
		DistanceMeters:  resp.DistanceMeters,
		DurationSeconds: resp.DurationSeconds,
		EncodedPolyline: resp.EncodedPolyline,
		Provider:        resp.Provider,
		TravelMode:      resp.TravelMode,
		ComputedAt:      resp.ComputedAt,
		CacheHit:        false,
	}, nil
}

// MyDestinationRoute returns the freshest cached destination route for
// the caller; nil + nil when none exists.
func (s *Service) MyDestinationRoute(ctx context.Context, rideID, callerID uuid.UUID) (*RouteResult, error) {
	if err := ensureMemberPool(ctx, s.pool, rideID, callerID); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		SELECT origin_latitude, origin_longitude, destination_latitude, destination_longitude,
		       distance_meters, duration_seconds, encoded_polyline, provider, travel_mode, created_at,
		       (SELECT destination_name FROM rides WHERE id = $1)
		  FROM ride_routes
		 WHERE ride_id = $1 AND user_id = $2 AND route_kind = 'DESTINATION'
		   AND expires_at > now()
		 ORDER BY created_at DESC
		 LIMIT 1
	`, rideID, callerID)
	var (
		oLat, oLng, dLat, dLng float64
		dist, dur              int
		poly, prov, mode       string
		at                     time.Time
		destName               *string
	)
	if err := row.Scan(&oLat, &oLng, &dLat, &dLng, &dist, &dur, &poly, &prov, &mode, &at, &destName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("rides: my route: %w", err)
	}
	out := &RouteResult{
		RideID:          rideID,
		UserID:          callerID,
		Origin:          routes.Point{Latitude: oLat, Longitude: oLng},
		Destination:     routes.Point{Latitude: dLat, Longitude: dLng},
		DistanceMeters:  dist,
		DurationSeconds: dur,
		EncodedPolyline: poly,
		Provider:        prov,
		TravelMode:      routes.TravelMode(mode),
		ComputedAt:      at,
		CacheHit:        true,
	}
	if destName != nil {
		out.DestinationName = *destName
	}
	return out, nil
}

// cachedRoute is the small struct returned by lookupRouteCache.
type cachedRoute struct {
	DistanceMeters  int
	DurationSeconds int
	EncodedPolyline string
	Provider        string
	TravelMode      string
	ComputedAt      time.Time
}

func (s *Service) lookupRouteCache(ctx context.Context, kind string, rideID, userID uuid.UUID, peerID *uuid.UUID, originGH, destGH, mode string) (*cachedRoute, bool, error) {
	var row pgx.Row
	if peerID == nil {
		row = s.pool.QueryRow(ctx, `
			SELECT distance_meters, duration_seconds, encoded_polyline, provider, travel_mode, created_at
			  FROM ride_routes
			 WHERE ride_id = $1 AND user_id = $2 AND route_kind = $3
			   AND origin_geohash7 = $4 AND destination_geohash7 = $5 AND travel_mode = $6
			   AND expires_at > now()
			 ORDER BY created_at DESC
			 LIMIT 1
		`, rideID, userID, kind, originGH, destGH, mode)
	} else {
		row = s.pool.QueryRow(ctx, `
			SELECT distance_meters, duration_seconds, encoded_polyline, provider, travel_mode, created_at
			  FROM ride_routes
			 WHERE ride_id = $1 AND user_id = $2 AND peer_user_id = $7 AND route_kind = $3
			   AND origin_geohash7 = $4 AND destination_geohash7 = $5 AND travel_mode = $6
			   AND expires_at > now()
			 ORDER BY created_at DESC
			 LIMIT 1
		`, rideID, userID, kind, originGH, destGH, mode, *peerID)
	}
	var c cachedRoute
	if err := row.Scan(&c.DistanceMeters, &c.DurationSeconds, &c.EncodedPolyline, &c.Provider, &c.TravelMode, &c.ComputedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("rides: cache lookup: %w", err)
	}
	return &c, true, nil
}

func ensureMemberPool(ctx context.Context, pool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, rideID, userID uuid.UUID) error {
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM ride_members
		 WHERE ride_id = $1 AND user_id = $2 AND left_at IS NULL
	`, rideID, userID).Scan(&n); err != nil {
		return fmt.Errorf("rides: member check: %w", err)
	}
	if n == 0 {
		return ErrNotMember
	}
	return nil
}

// throttleErr wraps the wait duration into an error compatible with
// errors.Is(err, ErrThrottled).
func throttleErr(wait time.Duration) error {
	return fmt.Errorf("%w: retry in %s", ErrThrottled, wait.Round(time.Millisecond))
}
