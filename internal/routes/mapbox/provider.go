// Package mapbox is the routes.Provider adapter for the Mapbox Directions
// API v5. See docs/ROUTE_PROVIDER.md §4 for the strategy.
//
// The adapter is intentionally narrow — it does not cache, throttle, or
// open circuit breakers; those concerns live one layer up in
// internal/routes (or its callers). This file is one of two parallel
// adapters; the other is internal/routes/google. Switching providers is a
// single env change (ROUTE_PROVIDER=google or mapbox).
//
// URL format (for reference):
//
//	GET {base}/directions/v5/mapbox/{profile}/{lon1,lat1};{lon2,lat2}
//	    ?access_token=...&geometries=polyline&overview=full&steps=false
//
// Note the lon,lat order in the path — Google's JSON body uses explicit
// latitude/longitude fields, but Mapbox URLs are lon,lat.
package mapbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
)

// providerName is the stable identifier persisted to ride_routes.provider
// and exposed in API responses.
const providerName = "mapbox"

// defaultBaseURL is the production endpoint base. Tests override this via
// Config.BaseURL to point at httptest.Server.
const defaultBaseURL = "https://api.mapbox.com"

// directionsPathTmpl is the path template; %s placeholders are
// {profile} and {coordinates}.
const directionsPathTmpl = "/directions/v5/mapbox/%s/%s"

// Mapbox profile names. We treat them as constants here; the Config maps
// our internal TravelMode to one of these.
const (
	profileCycling = "cycling"
	profileWalking = "walking"
	profileDriving = "driving"
)

// defaultTimeout caps any single HTTP call. The caller's context may
// shorten this further.
const defaultTimeout = 10 * time.Second

// Config configures the adapter. AccessToken is required. The profile
// fields let the operator decide whether TWO_WHEELER means cycling
// (default — RideAndConnect is a bike app) or driving (motorcycle apps).
type Config struct {
	AccessToken string
	BaseURL     string
	HTTPClient  *http.Client
	Timeout     time.Duration

	// Profile mapping. Empty values fall back to documented defaults.
	ProfileTwoWheeler string // default: "cycling"
	ProfileBicycle    string // default: "cycling"
	ProfileWalk       string // default: "walking"
}

// Provider implements routes.Provider against Mapbox Directions API v5.
type Provider struct {
	accessToken string
	baseURL     string
	httpClient  *http.Client
	timeout     time.Duration

	profileTwoWheeler string
	profileBicycle    string
	profileWalk       string
}

// New constructs a Provider. Returns an error if the access token is
// missing — configuration errors are caught at startup, not at first
// request.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.AccessToken) == "" {
		return nil, fmt.Errorf("%w: MAPBOX_ACCESS_TOKEN is empty", routes.ErrProviderUnconfigured)
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

	twoWheeler := strings.ToLower(strings.TrimSpace(cfg.ProfileTwoWheeler))
	if twoWheeler == "" {
		twoWheeler = profileCycling
	}
	bicycle := strings.ToLower(strings.TrimSpace(cfg.ProfileBicycle))
	if bicycle == "" {
		bicycle = profileCycling
	}
	walk := strings.ToLower(strings.TrimSpace(cfg.ProfileWalk))
	if walk == "" {
		walk = profileWalking
	}
	if !isKnownProfile(twoWheeler) || !isKnownProfile(bicycle) || !isKnownProfile(walk) {
		return nil, fmt.Errorf("%w: unknown Mapbox profile in config", routes.ErrProviderUnconfigured)
	}

	return &Provider{
		accessToken:       cfg.AccessToken,
		baseURL:           base,
		httpClient:        hc,
		timeout:           to,
		profileTwoWheeler: twoWheeler,
		profileBicycle:    bicycle,
		profileWalk:       walk,
	}, nil
}

// Name returns the stable identifier.
func (p *Provider) Name() string { return providerName }

// Capabilities reports static support flags. Mapbox has no native
// two-wheeler profile, so SupportsTwoWheeler is false; the configured
// fallback profile (cycling or driving) handles that mode.
func (p *Provider) Capabilities() routes.Capabilities {
	return routes.Capabilities{
		SupportsTwoWheeler: false,
		SupportsCycling:    true,
		SupportsWalking:    true,
	}
}

// Compute calls the Directions API and maps the response into a
// routes.ComputeResponse. The travel mode echoed back is always the input
// mode (no fallback rewrite at the API surface — the profile choice
// already encodes the operator's preference).
func (p *Provider) Compute(ctx context.Context, req routes.ComputeRequest) (routes.ComputeResponse, error) {
	if err := req.Validate(); err != nil {
		return routes.ComputeResponse{}, err
	}

	profile, err := p.profileFor(req.TravelMode)
	if err != nil {
		return routes.ComputeResponse{}, err
	}

	endpoint, err := p.buildURL(profile, req.Origin, req.Destination)
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("mapbox: build url: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("mapbox: new request: %w", err)
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("%w: %v", routes.ErrUpstream, classifyTransportError(err))
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("%w: read body: %v", routes.ErrUpstream, err)
	}

	switch httpResp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusUnauthorized, http.StatusForbidden:
		return routes.ComputeResponse{}, routes.ErrUpstreamAuth
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		// Mapbox returns 422 with `code: "NoRoute"` for legitimately
		// unroutable inputs and 422 with other codes for malformed input.
		// We try to disambiguate via the body before falling back to
		// upstream-validation.
		if isNoRouteBody(body) {
			return routes.ComputeResponse{}, routes.ErrNoRoute
		}
		return routes.ComputeResponse{}, fmt.Errorf("%w: status=%d", routes.ErrUpstreamValidation, httpResp.StatusCode)
	case http.StatusTooManyRequests:
		return routes.ComputeResponse{}, fmt.Errorf("%w: status=429", routes.ErrUpstream)
	default:
		if httpResp.StatusCode >= 500 {
			return routes.ComputeResponse{}, fmt.Errorf("%w: status=%d", routes.ErrUpstream, httpResp.StatusCode)
		}
		return routes.ComputeResponse{}, fmt.Errorf("%w: status=%d", routes.ErrUpstream, httpResp.StatusCode)
	}

	var parsed directionsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return routes.ComputeResponse{}, fmt.Errorf("%w: decode: %v", routes.ErrUpstream, err)
	}
	if parsed.Code != "" && !strings.EqualFold(parsed.Code, "Ok") {
		// Codes other than "Ok": "NoRoute", "NoSegment", "InvalidInput", etc.
		if strings.EqualFold(parsed.Code, "NoRoute") || strings.EqualFold(parsed.Code, "NoSegment") {
			return routes.ComputeResponse{}, routes.ErrNoRoute
		}
		return routes.ComputeResponse{}, fmt.Errorf("%w: provider code=%s", routes.ErrUpstreamValidation, parsed.Code)
	}
	if len(parsed.Routes) == 0 {
		return routes.ComputeResponse{}, routes.ErrNoRoute
	}
	r := parsed.Routes[0]

	return routes.ComputeResponse{
		DistanceMeters:  int(r.Distance + 0.5),
		DurationSeconds: int(r.Duration + 0.5),
		EncodedPolyline: r.Geometry,
		Provider:        providerName,
		TravelMode:      req.TravelMode,
		ComputedAt:      time.Now().UTC(),
	}, nil
}

// profileFor maps our TravelMode to a Mapbox profile per Config.
func (p *Provider) profileFor(m routes.TravelMode) (string, error) {
	switch m {
	case routes.TravelModeTwoWheeler:
		return p.profileTwoWheeler, nil
	case routes.TravelModeBicycle:
		return p.profileBicycle, nil
	case routes.TravelModeWalk:
		return p.profileWalk, nil
	default:
		return "", routes.ErrInvalidTravelMode
	}
}

// buildURL constructs the Directions request URL. The access token is
// included as a query parameter — Mapbox's standard auth scheme — and
// is intentionally NOT logged elsewhere; classifyTransportError redacts
// the query string from any error surface.
func (p *Provider) buildURL(profile string, origin, destination routes.Point) (string, error) {
	// Path is built manually rather than via url.PathEscape so we don't
	// percent-encode the comma/semicolon — Mapbox requires them literal.
	path := fmt.Sprintf(
		directionsPathTmpl,
		profile,
		formatCoordsPair(origin, destination),
	)
	u, err := url.Parse(p.baseURL + path)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("access_token", p.accessToken)
	q.Set("geometries", "polyline")
	q.Set("overview", "full")
	q.Set("steps", "false")
	q.Set("alternatives", "false")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// formatCoordsPair returns "lon1,lat1;lon2,lat2" with up to 6 decimal
// places — matches the documented coordinate precision (≈ 11 cm).
func formatCoordsPair(a, b routes.Point) string {
	return fmt.Sprintf("%s,%s;%s,%s",
		formatDeg(a.Longitude), formatDeg(a.Latitude),
		formatDeg(b.Longitude), formatDeg(b.Latitude),
	)
}

// formatDeg trims the float to 6 decimal places without trailing zeros.
func formatDeg(f float64) string {
	s := strconv.FormatFloat(f, 'f', 6, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

// isNoRouteBody peeks at a 4xx response body to see if Mapbox flagged it
// as a NoRoute error rather than a malformed-input error.
func isNoRouteBody(body []byte) bool {
	var head struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return false
	}
	return strings.EqualFold(head.Code, "NoRoute") || strings.EqualFold(head.Code, "NoSegment")
}

// isKnownProfile is a sanity check at startup. Mapbox ships several
// profiles; we accept the ones we map to.
func isKnownProfile(name string) bool {
	switch name {
	case profileCycling, profileWalking, profileDriving, "driving-traffic":
		return true
	default:
		return false
	}
}

// ─── response shapes ─────────────────────────────────────────────────────

type directionsResponse struct {
	Code   string `json:"code"`
	Routes []struct {
		Distance float64 `json:"distance"` // meters
		Duration float64 `json:"duration"` // seconds
		Geometry string  `json:"geometry"` // encoded polyline (geometries=polyline)
	} `json:"routes"`
}

// classifyTransportError trims the query string (which carries the access
// token) from transport errors before they reach logs.
func classifyTransportError(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, "?access_token="); i > 0 {
		msg = msg[:i] + "?<redacted>"
	} else if i := strings.Index(msg, "?"); i > 0 {
		msg = msg[:i] + "?<redacted>"
	}
	return errors.New(msg)
}
