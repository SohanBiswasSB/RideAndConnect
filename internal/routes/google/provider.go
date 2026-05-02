// Package google is the routes.Provider adapter for the Google Routes API
// v2 (computeRoutes). See docs/ROUTE_PROVIDER.md §3 for the strategy.
//
// The adapter is intentionally narrow — it does not cache, throttle, or
// open circuit breakers; those concerns live one layer up in
// internal/routes (or its callers). This file is one of two parallel
// adapters; the other is internal/routes/mapbox. Switching providers is a
// single env change (ROUTE_PROVIDER=google or mapbox).
package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
)

// providerName is the stable identifier persisted to ride_routes.provider
// and exposed in API responses.
const providerName = "google_routes"

// defaultBaseURL is the production endpoint base. Tests override this via
// Config.BaseURL to point at httptest.Server.
const defaultBaseURL = "https://routes.googleapis.com"

// computePath is the full request path mounted on BaseURL.
const computePath = "/directions/v2:computeRoutes"

// fieldMask keeps the response surface minimal — billing depends on the
// fields requested. Anything added here costs more per call.
const fieldMask = "routes.distanceMeters,routes.duration,routes.polyline.encodedPolyline"

// defaultTimeout caps any single HTTP call. The caller's context may
// shorten this further.
const defaultTimeout = 10 * time.Second

// twoWheelerAllowedRegions is a coarse static allowlist. Google's
// TWO_WHEELER mode is only supported in select countries; outside this
// list the adapter rewrites the request to BICYCLE.
//
// The list is intentionally small and explicit — verify with provider docs
// before adding regions for production launches. This is purely a fallback
// safety net; correctness does not depend on its accuracy.
var twoWheelerAllowedRegions = map[string]bool{
	// Detection by destination latitude/longitude in a future revision; for
	// v1 we treat TWO_WHEELER as accepted by default and rely on Google's
	// 400 response to signal unsupported regions, then retry as BICYCLE.
}

// Config configures the adapter. APIKey is the only required field; the
// rest have sensible defaults. The HTTPClient is overridable so tests can
// inject a recording/replay client.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Provider implements routes.Provider against Google Routes API v2.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}

// New constructs a Provider. Returns an error if the API key is missing —
// configuration errors are caught at startup, not at first request.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("%w: GOOGLE_ROUTES_API_KEY is empty", routes.ErrProviderUnconfigured)
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	to := cfg.Timeout
	if to <= 0 {
		to = defaultTimeout
	}
	return &Provider{
		apiKey:     cfg.APIKey,
		baseURL:    base,
		httpClient: hc,
		timeout:    to,
	}, nil
}

// Name returns the stable identifier.
func (p *Provider) Name() string { return providerName }

// Capabilities reports static support flags.
func (p *Provider) Capabilities() routes.Capabilities {
	return routes.Capabilities{
		SupportsTwoWheeler: true,
		SupportsCycling:    true, // mapped to BICYCLE
		SupportsWalking:    true,
	}
}

// Compute calls computeRoutes and maps the response into a
// routes.ComputeResponse. The TWO_WHEELER → BICYCLE fallback is performed
// here transparently when Google rejects the mode for the destination's
// region; ComputeResponse.TravelMode reflects the mode actually used.
func (p *Provider) Compute(ctx context.Context, req routes.ComputeRequest) (routes.ComputeResponse, error) {
	if err := req.Validate(); err != nil {
		return routes.ComputeResponse{}, err
	}

	// First attempt — use the requested mode.
	resp, err := p.computeOnce(ctx, req, req.TravelMode)
	if err == nil {
		return resp, nil
	}

	// Region-restricted TWO_WHEELER fallback. Google returns 400 with a
	// specific message; we retry as BICYCLE and surface the actual mode.
	if req.TravelMode == routes.TravelModeTwoWheeler && errors.Is(err, routes.ErrUpstreamValidation) {
		fallback, fbErr := p.computeOnce(ctx, req, routes.TravelModeBicycle)
		if fbErr == nil {
			return fallback, nil
		}
		// Both failed — return the bicycle error which is more informative
		// than the original "two-wheeler unsupported" message.
		return routes.ComputeResponse{}, fbErr
	}
	return routes.ComputeResponse{}, err
}

// computeOnce performs one request with a specific travel mode.
func (p *Provider) computeOnce(ctx context.Context, req routes.ComputeRequest, mode routes.TravelMode) (routes.ComputeResponse, error) {
	body, err := buildRequestBody(req, mode)
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("google: build request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.baseURL+computePath, bytes.NewReader(body))
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("google: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Goog-Api-Key", p.apiKey)
	httpReq.Header.Set("X-Goog-FieldMask", fieldMask)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		// Network/timeout — treat as upstream. Do not echo the URL with key
		// even though we never put the key in the URL; defense in depth.
		return routes.ComputeResponse{}, fmt.Errorf("%w: %v", routes.ErrUpstream, classifyTransportError(err))
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("%w: read body: %v", routes.ErrUpstream, err)
	}

	switch httpResp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusUnauthorized, http.StatusForbidden:
		return routes.ComputeResponse{}, routes.ErrUpstreamAuth
	case http.StatusBadRequest:
		return routes.ComputeResponse{}, fmt.Errorf("%w: status=%d", routes.ErrUpstreamValidation, httpResp.StatusCode)
	case http.StatusTooManyRequests:
		return routes.ComputeResponse{}, fmt.Errorf("%w: status=429", routes.ErrUpstream)
	default:
		if httpResp.StatusCode >= 500 {
			return routes.ComputeResponse{}, fmt.Errorf("%w: status=%d", routes.ErrUpstream, httpResp.StatusCode)
		}
		return routes.ComputeResponse{}, fmt.Errorf("%w: status=%d", routes.ErrUpstream, httpResp.StatusCode)
	}

	var parsed computeResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("%w: decode: %v", routes.ErrUpstream, err)
	}
	if len(parsed.Routes) == 0 {
		return routes.ComputeResponse{}, routes.ErrNoRoute
	}
	r := parsed.Routes[0]

	durationSeconds, err := parseDurationSeconds(r.Duration)
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("%w: parse duration: %v", routes.ErrUpstream, err)
	}

	return routes.ComputeResponse{
		DistanceMeters:  r.DistanceMeters,
		DurationSeconds: durationSeconds,
		EncodedPolyline: r.Polyline.EncodedPolyline,
		Provider:        providerName,
		TravelMode:      mode,
		ComputedAt:      time.Now().UTC(),
	}, nil
}

// ─── request/response shapes ─────────────────────────────────────────────

type latLng struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type endpoint struct {
	Location struct {
		LatLng latLng `json:"latLng"`
	} `json:"location"`
}

type routeModifiers struct {
	AvoidTolls bool `json:"avoidTolls,omitempty"`
}

type computeRequest struct {
	Origin            endpoint       `json:"origin"`
	Destination       endpoint       `json:"destination"`
	TravelMode        string         `json:"travelMode"`
	PolylineEncoding  string         `json:"polylineEncoding"`
	Units             string         `json:"units"`
	RouteModifiers    routeModifiers `json:"routeModifiers,omitempty"`
}

type computeResponse struct {
	Routes []struct {
		DistanceMeters int    `json:"distanceMeters"`
		Duration       string `json:"duration"` // protobuf duration, e.g. "1234s"
		Polyline       struct {
			EncodedPolyline string `json:"encodedPolyline"`
		} `json:"polyline"`
	} `json:"routes"`
}

func buildRequestBody(req routes.ComputeRequest, mode routes.TravelMode) ([]byte, error) {
	body := computeRequest{
		TravelMode:       mapTravelMode(mode),
		PolylineEncoding: "ENCODED_POLYLINE",
		Units:            "METRIC",
		RouteModifiers:   routeModifiers{AvoidTolls: req.AvoidTolls},
	}
	body.Origin.Location.LatLng = latLng{Latitude: req.Origin.Latitude, Longitude: req.Origin.Longitude}
	body.Destination.Location.LatLng = latLng{Latitude: req.Destination.Latitude, Longitude: req.Destination.Longitude}
	return json.Marshal(body)
}

// mapTravelMode translates routes.TravelMode to the wire enum Google expects.
// All three modes are supported in the API surface; region restrictions for
// TWO_WHEELER are handled by the fallback in Compute.
func mapTravelMode(m routes.TravelMode) string {
	switch m {
	case routes.TravelModeTwoWheeler:
		return "TWO_WHEELER"
	case routes.TravelModeBicycle:
		return "BICYCLE"
	case routes.TravelModeWalk:
		return "WALK"
	default:
		// Validate runs before this; if we reach here it's a bug.
		return string(m)
	}
}

// parseDurationSeconds parses Google's protobuf Duration JSON form, which
// is a string like "1234s" or "1234.5s". We accept both integer and
// fractional seconds and return rounded int seconds to match the API
// response shape.
func parseDurationSeconds(s string) (int, error) {
	if !strings.HasSuffix(s, "s") {
		return 0, fmt.Errorf("expected trailing 's', got %q", s)
	}
	num := strings.TrimSuffix(s, "s")
	// Try integer first to avoid floating-point rounding for the common case.
	if !strings.Contains(num, ".") {
		var v int
		if _, err := fmt.Sscanf(num, "%d", &v); err != nil {
			return 0, err
		}
		return v, nil
	}
	var f float64
	if _, err := fmt.Sscanf(num, "%f", &f); err != nil {
		return 0, err
	}
	return int(f + 0.5), nil
}

// classifyTransportError trims potentially-sensitive URL fragments from
// transport errors before they reach logs. The Google adapter never puts
// the key in the URL, but this is defense-in-depth for any future change.
func classifyTransportError(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, "?"); i > 0 {
		msg = msg[:i] + "?<redacted>"
	}
	return errors.New(msg)
}
