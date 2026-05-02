# Route Provider Strategy — RideAndConnect

> Companion to API §3.4–§3.5 and DATABASE_SCHEMA §3.5. All keys/tokens are server-side only — never returned to clients.
> Coding Standards: must comply with `coding_standards.json` (root).

## 1. Goals
- **G1** Compute a routable path for two cases:
  1. **Destination route** — from a single rider's origin to the ride's shared destination.
  2. **Peer route** — from rider A's current location to rider B's current location (the "tap-on-rider-B-to-see-distance" UX).
- **G2** Return a stable response shape: `distanceMeters`, `durationSeconds`, `encodedPolyline`, `provider`, `travelMode`.
- **G3** Support **two providers from day 1** behind a single Go interface:
  - **Google Routes API v2** (default in many regions; supports `TWO_WHEELER`).
  - **Mapbox Directions API** (default in regions where Google two-wheeler is unavailable; supports cycling/walking/driving).
- **G4** Switching providers is a single config change (`ROUTE_PROVIDER=google` or `mapbox`).
- **G5** Bound provider spend through caching, throttling, and a circuit breaker.
- **G6** Never expose any provider key to clients.

## 2. Provider Abstraction (Go interface)

```go
// internal/routes/provider.go
package routes

type TravelMode string
const (
    TravelModeTwoWheeler TravelMode = "TWO_WHEELER"
    TravelModeBicycle    TravelMode = "BICYCLE"
    TravelModeWalk       TravelMode = "WALK"
)

type Point struct {
    Latitude  float64
    Longitude float64
}

type ComputeRequest struct {
    Origin      Point
    Destination Point
    TravelMode  TravelMode
    AvoidTolls  bool
}

type ComputeResponse struct {
    DistanceMeters  int
    DurationSeconds int
    EncodedPolyline string
    Provider        string
    TravelMode      TravelMode
    ComputedAt      time.Time
}

type Capabilities struct {
    SupportsTwoWheeler bool
    SupportsCycling    bool
    SupportsWalking    bool
}

type Provider interface {
    Name() string
    Compute(ctx context.Context, req ComputeRequest) (ComputeResponse, error)
    Capabilities() Capabilities
}
```

A factory in `internal/routes/factory.go` maps a config name (`google`, `mapbox`) to a Provider. Both adapters implement the interface and live in their own subpackages so a single env change picks the active one.

## 3. Provider 1 — Google Routes API v2

### 3.1 Endpoint
- `POST https://routes.googleapis.com/directions/v2:computeRoutes`
- Auth: `X-Goog-Api-Key: $GOOGLE_ROUTES_API_KEY` header.
- Field mask required:
  - `X-Goog-FieldMask: routes.distanceMeters,routes.duration,routes.polyline.encodedPolyline`
- Field mask is a billing-cost lever — keep it minimal.

### 3.2 Request Body Mapping
```json
{
  "origin":      { "location": { "latLng": { "latitude": ..., "longitude": ... } } },
  "destination": { "location": { "latLng": { "latitude": ..., "longitude": ... } } },
  "travelMode": "TWO_WHEELER",     // or "BICYCLE", "WALK"
  "polylineEncoding": "ENCODED_POLYLINE",
  "units": "METRIC",
  "routeModifiers": { "avoidTolls": false }
}
```
- `travelMode`: `TWO_WHEELER` requested where regionally supported; otherwise the adapter falls back to `BICYCLE`. The response always echoes the actually-used mode.

### 3.3 Response Mapping
```json
{
  "routes": [{
    "distanceMeters": 12345,
    "duration": "1234s",
    "polyline": { "encodedPolyline": "..." }
  }]
}
```
- `routes[0].distanceMeters` → `DistanceMeters`
- `routes[0].duration` (Protobuf duration string) → parsed → `DurationSeconds`
- `routes[0].polyline.encodedPolyline` → `EncodedPolyline`
- `Provider = "google_routes"`

### 3.4 Errors
| Provider response | Adapter behavior |
|---|---|
| 200 with empty `routes` | `ErrNoRoute` → 422 `UNPROCESSABLE` |
| 400 | wrap as `VALIDATION_FAILED` (provider message in dev only) |
| 401/403 | `ErrUpstream` + `provider_auth_failed` log; pages oncall in prod |
| 429 | `ErrUpstream` (throttled); circuit breaker increment; honor `Retry-After` |
| 5xx | `ErrUpstream`; circuit breaker increment |
| Network/timeout | `ErrUpstream` |

### 3.5 Two-Wheeler Region Fallback
Google's `TWO_WHEELER` is region-restricted (India, Indonesia, Vietnam, etc.). Outside the configured allowlist, the adapter rewrites `travelMode` to `BICYCLE` and reflects this in `ComputeResponse.TravelMode`.

## 4. Provider 2 — Mapbox Directions API

### 4.1 Endpoint
- `GET https://api.mapbox.com/directions/v5/mapbox/{profile}/{lon1,lat1};{lon2,lat2}`
- Auth: query param `?access_token=$MAPBOX_ACCESS_TOKEN`. (Mapbox supports header auth on some endpoints; v1 uses the query param for portability across Mapbox APIs.)

### 4.2 Profile Mapping
Mapbox profiles: `driving`, `driving-traffic`, `cycling`, `walking`. The adapter maps via env config:

| App `TravelMode` | Default Mapbox profile | Env override |
|---|---|---|
| `TWO_WHEELER` | `cycling` (RideAndConnect is a bike app) | `MAPBOX_PROFILE_TWO_WHEELER` (set to `driving` for motorcycle apps) |
| `BICYCLE` | `cycling` | `MAPBOX_PROFILE_BICYCLE` |
| `WALK` | `walking` | `MAPBOX_PROFILE_WALK` |

### 4.3 Query Parameters
- `geometries=polyline` — encoded polyline (matches Google's encoding so downstream renderers don't branch).
- `overview=full` — full geometry, not simplified.
- `steps=false` — we don't need turn-by-turn instructions on the server.
- `annotations=` (none in v1, cost lever).
- `alternatives=false`.
- `access_token=...`.

Coordinates in URL: **`lon,lat`** order, semicolon-separated. (Beware: Mapbox URLs are `lon,lat`; Google's JSON body is explicit `latitude`/`longitude`.)

### 4.4 Response Mapping
```json
{
  "code": "Ok",
  "routes": [{
    "distance": 12345.67,
    "duration": 1234.5,
    "geometry": "encoded_polyline_here"
  }]
}
```
- `routes[0].distance` (float meters) → `int(round(distance))` → `DistanceMeters`
- `routes[0].duration` (float seconds) → `int(round(duration))` → `DurationSeconds`
- `routes[0].geometry` (encoded polyline) → `EncodedPolyline`
- `Provider = "mapbox"`

### 4.5 Errors
| Provider response | Adapter behavior |
|---|---|
| `code: "Ok"`, `routes: []` | `ErrNoRoute` → 422 `UNPROCESSABLE` |
| `code: "NoRoute"` | `ErrNoRoute` |
| `code: "InvalidInput"` (HTTP 422) | `VALIDATION_FAILED` |
| HTTP 401 | `ErrUpstream` + `provider_auth_failed` log |
| HTTP 429 | `ErrUpstream` (throttled); circuit breaker increment |
| HTTP 5xx | `ErrUpstream`; circuit breaker increment |
| Network/timeout | `ErrUpstream` |

### 4.6 Capabilities
```go
Capabilities{
  SupportsTwoWheeler: false, // mapped to cycling/driving via profile env
  SupportsCycling:    true,
  SupportsWalking:    true,
}
```

## 5. Provider Selection
- `ROUTE_PROVIDER` env var: `google` | `mapbox`. Single value, applies to all compute calls (destination + peer).
- v1 ships both adapters; the inactive one stays unwired but importable so swapping is one env change.
- v2 may add fallback chain (try Mapbox if Google fails) — deferred.

## 6. Peer Route Distance (NEW)

### 6.1 Trigger
GraphQL `peerRoute(rideId, peerUserId, input)` query — see API §3.5.

### 6.2 Origin & Destination Resolution
- **Origin** — caller's last-known location from the WS Hub's in-memory presence; or `input.originOverride` if the caller passes a fresher position.
- **Destination** — peer's last-known location from the WS Hub's in-memory presence (the peer's own `originOverride` is not honored; only their server-observed broadcasts are used).

The Hub keeps a `lastLocation { lat, lng, at }` per `(rideId, userId)` updated on each successful inbound `location.update`. No DB persistence — same privacy posture as MVP.

### 6.3 Staleness Guard
- `PEER_ROUTE_STALE_TTL` (default `30s`).
- If either side's location is missing → `PEER_LOCATION_MISSING`.
- If either side's location is older than the TTL → `PEER_LOCATION_STALE` with `extensions.peerLastSeenSecondsAgo`.
- Caller-side override only avoids the caller-side staleness check; the peer-side check is mandatory.

### 6.4 Provider Call
Same `Provider.Compute` as destination routes. Travel mode comes from input (default `TWO_WHEELER`). The provider sees only two coordinates; it does not know it's a peer route.

### 6.5 Cache
- Stored in `ride_routes` with `route_kind = 'PEER'` and `peer_user_id = <toUserId>`.
- Cache key: `(rideId, fromUserId = caller, toUserId = peerUserId, fromGeohash7, toGeohash7, travelMode, route_kind='PEER')`.
- TTL: shorter than destination cache because positions move — `PEER_ROUTE_CACHE_TTL` default `60s`.
- Lookup: latest non-expired row matching the key.

### 6.6 Throttling
- 1 / 30 s per ordered `(rideId, fromUserId, toUserId)` pair.
- 12 / 10 min per ride across all peer pairs.

### 6.7 Authorization
- Caller and `peerUserId` must both be active members of `rideId`.
- `peerUserId ≠ caller`.
- Standard ride membership middleware enforces caller's membership before the resolver runs.

### 6.8 Why a real route, not Haversine
The user requirement is explicit: distance must reflect actual road distance, not great-circle distance. A peer 200 m away as the crow flies might be 1.4 km away by route (one-way streets, no through-roads, river crossings). The peer route always goes through the configured Provider.

## 7. Caching (combined for destination + peer)

### 7.1 Cache Key
```
(ride_id, route_kind, user_id, peer_user_id_or_null,
 origin_geohash7, destination_geohash7, travel_mode)
```

- Geohash precision 7 ≈ 153 m × 153 m bucket.
- TTL: `ROUTE_CACHE_TTL` (default 30 min) for destination routes; `PEER_ROUTE_CACHE_TTL` (default 60 s) for peer routes.
- Storage: `ride_routes` table (see DATABASE_SCHEMA §3.5).

### 7.2 Lookup Path
1. Compute geohashes.
2. SELECT latest non-expired row matching the key.
3. Hit → return with `cacheHit = true`.
4. Miss → call provider, INSERT row, return with `cacheHit = false`.

### 7.3 Invalidation
- `setDestination` deletes/expires all `ride_routes` rows for that ride with `route_kind='DESTINATION'`. Peer-route rows are unaffected.
- `endRide` cascades-delete via `ON DELETE CASCADE` on `ride_id`.

## 8. Throttling

| Layer | Limit | Action on excess |
|---|---|---|
| `computeRoute` per user × ride | 1 / 30 s (token bucket size 2) | `RATE_LIMITED` + `Retry-After` |
| `computeRoute` per ride | 6 / 10 min | `RATE_LIMITED` |
| `peerRoute` per (caller, peer) ordered pair | 1 / 30 s | `RATE_LIMITED` |
| `peerRoute` per ride | 12 / 10 min | `RATE_LIMITED` |
| Provider HTTP — global | per-provider config (e.g. 100 rps default) | request queued briefly, then `ErrUpstream` |

Buckets are in-memory with a 24 h sweeper (acceptable for single-instance v1). Future: Redis for horizontal scale.

## 9. Circuit Breaker

- Library: `sony/gobreaker`.
- Per provider (Google and Mapbox have independent breakers).
- States: closed → open (after 5 consecutive failures or ≥ 50 % failure rate over 20 calls) → half-open (after 30 s).
- While open: provider calls short-circuit with `ErrCircuitOpen`; resolver returns `UPSTREAM_UNAVAILABLE`.
- Cache hits continue to serve while the breaker is open.

## 10. Cost Controls

- **Field mask / query params** kept minimal (cost levers).
- **Cache TTLs** tuned per route kind.
- **Budget metric**: `route_provider_calls_total{provider, outcome}` counter and `route_provider_cost_estimate{provider}` gauge.
- **Hard cap**: optional env `ROUTE_PROVIDER_DAILY_CAP` per provider. Once exceeded, adapter short-circuits with 503 until next UTC day.

## 11. Observability

Metrics:
- `route_provider_calls_total{provider,outcome}` — `outcome ∈ success|error|circuit_open|throttled|cache_hit|cache_miss|no_route`
- `route_provider_latency_seconds{provider}` (histogram)
- `route_cache_lookups_total{result,kind}` — `result ∈ hit|miss`, `kind ∈ destination|peer`
- `route_provider_breaker_state{provider}` — gauge `0=closed, 1=half_open, 2=open`
- `peer_route_staleness_seconds` — histogram of peer-side last-seen ages on resolver entry
- `peer_route_failures_total{reason}` — `reason ∈ stale|missing|throttled|forbidden`

Logs (no coordinates, no polylines):
- `route.compute.start` with `ride_id`, `user_id`, `travel_mode`, `kind` (`destination`/`peer`)
- `route.compute.complete` with `distance_meters`, `duration_seconds`, `cache_hit`, `provider`, `kind`
- `route.compute.error` with provider error class only (no body)

## 12. Security

- API key/token only via env (`GOOGLE_ROUTES_API_KEY`, `MAPBOX_ACCESS_TOKEN`).
- The HTTP client wrapping each provider strips key/token from any error/log context.
- Each adapter's endpoint is a constant; caller-supplied URLs/paths are not accepted.
- Outbound TLS verification on (Go default).
- Per-call request timeout `10 s`; total deadline derived from inbound `context.Context`.
- Adapters never return raw provider responses to clients — only the mapped `ComputeResponse`.

## 13. Adding More Providers
1. Implement `routes.Provider` in `internal/routes/<name>/provider.go`.
2. Register in `internal/routes/factory.go` keyed by name.
3. Set `ROUTE_PROVIDER=<name>` and provider-specific env vars.
4. Add a recorded-fixture contract test under `internal/routes/<name>/testdata/` (no live calls in unit tests).

## 14. Failure Modes Summary

| Failure | Cache hit? | Behavior |
|---|---|---|
| Provider 5xx | yes | Serve cache; record provider error; do not extend TTL |
| Provider 5xx | no | `UPSTREAM_UNAVAILABLE`, `Retry-After` |
| Provider 429 | yes | Serve cache |
| Provider 429 | no | `UPSTREAM_UNAVAILABLE`; open breaker |
| Provider 0-routes | n/a | `UNPROCESSABLE` (`ErrNoRoute`) |
| Caller throttle exceeded | n/a | `RATE_LIMITED` with `retryAfterSeconds` |
| Peer location stale/missing | n/a | `PEER_LOCATION_STALE` / `PEER_LOCATION_MISSING` (no provider call) |
| Daily cap exceeded | yes | Serve cache only |
| Daily cap exceeded | no | `UPSTREAM_UNAVAILABLE` |
