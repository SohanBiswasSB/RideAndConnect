// Package routes defines the route-provider abstraction used by both the
// destination-route flow (computeRoute) and the peer-route flow (peerRoute).
//
// Two adapters live in subpackages and implement Provider:
//
//   - internal/routes/google — Google Routes API v2 (computeRoutes)
//   - internal/routes/mapbox — Mapbox Directions API v5
//
// Switching between them is a single env var change (ROUTE_PROVIDER).
// See docs/ROUTE_PROVIDER.md for the strategy and docs/API.md §3 for the
// resolver contract.
package routes

import (
	"context"
	"math"
	"time"
)

// TravelMode is the travel profile requested by the caller. Each adapter
// translates this into provider-specific values; see ROUTE_PROVIDER §3.2 and
// §4.2 for the mapping. Adapters MUST echo back the actually-used mode in
// ComputeResponse.TravelMode (it may differ when a fallback applies).
type TravelMode string

const (
	TravelModeTwoWheeler TravelMode = "TWO_WHEELER"
	TravelModeBicycle    TravelMode = "BICYCLE"
	TravelModeWalk       TravelMode = "WALK"
)

// Validate reports whether the mode is one of the supported values.
func (m TravelMode) Validate() error {
	switch m {
	case TravelModeTwoWheeler, TravelModeBicycle, TravelModeWalk:
		return nil
	default:
		return ErrInvalidTravelMode
	}
}

// Point is a WGS84 lat/lng pair in decimal degrees.
type Point struct {
	Latitude  float64
	Longitude float64
}

// Validate enforces the documented coordinate ranges (API.md §1).
// NaN and ±Inf are rejected.
func (p Point) Validate() error {
	if isNaNOrInf(p.Latitude) || isNaNOrInf(p.Longitude) {
		return ErrInvalidCoordinate
	}
	if p.Latitude < -90 || p.Latitude > 90 {
		return ErrInvalidCoordinate
	}
	if p.Longitude < -180 || p.Longitude > 180 {
		return ErrInvalidCoordinate
	}
	return nil
}

// ComputeRequest is the provider-agnostic input for a single route compute.
// Adapters do not see ride/user identifiers — those are the caller's
// concern. Adapters see only the points, the mode, and a small set of
// modifiers.
type ComputeRequest struct {
	Origin      Point
	Destination Point
	TravelMode  TravelMode
	AvoidTolls  bool
}

// Validate composes Point/TravelMode validation. Callers should run this
// before invoking the provider so we never spend a paid call on bad input.
func (r ComputeRequest) Validate() error {
	if err := r.TravelMode.Validate(); err != nil {
		return err
	}
	if err := r.Origin.Validate(); err != nil {
		return err
	}
	if err := r.Destination.Validate(); err != nil {
		return err
	}
	if r.Origin == r.Destination {
		return ErrZeroLengthRoute
	}
	return nil
}

// ComputeResponse is the provider-agnostic output. Distances/durations are
// always in meters/seconds; polylines are encoded with the standard Google
// polyline algorithm — both adapters produce that format.
type ComputeResponse struct {
	DistanceMeters  int
	DurationSeconds int
	EncodedPolyline string
	Provider        string
	TravelMode      TravelMode
	ComputedAt      time.Time
}

// BBox is a WGS84 bounding box in min/max form. Returned with route
// results so mobile clients can pre-fetch map tiles for an offline
// corridor (~500 m around the route polyline).
type BBox struct {
	MinLat, MinLng float64
	MaxLat, MaxLng float64
}

// PolylineBBox decodes a Google-format encoded polyline and returns the
// minimum bounding box, expanded by `bufferDeg` on each side. A buffer
// of 0.005° corresponds to roughly 500 m. Returns ok=false on an
// unparseable or empty input — caller should fall back to an OD-derived
// bbox in that case.
func PolylineBBox(encoded string, bufferDeg float64) (BBox, bool) {
	pts := decodePolyline(encoded)
	if len(pts) == 0 {
		return BBox{}, false
	}
	minLat, maxLat := pts[0].Latitude, pts[0].Latitude
	minLng, maxLng := pts[0].Longitude, pts[0].Longitude
	for _, p := range pts[1:] {
		if p.Latitude < minLat {
			minLat = p.Latitude
		}
		if p.Latitude > maxLat {
			maxLat = p.Latitude
		}
		if p.Longitude < minLng {
			minLng = p.Longitude
		}
		if p.Longitude > maxLng {
			maxLng = p.Longitude
		}
	}
	return BBox{
		MinLat: minLat - bufferDeg,
		MinLng: minLng - bufferDeg,
		MaxLat: maxLat + bufferDeg,
		MaxLng: maxLng + bufferDeg,
	}, true
}

// decodePolyline implements the Google polyline-encoding algorithm
// (https://developers.google.com/maps/documentation/utilities/polylinealgorithm).
// Inlined here so internal/routes has no third-party decode dependency.
func decodePolyline(s string) []Point {
	var pts []Point
	var lat, lng int
	i := 0
	for i < len(s) {
		dlat, n := decodePolylineInt(s[i:])
		if n == 0 {
			return nil
		}
		i += n
		dlng, n2 := decodePolylineInt(s[i:])
		if n2 == 0 {
			return nil
		}
		i += n2
		lat += dlat
		lng += dlng
		pts = append(pts, Point{Latitude: float64(lat) / 1e5, Longitude: float64(lng) / 1e5})
	}
	return pts
}

func decodePolylineInt(s string) (int, int) {
	var result, shift int
	for i := 0; i < len(s); i++ {
		b := int(s[i]) - 63
		if b < 0 {
			return 0, 0
		}
		result |= (b & 0x1f) << shift
		shift += 5
		if b < 0x20 {
			if result&1 != 0 {
				return ^(result >> 1), i + 1
			}
			return result >> 1, i + 1
		}
	}
	return 0, 0
}

// Capabilities is a static, per-provider description of which travel modes
// the underlying API natively supports. The factory uses this for sanity
// checks at startup. It does not vary by region — region-specific fallback
// is handled inside the adapter.
type Capabilities struct {
	SupportsTwoWheeler bool
	SupportsCycling    bool
	SupportsWalking    bool
}

// Provider is the single seam between the routes service and any external
// directions API. Implementations MUST be safe for concurrent use.
type Provider interface {
	// Name returns a stable identifier persisted in ride_routes.provider and
	// returned to clients in API responses.
	Name() string

	// Compute performs a directions lookup. Implementations MUST:
	//   - honor ctx (timeout/cancel),
	//   - never log auth tokens or full provider response bodies,
	//   - return one of the sentinel errors for retryable/upstream failures
	//     (see errors.go), wrapping the underlying cause via fmt.Errorf %w.
	Compute(ctx context.Context, req ComputeRequest) (ComputeResponse, error)

	// Capabilities reports static feature support. Stable for the lifetime
	// of the process.
	Capabilities() Capabilities
}

func isNaNOrInf(f float64) bool {
	return math.IsNaN(f) || math.IsInf(f, 0)
}
