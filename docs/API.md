# API Contract — RideAndConnect (GraphQL)

> Companion to PRD, ARCHITECTURE, WEBSOCKET_PROTOCOL, DATABASE_SCHEMA, ROUTE_PROVIDER, SECURITY.
> The API surface is **GraphQL**; the realtime channel is the custom **WebSocket** protocol in WEBSOCKET_PROTOCOL.md (TOON + QoS at 1–60 kbps). REST has been retired except for the operational meta endpoints (`/healthz`, `/readyz`, `/version`) and the WS upgrade.
> Coding Standards: must comply with `coding_standards.json` (root).

## 1. Conventions

- **Endpoint** — single POST endpoint: `https://api.rideandconnect.example/api/v1/graphql`
- **Versioning** — schema-versioned via `/v1`. Breaking changes bump the prefix.
- **Method** — `POST` only for queries and mutations. `GET` is rejected (avoids CSRF mounting and accidental caching).
- **Content type** — `application/json; charset=utf-8` for both request and response bodies.
- **Auth** — `Authorization: Bearer <jwt>` for all operations except the `register`, `login`, and `refresh` mutations.
- **Field casing** — **camelCase** in the GraphQL schema (language idiom). The independent WS protocol keeps `snake_case` per WEBSOCKET_PROTOCOL.md.
- **Time format** — RFC 3339 / ISO 8601 in UTC (custom `DateTime` scalar).
- **IDs** — UUIDv4 strings, custom `UUID` scalar, server-generated.
- **Coordinates** — WGS84 decimal degrees; `Latitude` ∈ `[-90, 90]`, `Longitude` ∈ `[-180, 180]`; up to 6 decimal places (≈11 cm).

### 1.1 Request Shape
```http
POST /api/v1/graphql HTTP/1.1
Authorization: Bearer <jwt>
Content-Type: application/json
Accept: application/json
Accept-Encoding: br, gzip

{
  "query": "...",
  "variables": { ... },
  "operationName": "..."
}
```

### 1.2 Response Shape
```json
{
  "data":   { ... },
  "errors": [
    {
      "message": "Ride already has the maximum of 3 members.",
      "path":    ["joinRide"],
      "extensions": {
        "code":      "RIDE_FULL",
        "rideId":    "01931f...",
        "requestId": "8e2c..."
      }
    }
  ]
}
```

GraphQL itself returns HTTP 200 for almost everything; semantic failures are reported as entries in the `errors` array. The server emits HTTP 4xx/5xx only for: malformed JSON (400), missing/invalid auth on a non-public op (401), payload too large (413), throttled at the transport layer (429), DB or upstream outage (503).

### 1.3 Error Codes (`extensions.code`)
| Code | Meaning |
|---|---|
| `INVALID_PAYLOAD` | Malformed JSON or invalid GraphQL syntax |
| `VALIDATION_FAILED` | Field-level constraint violation |
| `UNAUTHENTICATED` | Missing/invalid/expired token |
| `INVALID_CREDENTIALS` | Login mismatch |
| `FORBIDDEN` | Authenticated but not authorized (not a member) |
| `NOT_FOUND` | Resource does not exist or not visible |
| `CONFLICT` | State conflict (e.g. ride already ended) |
| `RIDE_FULL` | 3-member cap reached |
| `ALREADY_MEMBER` | Caller already in this ride |
| `EMAIL_TAKEN` | Email already registered |
| `INVITE_INVALID` | Invite code unknown or invalidated |
| `PEER_LOCATION_STALE` | Peer's last-known location older than `PEER_ROUTE_STALE_TTL` |
| `PEER_LOCATION_MISSING` | Peer has no known location yet |
| `RATE_LIMITED` | Throttled; honor `extensions.retryAfterSeconds` |
| `UPSTREAM_UNAVAILABLE` | Route provider or DB unavailable |
| `INTERNAL` | Unexpected server error |
| `QUERY_DEPTH_EXCEEDED` | Hardening: query nesting beyond limit |
| `QUERY_COMPLEXITY_EXCEEDED` | Hardening: complexity score beyond limit |

### 1.4 Standard Headers
- Request: `Authorization`, `Content-Type`, `Accept`, `Accept-Encoding`, `X-Request-Id` (optional).
- Response: `Content-Type`, `Content-Encoding`, `X-Request-Id`, `Retry-After` on 429/503.

### 1.5 Compression
- Server emits `Content-Encoding: br` (preferred) or `gzip` for responses ≥ 256 bytes when `Accept-Encoding` allows.
- **Auth mutations (`register`, `login`, `refresh`) are served with `Content-Encoding: identity`** to defuse CRIME/BREACH-class concerns (see SECURITY).
- Decompressed request body cap remains 64 KB.

### 1.6 GraphQL Hardening (enforced by middleware)
- `introspection` disabled in `prod` (`APP_ENV=prod`); enabled in `dev`/`staging`.
- Query depth limit: 8 (configurable via `GRAPHQL_DEPTH_LIMIT`).
- Query complexity limit: 200 (configurable via `GRAPHQL_COMPLEXITY_LIMIT`).
- Operation type allowlist: `query`, `mutation`. (No `subscription` — realtime lives on the custom WS.)
- Request body cap: 64 KB.
- Persisted queries: not in v1; tracked for v2 if cost optimization needs it.

## 2. GraphQL Schema (canonical)

```graphql
# ───── Custom scalars ─────
scalar UUID         # canonical UUIDv4 string, e.g. "01931f8a-..."
scalar DateTime     # RFC 3339 UTC, e.g. "2026-05-02T10:30:00Z"
scalar Latitude     # Float in [-90, 90]
scalar Longitude    # Float in [-180, 180]

# ───── Enums ─────
enum RideStatus     { ACTIVE ENDED }
enum RideMemberRole { OWNER MEMBER }
enum TravelMode     { TWO_WHEELER BICYCLE WALK }

# ───── Object types ─────
type User {
  id:        UUID!
  name:      String!
  email:     String        # nullable in schema for phone-only future
  createdAt: DateTime!
}

type AuthPayload {
  user:         User!
  accessToken:  String!
  refreshToken: String!
  expiresIn:    Int!       # seconds until access token expires
}

type WsTicket {
  ticket:    String!
  expiresIn: Int!          # seconds (default 300)
}

type Coordinates {
  latitude:  Latitude!
  longitude: Longitude!
}

type Destination {
  name:       String!
  placeId:    String
  latitude:   Latitude!
  longitude:  Longitude!
  updatedAt:  DateTime!
}

type RideMember {
  userId:    UUID!
  name:      String!
  role:      RideMemberRole!
  joinedAt:  DateTime!
  leftAt:    DateTime
}

type Ride {
  id:           UUID!
  ownerUserId:  UUID!
  inviteCode:   String!
  status:       RideStatus!
  destination:  Destination
  startedAt:    DateTime!
  endedAt:      DateTime
  createdAt:    DateTime!
  members:      [RideMember!]!
}

# Route from a single rider's origin to the shared destination.
type Route {
  rideId:           UUID!
  userId:           UUID!
  origin:           Coordinates!
  destination:      Coordinates!
  destinationName:  String!
  distanceMeters:   Int!
  durationSeconds:  Int!
  encodedPolyline:  String!
  provider:         String!     # "google_routes" | "mapbox"
  travelMode:       TravelMode!
  computedAt:       DateTime!
  cacheHit:         Boolean!
}

# Map-route distance between two riders in the same ride.
type PeerRoute {
  rideId:           UUID!
  fromUserId:       UUID!
  toUserId:         UUID!
  fromLocation:     Coordinates!
  toLocation:       Coordinates!
  fromLocationAt:   DateTime!   # when fromLocation was last reported
  toLocationAt:     DateTime!   # when toLocation was last reported
  distanceMeters:   Int!
  durationSeconds:  Int!
  encodedPolyline:  String!
  provider:         String!
  travelMode:       TravelMode!
  computedAt:       DateTime!
  cacheHit:         Boolean!
}

# ───── Inputs ─────
input CoordinatesInput {
  latitude:  Latitude!
  longitude: Longitude!
}

input RegisterInput {
  name:     String!
  email:    String!
  password: String!
}

input LoginInput {
  email:    String!
  password: String!
}

input DestinationInput {
  name:      String!
  placeId:   String
  latitude:  Latitude!
  longitude: Longitude!
}

input ComputeRouteInput {
  origin:     CoordinatesInput!
  travelMode: TravelMode = TWO_WHEELER
}

input PeerRouteInput {
  travelMode:     TravelMode = TWO_WHEELER
  originOverride: CoordinatesInput   # optional: caller may pass a fresher origin
}

# ───── Operations ─────
type Query {
  me:                                                  User!
  ride(rideId: UUID!):                                 Ride!
  rideMembers(rideId: UUID!):                          [RideMember!]!
  destination(rideId: UUID!):                          Destination
  myRoute(rideId: UUID!):                              Route                # most recent cached route to destination
  peerRoute(rideId: UUID!, peerUserId: UUID!,
            input: PeerRouteInput):                    PeerRoute!
}

type Mutation {
  # Auth
  register(input: RegisterInput!):                     AuthPayload!
  login(input: LoginInput!):                           AuthPayload!
  refresh(refreshToken: String!):                      AuthPayload!
  logout(refreshToken: String!):                       Boolean!
  wsTicket:                                            WsTicket!

  # Ride lifecycle
  createRide(destination: DestinationInput):           Ride!
  joinRide(rideId: UUID!, inviteCode: String!):        Ride!
  leaveRide(rideId: UUID!):                            Boolean!
  endRide(rideId: UUID!):                              Ride!

  # Destination & route
  setDestination(rideId: UUID!, input: DestinationInput!): Destination!
  computeRoute(rideId: UUID!, input: ComputeRouteInput!):  Route!
}
```

## 3. Operation Notes

### 3.1 Auth

`register`, `login`, `refresh` are public. All others require a valid bearer.

```graphql
mutation Register($input: RegisterInput!) {
  register(input: $input) {
    user { id name email createdAt }
    accessToken refreshToken expiresIn
  }
}
```

Validation:
- `email`: RFC 5321 length, RFC 5322 syntax, case-insensitive uniqueness.
- `password`: 12–128 chars, ≥ 3 of {upper, lower, digit, symbol}.
- `name`: 1–40 chars, printable Unicode, NFC-normalized, trimmed.

### 3.2 Ride Lifecycle

- `createRide` — caller becomes owner. Optional `destination` may be set at creation. Returns full `Ride` with the single owner member. Generates a 6-char invite code matching `^[A-HJ-NP-Z2-9]{6}$`.
- `joinRide` — body must include `inviteCode`; `rideId` and `inviteCode` must point to the same active ride. Errors: `INVITE_INVALID`, `RIDE_FULL`, `ALREADY_MEMBER`.
- `leaveRide` — owner-leave triggers ownership transfer to earliest joiner; last-member-leave auto-ends the ride.
- `endRide` — owner-only; transitions to `ENDED`, invalidates invite code, closes WS room, fans out `ride.ended`.

### 3.3 Destination

`setDestination` is member-only. Server emits `destination.updated` over WS to all connected members and invalidates `ride_routes` cache for the ride. Errors: `VALIDATION_FAILED`, `FORBIDDEN`, `NOT_FOUND`, `CONFLICT` (ended).

### 3.4 Route to Destination — `computeRoute`

Member-only. Computes (or returns cached) route from the caller's origin to the ride's destination. Throttle: 1 / 30 s per user per ride; 6 / 10 min per ride. Server emits `route.updated` over WS to the requester only.

```graphql
mutation Compute($rideId: UUID!, $input: ComputeRouteInput!) {
  computeRoute(rideId: $rideId, input: $input) {
    distanceMeters durationSeconds encodedPolyline provider travelMode cacheHit
  }
}
```

### 3.5b Pillion pairing — `pairWithRider` / `detachFromRider`

A pillion calls `pairWithRider(rideId, riderUserId)` to declare they are physically on the rider's bike. The server validates both are active members of the ride, that neither is currently in another active pair on this ride, and INSERTs a row into `ride_pairs`.

While the pair is active:
- The pillion's WS may suppress `location.update`. If the client still publishes (e.g. for redundancy), the server discards the position and uses the rider's last-known instead.
- `location.broadcast` from the rider includes `paired_user_ids: [pillionUserId]` so receivers render the pillion icon at the rider's pin.
- `peerRoute(X, pillionUserId)` reuses the rider's last-known location server-side; no provider call is made when the rider's position is fresh.
- `peerRoute(rider, pillion)` short-circuits with `provider="self_pair"`, `distanceMeters=0`, `durationSeconds=0`, empty polyline.

`detachFromRider(rideId)` (typically the pillion taps a "detach" button) sets `detached_at = now()` on the active pair row and stamps `detached_by` with the caller. From that moment the pillion is treated as an independent entity for routing and broadcast.

```graphql
mutation Pair($rideId: UUID!, $riderUserId: UUID!) {
  pairWithRider(rideId: $rideId, riderUserId: $riderUserId) {
    id rideId riderUserId pillionUserId pairedAt
  }
}

mutation Detach($rideId: UUID!) {
  detachFromRider(rideId: $rideId)
}
```

Errors: `FORBIDDEN`, `NOT_FOUND`, `CONFLICT` (already paired, or rider not member, or pillion already in another pair), `VALIDATION_FAILED`.

### 3.5 Peer Route — `peerRoute` (NEW)

When rider A taps rider B in the UI to see "how far is B from me by route", the client issues:

```graphql
query PeerRoute($rideId: UUID!, $peerUserId: UUID!) {
  peerRoute(rideId: $rideId, peerUserId: $peerUserId) {
    distanceMeters
    durationSeconds
    encodedPolyline
    provider
    travelMode
    fromLocation { latitude longitude }
    toLocation   { latitude longitude }
    fromLocationAt
    toLocationAt
    cacheHit
  }
}
```

Behavior:

1. Server resolves caller's last-known location and `peerUserId`'s last-known location from the in-memory presence store (the WS Hub already tracks this — see ARCHITECTURE).
2. If the caller passes `input.originOverride`, that origin is used instead of the cached presence.
3. Both timestamps are checked against `PEER_ROUTE_STALE_TTL` (default 30 s). If either is stale, the resolver returns `PEER_LOCATION_STALE`. If a peer has no known location at all, returns `PEER_LOCATION_MISSING`.
4. Provider call: same `routes.Provider` as `computeRoute`, but with origin = caller's location and destination = peer's location. Distance is the routed distance, not Haversine.
5. Cache: `ride_routes` row with `route_kind = 'PEER'`, `peer_user_id = <peerUserId>`, keyed additionally by both geohash7 buckets (see DATABASE_SCHEMA §3.5).
6. Throttle:
   - 1 / 30 s per `(rideId, fromUserId, toUserId)` ordered pair.
   - 12 / 10 min per ride across all peer pairs.
7. Authorization:
   - Caller must be an active member of `rideId`.
   - `peerUserId` must be an active member of `rideId`.
   - `peerUserId ≠ caller`.

Errors: `FORBIDDEN`, `NOT_FOUND`, `PEER_LOCATION_STALE`, `PEER_LOCATION_MISSING`, `VALIDATION_FAILED`, `RATE_LIMITED`, `UPSTREAM_UNAVAILABLE`.

## 4. WebSocket Endpoint
The realtime channel is unchanged from REST→GraphQL transition.

`GET /api/v1/ws/rides/{rideId}` — see [WEBSOCKET_PROTOCOL.md](WEBSOCKET_PROTOCOL.md) for the full contract (TOON encoding, `permessage-deflate`, QoS tiers, location-priority scheduler).

`wsTicket` mutation issues the short-lived ticket usable as `?access_token=` for browser-based WS upgrades.

## 5. Health & Meta (HTTP, not GraphQL)

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/healthz` | Liveness — `{ "status": "ok" }` |
| `GET` | `/readyz` | Readiness — DB ping + provider key check |
| `GET` | `/version` | `{ "version": "...", "commit": "...", "build_time": "..." }` |
| `GET` | `/api/v1/ws/rides/{rideId}` | WebSocket upgrade |

Anonymous; no JWT required.

## 6. Rate Limits (transport layer, in front of GraphQL)

| Group | Limit |
|---|---|
| `register`, `login` | 5 / minute / IP |
| `refresh`, `wsTicket` | 30 / minute / IP |
| Ride mutations (create, join, leave, end, setDestination) | 30 / minute / user |
| `computeRoute` | 1 / 30 s per user per ride; 6 / 10 min per ride |
| `peerRoute` | 1 / 30 s per ordered (caller, peer) pair; 12 / 10 min per ride |
| Reads (`me`, `ride`, `rideMembers`, `destination`, `myRoute`) | 120 / minute / user |
| `/healthz`, `/readyz`, `/version` | unlimited |

429 responses include `Retry-After` (seconds). Per-operation throttles surface inside the GraphQL `errors` array as `RATE_LIMITED` with `extensions.retryAfterSeconds`.

## 7. Authorization Matrix

| Operation | Anonymous | Authenticated | Member | Owner |
|---|---|---|---|---|
| `register`, `login`, `refresh` | ✓ | | | |
| `me`, `wsTicket`, `logout` | | ✓ | | |
| `createRide`, `joinRide` | | ✓ | | |
| `ride`, `rideMembers`, `destination`, `myRoute` | | | ✓ | ✓ |
| `setDestination`, `computeRoute`, `peerRoute`, `leaveRide` | | | ✓ | ✓ |
| `endRide` | | | | ✓ |
| WS upgrade | | | ✓ | ✓ |

## 8. Validation Rules
- `email`: RFC 5321 length, RFC 5322 syntax, case-insensitive uniqueness.
- `password`: 12–128 chars, ≥ 3 of {upper, lower, digit, symbol}.
- `name`: 1–40 chars, NFC-normalized, trimmed.
- `inviteCode`: matches `^[A-HJ-NP-Z2-9]{6}$`.
- `Latitude`/`Longitude` scalars validate range and reject `NaN`, `±Inf`.
- `DestinationInput.name`: 1–120 chars, no control chars except space.
- `DestinationInput.placeId`: ≤ 256 chars, ASCII printable.
- All free text undergoes Unicode NFC normalization and length caps in bytes (UTF-8) before storage.
- Custom scalar parsers reject coordinates with > 6 decimal places (rounded server-side anyway).

## 9. Example: Full Round-Trip (peerRoute)

Request:
```http
POST /api/v1/graphql HTTP/1.1
Authorization: Bearer eyJhbGciOiJIUzI1...
Content-Type: application/json

{
  "query": "query Q($r: UUID!, $p: UUID!) { peerRoute(rideId: $r, peerUserId: $p) { distanceMeters durationSeconds provider cacheHit fromLocationAt toLocationAt } }",
  "variables": { "r": "01931f8a-...", "p": "01931e22-..." }
}
```

Response:
```json
{
  "data": {
    "peerRoute": {
      "distanceMeters":  812,
      "durationSeconds": 168,
      "provider":        "mapbox",
      "cacheHit":        false,
      "fromLocationAt":  "2026-05-02T10:30:01Z",
      "toLocationAt":    "2026-05-02T10:29:58Z"
    }
  }
}
```

Stale-location example:
```json
{
  "data": null,
  "errors": [{
    "message": "Peer's last known location is older than 30s.",
    "path":    ["peerRoute"],
    "extensions": {
      "code":      "PEER_LOCATION_STALE",
      "peerLastSeenSecondsAgo": 47,
      "requestId": "8e2c..."
    }
  }]
}
```
