package graph

import (
	"context"

	"github.com/SohanBiswasSB/rideandconnect/internal/rides"
	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
)

// Phase 6 lights up the actual route compute path. For now these
// resolvers exist so the GraphQL handler can answer schema queries
// without rejecting unknown fields, but they all return UPSTREAM_UNAVAILABLE
// so clients see a stable error contract during the build-out.

type computeRouteInput struct {
	Origin     coordinatesInput
	TravelMode string
}

type coordinatesInput struct {
	Latitude  float64
	Longitude float64
}

type peerRouteInput struct {
	TravelMode     string
	OriginOverride *coordinatesInput
}

type routeResolver struct {
	rideID          UUID
	userID          UUID
	originLat       float64
	originLng       float64
	destLat         float64
	destLng         float64
	destinationName string
	distanceMeters  int32
	durationSeconds int32
	encodedPolyline string
	provider        string
	travelMode      string
	computedAt      DateTime
	cacheHit        bool
	bbox            *bboxResolver
}

// bboxResolver maps routes.BBox onto the GraphQL BBox type.
type bboxResolver struct {
	minLat, minLng, maxLat, maxLng float64
}

func (b *bboxResolver) MinLat() float64 { return b.minLat }
func (b *bboxResolver) MinLng() float64 { return b.minLng }
func (b *bboxResolver) MaxLat() float64 { return b.maxLat }
func (b *bboxResolver) MaxLng() float64 { return b.maxLng }

func bboxFrom(b *routes.BBox) *bboxResolver {
	if b == nil {
		return nil
	}
	return &bboxResolver{minLat: b.MinLat, minLng: b.MinLng, maxLat: b.MaxLat, maxLng: b.MaxLng}
}

func (r *routeResolver) RideID() UUID                  { return r.rideID }
func (r *routeResolver) UserID() UUID                  { return r.userID }
func (r *routeResolver) Origin() *coordinatesResolver  { return &coordinatesResolver{lat: r.originLat, lng: r.originLng} }
func (r *routeResolver) Destination() *coordinatesResolver {
	return &coordinatesResolver{lat: r.destLat, lng: r.destLng}
}
func (r *routeResolver) DestinationName() string  { return r.destinationName }
func (r *routeResolver) DistanceMeters() int32    { return r.distanceMeters }
func (r *routeResolver) DurationSeconds() int32   { return r.durationSeconds }
func (r *routeResolver) EncodedPolyline() string  { return r.encodedPolyline }
func (r *routeResolver) Provider() string         { return r.provider }
func (r *routeResolver) TravelMode() string       { return r.travelMode }
func (r *routeResolver) ComputedAt() DateTime     { return r.computedAt }
func (r *routeResolver) CacheHit() bool           { return r.cacheHit }
func (r *routeResolver) BBox() *bboxResolver      { return r.bbox }

type peerRouteResolver struct {
	rideID         UUID
	fromUserID     UUID
	toUserID       UUID
	fromLat        float64
	fromLng        float64
	toLat          float64
	toLng          float64
	fromLocationAt DateTime
	toLocationAt   DateTime
	distanceMeters int32
	durationSeconds int32
	encodedPolyline string
	provider       string
	travelMode     string
	computedAt     DateTime
	cacheHit       bool
	bbox           *bboxResolver
}

func (r *peerRouteResolver) RideID() UUID                       { return r.rideID }
func (r *peerRouteResolver) FromUserID() UUID                   { return r.fromUserID }
func (r *peerRouteResolver) ToUserID() UUID                     { return r.toUserID }
func (r *peerRouteResolver) FromLocation() *coordinatesResolver { return &coordinatesResolver{lat: r.fromLat, lng: r.fromLng} }
func (r *peerRouteResolver) ToLocation() *coordinatesResolver   { return &coordinatesResolver{lat: r.toLat, lng: r.toLng} }
func (r *peerRouteResolver) FromLocationAt() DateTime           { return r.fromLocationAt }
func (r *peerRouteResolver) ToLocationAt() DateTime             { return r.toLocationAt }
func (r *peerRouteResolver) DistanceMeters() int32              { return r.distanceMeters }
func (r *peerRouteResolver) DurationSeconds() int32             { return r.durationSeconds }
func (r *peerRouteResolver) EncodedPolyline() string            { return r.encodedPolyline }
func (r *peerRouteResolver) Provider() string                   { return r.provider }
func (r *peerRouteResolver) TravelMode() string                 { return r.travelMode }
func (r *peerRouteResolver) ComputedAt() DateTime               { return r.computedAt }
func (r *peerRouteResolver) CacheHit() bool                     { return r.cacheHit }
func (r *peerRouteResolver) BBox() *bboxResolver                { return r.bbox }

// MyRoute returns the most recent cached destination route for the caller.
func (r *Resolver) MyRoute(ctx context.Context, args struct{ RideID UUID }) (*routeResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	res, err := r.rides.MyDestinationRoute(ctx, args.RideID.V, uid)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if res == nil {
		return nil, nil
	}
	return &routeResolver{
		rideID:          UUID{V: res.RideID},
		userID:          UUID{V: res.UserID},
		originLat:       res.Origin.Latitude,
		originLng:       res.Origin.Longitude,
		destLat:         res.Destination.Latitude,
		destLng:         res.Destination.Longitude,
		destinationName: res.DestinationName,
		distanceMeters:  int32(res.DistanceMeters),
		durationSeconds: int32(res.DurationSeconds),
		encodedPolyline: res.EncodedPolyline,
		provider:        res.Provider,
		travelMode:      string(res.TravelMode),
		computedAt:      DateTime{T: res.ComputedAt},
		cacheHit:        res.CacheHit,
		bbox:            bboxFrom(res.BBox),
	}, nil
}

// ComputeRoute mutation. Calls the active provider via the rides service.
func (r *Resolver) ComputeRoute(ctx context.Context, args struct {
	RideID UUID
	Input  computeRouteInput
}) (*routeResolver, error) {
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if r.provider == nil || r.provider.Active() == nil {
		return nil, &gqlError{Code: "UPSTREAM_UNAVAILABLE", Msg: "no route provider configured"}
	}

	mode := args.Input.TravelMode
	if mode == "" {
		mode = "TWO_WHEELER"
	}
	res, err := r.rides.ComputeRoute(ctx, r.provider.Active(), args.RideID.V, uid, rides.ComputeRouteInput{
		Origin:     routes.Point{Latitude: args.Input.Origin.Latitude, Longitude: args.Input.Origin.Longitude},
		TravelMode: routes.TravelMode(mode),
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return &routeResolver{
		rideID:          UUID{V: res.RideID},
		userID:          UUID{V: res.UserID},
		originLat:       res.Origin.Latitude,
		originLng:       res.Origin.Longitude,
		destLat:         res.Destination.Latitude,
		destLng:         res.Destination.Longitude,
		destinationName: res.DestinationName,
		distanceMeters:  int32(res.DistanceMeters),
		durationSeconds: int32(res.DurationSeconds),
		encodedPolyline: res.EncodedPolyline,
		provider:        res.Provider,
		travelMode:      string(res.TravelMode),
		computedAt:      DateTime{T: res.ComputedAt},
		cacheHit:        res.CacheHit,
		bbox:            bboxFrom(res.BBox),
	}, nil
}

// PeerRoute query. Phase 6 wired: presence lookup, pair short-circuit,
// throttle, breaker, cache.
func (r *Resolver) PeerRoute(ctx context.Context, args struct {
	RideID     UUID
	PeerUserID UUID
	Input      *peerRouteInput
}) (*peerRouteResolver, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	uid, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if r.presence == nil {
		return nil, &gqlError{Code: "UPSTREAM_UNAVAILABLE", Msg: "presence not wired"}
	}

	mode := "TWO_WHEELER"
	var origin *routes.Point
	if args.Input != nil {
		if args.Input.TravelMode != "" {
			mode = args.Input.TravelMode
		}
		if args.Input.OriginOverride != nil {
			origin = &routes.Point{Latitude: args.Input.OriginOverride.Latitude, Longitude: args.Input.OriginOverride.Longitude}
		}
	}
	in := rides.PeerRouteInput{TravelMode: routes.TravelMode(mode), OriginOverride: origin}
	var prov routes.Provider
	if r.provider != nil {
		prov = r.provider.Active()
	}
	res, err := r.rides.PeerRoute(ctx, prov, r.presence, args.RideID.V, uid, args.PeerUserID.V, in)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return &peerRouteResolver{
		rideID:          UUID{V: res.RideID},
		fromUserID:      UUID{V: res.FromUserID},
		toUserID:        UUID{V: res.ToUserID},
		fromLat:         res.From.Latitude,
		fromLng:         res.From.Longitude,
		toLat:           res.To.Latitude,
		toLng:           res.To.Longitude,
		fromLocationAt:  DateTime{T: res.FromAt},
		toLocationAt:    DateTime{T: res.ToAt},
		distanceMeters:  int32(res.DistanceMeters),
		durationSeconds: int32(res.DurationSeconds),
		encodedPolyline: res.EncodedPolyline,
		provider:        res.Provider,
		travelMode:      string(res.TravelMode),
		computedAt:      DateTime{T: res.ComputedAt},
		cacheHit:        res.CacheHit,
		bbox:            bboxFrom(res.BBox),
	}, nil
}
