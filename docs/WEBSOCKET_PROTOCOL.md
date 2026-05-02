# WebSocket Protocol — RideAndConnect

> Companion to API.md. All authentication and authorization rules from API §6 and SECURITY apply.
> Coding Standards: must comply with `coding_standards.json` (root).

> Independent of the GraphQL API. The realtime channel runs on a custom WebSocket protocol with TOON encoding and strict-priority QoS. There are no GraphQL subscriptions — JSON-framed subscriptions would blow the 1 kbps byte budget. Queries and mutations live on the GraphQL endpoint (see [API.md](API.md)).

## 1. Endpoint
`GET /api/v1/ws/rides/{ride_id}` — upgrades to WebSocket. WSS in production.

### 1.1 Authentication
- Preferred: HTTP `Authorization: Bearer <jwt>` header on the upgrade request.
- Fallback: `?access_token=<ws_ticket>` query param. Tickets are short-lived (5 min, single-use, `scope=ws`) issued via `POST /api/v1/auth/ws-ticket`.
- The JWT or ticket is verified before the protocol switch. **No bytes are accepted before auth succeeds.**

### 1.2 Authorization (per upgrade)
The server must verify, in order:
1. Valid token / ticket.
2. The token subject is a current active member of `{ride_id}` (`ride_members.left_at IS NULL`).
3. The ride status is not `ended`.
4. (Optional) The same `(user_id, ride_id)` pair does not have an active connection — if it does, the prior connection is closed with code `4009 SUPERSEDED` and replaced.

Any failure aborts the upgrade with HTTP 401/403; no upgrade occurs.

### 1.3 Subprotocol & Encoding
- Subprotocol negotiation via the `Sec-WebSocket-Protocol` header on upgrade. Supported values, in server preference order:
  - `toon.v1` — TOON (Token-Oriented Object Notation), default and recommended.
  - `json.v1` — UTF-8 JSON, fallback for clients without a TOON codec.
  - The server picks the first value it recognises and echoes it; if none are offered, the server defaults to `toon.v1`.
- Compression: `permessage-deflate` per RFC 7692 is offered and enabled by default. Disable per-connection by sending `?compression=off` on the upgrade (debug only).
- Max **decoded** message size: `WS_MAX_MSG_SIZE` (default 8192 bytes). Larger decoded payloads close the connection with `4011 MESSAGE_TOO_LARGE`. The compressed wire size is additionally capped at `WS_MAX_WIRE_BYTES` (default 16 KB) to prevent decompression bombs.
- The server may close with `4010 MALFORMED` if the message cannot be decoded or the envelope is invalid in the negotiated codec.

See §9 for codec details and §10 for the QoS / low-bandwidth mode that depend on this negotiation.

## 2. Message Envelope
Every message — client or server — has the same envelope shape:

```json
{
  "type": "<message-type>",
  "ride_id": "ride_123",
  "timestamp": "2026-05-02T10:30:00.123Z",
  "from_user_id": "user_1",
  "to_user_id": "user_2",
  "payload": { /* type-specific */ }
}
```

Field rules:
- `type` — required, ASCII string from §3.
- `ride_id` — required, must equal the path param of the upgrade. Server rejects any message whose `ride_id` mismatches with `4012 RIDE_MISMATCH`.
- `timestamp` — required, RFC 3339 in UTC. On broadcast messages, the **server overwrites** the timestamp authoritatively.
- `from_user_id` — required for messages whose semantics include a sender (location, signaling). Server validates it equals the connection's authenticated user — clients cannot forge sender identity. On any mismatch, the server closes the connection with `4007 SPOOFED_SENDER`.
- `to_user_id` — required for targeted messages (`voice.offer`, `voice.answer`, `voice.ice_candidate`). Server validates target is an active member.
- `payload` — required object (may be empty `{}`). Type-specific schema below.

## 3. Message Types

### 3.1 Direction & Lifecycle Summary

| Type | Direction | Notes |
|---|---|---|
| `ping` | C↔S | Heartbeat |
| `pong` | C↔S | Heartbeat reply |
| `location.update` | C→S | Rider publishes own GPS sample |
| `location.broadcast` | S→C | Server fans out a peer's location |
| `destination.set` | C→S | Optional WS path to set destination (canonical path is HTTP) |
| `destination.updated` | S→C | Server fans out destination change |
| `route.request` | C→S | Optional WS path to request route (canonical path is HTTP) |
| `route.updated` | S→C | Server returns or pushes a route to the requester |
| `rider.online` | S→C | Server reports a member connected |
| `rider.offline` | S→C | Server reports a member disconnected |
| `member.joined` | S→C | Server reports a member joined the ride |
| `member.left` | S→C | Server reports a member left the ride |
| `voice.offer` | C→S→C | Targeted SDP offer |
| `voice.answer` | C→S→C | Targeted SDP answer |
| `voice.ice_candidate` | C→S→C | Targeted ICE candidate |
| `ride.ended` | S→C | Server announces ride has ended |
| `error` | S→C | Server-emitted error with code/message |
| `network.advisory` | S→C | Server-recommended bandwidth tier and publish cadence |
| `client.advisory` | C→S | Client-reported observed link quality and requested mode |

### 3.2 Heartbeat

Server sends `ping` every `WS_PING_INTERVAL` (25 s default). Client must reply with `pong` within `WS_IDLE_TIMEOUT` (60 s default) or be closed with `4006 IDLE_TIMEOUT`.

```json
{ "type": "ping",  "ride_id": "ride_123", "timestamp": "...", "payload": { "nonce": "abc" } }
{ "type": "pong",  "ride_id": "ride_123", "timestamp": "...", "payload": { "nonce": "abc" } }
```

Clients may also send `ping` proactively; the server will respond with `pong`.

### 3.3 `location.update` (C→S)

```json
{
  "type": "location.update",
  "ride_id": "ride_123",
  "from_user_id": "user_1",
  "timestamp": "2026-05-02T10:30:00Z",
  "payload": {
    "latitude": 12.9716,
    "longitude": 77.5946,
    "speed_kmph": 42.5,
    "heading": 87.2,
    "accuracy_meters": 6.4,
    "battery_percent": 74
  }
}
```

Validation:
- `latitude`, `longitude` numeric within bounds, no `NaN`/`Inf`.
- `speed_kmph` ∈ [0, 120]; values > 120 are rejected (clearly non-bike).
- `heading` ∈ [0, 360).
- `accuracy_meters` ∈ [0, 1000]; samples with `accuracy_meters > 100` may be dropped silently.
- `battery_percent` optional; if present, ∈ [0, 100].

Throttle: ≤ 2 Hz per connection. Excess messages return `error` (`code=RATE_LIMITED`) and may close the connection on sustained violation (`4004 RATE_LIMITED`).

### 3.4 `location.broadcast` (S→C)
Server forwards each accepted `location.update` to every other connected member of the ride, **never to the original sender** (clients echo locally).

```json
{
  "type": "location.broadcast",
  "ride_id": "ride_123",
  "from_user_id": "user_1",
  "timestamp": "2026-05-02T10:30:00Z",
  "payload": {
    "latitude": 12.9716,
    "longitude": 77.5946,
    "speed_kmph": 42.5,
    "heading": 87.2,
    "accuracy_meters": 6.4,
    "battery_percent": 74
  }
}
```

`timestamp` on the broadcast is the server's receive time (authoritative).

### 3.5 `destination.set` (C→S)
Optional WS path equivalent to `POST /api/v1/rides/{ride_id}/destination`. Server validates ownership/membership and emits `destination.updated`.

```json
{
  "type": "destination.set",
  "ride_id": "ride_123",
  "from_user_id": "user_1",
  "timestamp": "2026-05-02T10:31:00Z",
  "payload": {
    "name": "Nandi Hills",
    "place_id": "optional_provider_place_id",
    "latitude": 13.3702,
    "longitude": 77.6835
  }
}
```

### 3.6 `destination.updated` (S→C)
Fan-out to all room members.

```json
{
  "type": "destination.updated",
  "ride_id": "ride_123",
  "timestamp": "2026-05-02T10:31:00Z",
  "payload": {
    "destination": {
      "name": "Nandi Hills",
      "place_id": "optional_provider_place_id",
      "latitude": 13.3702,
      "longitude": 77.6835
    },
    "set_by_user_id": "user_1"
  }
}
```

### 3.7 `route.request` (C→S)
Optional WS path equivalent to `POST /api/v1/rides/{ride_id}/route`. Same validation and throttling.

```json
{
  "type": "route.request",
  "ride_id": "ride_123",
  "from_user_id": "user_1",
  "timestamp": "2026-05-02T10:32:00Z",
  "payload": {
    "origin": { "latitude": 12.9716, "longitude": 77.5946 },
    "travel_mode": "TWO_WHEELER"
  }
}
```

### 3.8 `route.updated` (S→C, requester only)

```json
{
  "type": "route.updated",
  "ride_id": "ride_123",
  "to_user_id": "user_1",
  "timestamp": "2026-05-02T10:32:01Z",
  "payload": {
    "user_id": "user_1",
    "origin": { "latitude": 12.9716, "longitude": 77.5946 },
    "destination": {
      "name": "Nandi Hills",
      "latitude": 13.3702,
      "longitude": 77.6835
    },
    "distance_meters": 64200,
    "duration_seconds": 5400,
    "encoded_polyline": "encoded_polyline_here",
    "provider": "google_routes",
    "travel_mode": "TWO_WHEELER",
    "cache_hit": false
  }
}
```

### 3.9 `rider.online` / `rider.offline` (S→C)
Emitted to room members when a peer's WS connection opens or closes.

```json
{
  "type": "rider.online",
  "ride_id": "ride_123",
  "timestamp": "...",
  "payload": { "user_id": "user_2", "name": "Bob" }
}
```

### 3.10 `member.joined` / `member.left` (S→C)
Emitted when a member is added (via `POST /rides/{id}/join`) or removed (via leave/end), regardless of WS connection state.

```json
{
  "type": "member.joined",
  "ride_id": "ride_123",
  "timestamp": "...",
  "payload": {
    "user_id": "user_2",
    "name": "Bob",
    "role": "member",
    "joined_at": "..."
  }
}
```

### 3.11 WebRTC Signaling (`voice.offer`, `voice.answer`, `voice.ice_candidate`)
Server validates schema and authorization, then forwards verbatim to `to_user_id` only. Server never logs `sdp` or `candidate` content.

`voice.offer` (C→S→C):
```json
{
  "type": "voice.offer",
  "ride_id": "ride_123",
  "from_user_id": "user_1",
  "to_user_id": "user_2",
  "timestamp": "2026-05-02T10:33:00Z",
  "payload": {
    "sdp": "v=0\r\no=- ...",
    "session_id": "01931f..."
  }
}
```

`voice.answer` (C→S→C):
```json
{
  "type": "voice.answer",
  "ride_id": "ride_123",
  "from_user_id": "user_2",
  "to_user_id": "user_1",
  "timestamp": "...",
  "payload": {
    "sdp": "v=0\r\no=- ...",
    "session_id": "01931f..."
  }
}
```

`voice.ice_candidate` (C→S→C):
```json
{
  "type": "voice.ice_candidate",
  "ride_id": "ride_123",
  "from_user_id": "user_1",
  "to_user_id": "user_2",
  "timestamp": "...",
  "payload": {
    "candidate": "candidate:842163049 1 udp 1677729535 ...",
    "sdp_mid": "0",
    "sdp_m_line_index": 0,
    "session_id": "01931f..."
  }
}
```

Server-side validation:
- `from_user_id` equals connection's authenticated user.
- `to_user_id` ≠ `from_user_id` and is an active ride member.
- `payload.sdp` ≤ 8 KB; `payload.candidate` ≤ 1 KB.
- `session_id` is a UUIDv7-ish opaque string ≤ 64 chars.
- The server does not parse, mutate, or interpret SDP/candidate contents beyond size and presence checks.

### 3.12 `ride.ended` (S→C)
Final message before the server closes all connections of a ride.

```json
{
  "type": "ride.ended",
  "ride_id": "ride_123",
  "timestamp": "...",
  "payload": { "ended_by_user_id": "user_1", "reason": "owner_ended" }
}
```

After this message the server closes the connection with code `4001 RIDE_ENDED`.

### 3.13 `error` (S→C)
The server emits an `error` for client-fixable issues without closing the connection.

```json
{
  "type": "error",
  "ride_id": "ride_123",
  "timestamp": "...",
  "payload": {
    "code": "VALIDATION_FAILED",
    "message": "latitude out of range",
    "details": { "field": "payload.latitude" }
  }
}
```

For unrecoverable issues, the server closes the connection with one of the codes in §4.

### 3.14 `network.advisory` (S→C)
Sent on connect, on tier change, and at most once every 5 s thereafter. Recommends a bandwidth tier and the server-side intent for client behavior. The client SHOULD honor it; the server enforces hard caps independently.

```json
{
  "type": "network.advisory",
  "ride_id": "ride_123",
  "timestamp": "2026-05-02T10:30:00Z",
  "payload": {
    "tier": "low",
    "rtt_ms_p95": 850,
    "recommended_location_hz": 0.5,
    "recommended_voice_bitrate_kbps": 8,
    "voice_signaling_allowed": true,
    "reason": "high_latency"
  }
}
```

Tier semantics (see §10.1):

| `tier` | Approximate link | Server effect |
|---|---|---|
| `high` | ≥ 60 kbps | Default cadence, voice ICE relay allowed |
| `medium` | ~ 30 kbps | Location 1 Hz, voice signaling allowed |
| `low` | ~ 10 kbps | Location ≤ 0.5 Hz, voice signaling deprioritized |
| `critical` | < 10 kbps | Location-only mode; voice signaling buffered or shed |

`reason` ∈ {`startup`, `client_reported`, `server_probe_high_rtt`, `slow_consumer_pressure`, `bandwidth_estimate_drop`, `recovered`}.

### 3.15 `client.advisory` (C→S)
Optional. Lets the client tell the server what it is observing locally and what mode it requests. The server may use it to adjust cadence or downgrade fan-out, but never grants more than the hard caps allow.

```json
{
  "type": "client.advisory",
  "ride_id": "ride_123",
  "from_user_id": "user_1",
  "timestamp": "2026-05-02T10:30:00Z",
  "payload": {
    "observed_kbps_down": 12,
    "observed_kbps_up": 8,
    "battery_percent": 31,
    "requested_tier": "low",
    "voice_enabled": false
  }
}
```

Validation:
- `observed_kbps_*` ∈ [0, 10000]; `requested_tier` ∈ {`high`, `medium`, `low`, `critical`}.
- The server may downgrade further (e.g. client requests `medium` but RTT measurements force `low`).

## 4. Close Codes

| Code | Name | Meaning |
|---|---|---|
| 1000 | NORMAL | Clean close |
| 1008 | POLICY_VIOLATION | Generic policy violation |
| 4001 | RIDE_ENDED | Ride was ended |
| 4002 | UNAUTHORIZED | Auth invalid/expired |
| 4003 | FORBIDDEN | Not a member of the ride |
| 4004 | RATE_LIMITED | Sustained throttle violation |
| 4005 | NOT_FOUND | Ride no longer exists |
| 4006 | IDLE_TIMEOUT | No pong within timeout |
| 4007 | SPOOFED_SENDER | `from_user_id` mismatch |
| 4008 | SLOW_CONSUMER | Send queue full |
| 4009 | SUPERSEDED | A new connection from same user superseded this one |
| 4010 | MALFORMED | Bad envelope or invalid JSON |
| 4011 | MESSAGE_TOO_LARGE | Frame exceeded `WS_MAX_MSG_SIZE` |
| 4012 | RIDE_MISMATCH | `ride_id` in body ≠ path |
| 4013 | ROOM_FULL | Server-side room cap exceeded |
| 4014 | UNSUPPORTED_TYPE | Unknown `type` value |

## 5. Server-Side Throttling
- `location.update`: ≤ 2 Hz per connection (token bucket size 4, refill 2/s). Excess emits `error` then `4004` if sustained.
- `voice.*`: ≤ 30 messages / 10 s per connection.
- All other client messages: ≤ 5 / s per connection.

## 6. Presence & Online/Offline
- A user is `online` for the ride while they hold an active WS connection. The Hub tracks this in-memory.
- On connection open after passing auth/membership: server emits `rider.online` to other members.
- On connection close (any reason): server emits `rider.offline` to other members.
- Presence is not persisted.

## 7. Ordering & Delivery Guarantees
- Per-connection ordering is preserved on the read pump.
- Fan-out is best-effort, non-blocking: a slow consumer is dropped (`4008`) rather than blocking the room.
- Messages are not retransmitted; clients are responsible for resilience (e.g. resending location on reconnect).

## 8. Versioning
- Protocol version is implied by API version (`/v1`).
- Adding new `type` values is non-breaking.
- Removing fields, renaming `type`, or changing semantics requires a new API version.

## 9. Encoding & Compression

The transport is optimized for cellular bike riders who do not always have 4G/5G — supported sustained throughput is **1 kbps to 60 kbps**. At the 1 kbps floor (~125 B/s after framing) the system runs as a location-ticker plus a sparse control plane; voice signaling is shed entirely.

> **Caveat at the 1 kbps floor.** TLS handshake and WebSocket upgrade require several KB of one-shot exchange that may take seconds to complete on a 1 kbps link. Steady-state operation fits the budget; **reconnection is expensive at this tier** and clients should avoid voluntary reconnects until link quality improves (see §10.6).

### 9.1 Codec — TOON (default) or JSON (fallback)

The default codec is **TOON (Token-Oriented Object Notation)**, chosen for its compact tabular array form, schema-less header, and 30–50% byte reduction over JSON for the location/broadcast hot path. JSON is supported as a fallback for clients without a TOON library.

- Negotiated via `Sec-WebSocket-Protocol`: `toon.v1` preferred, `json.v1` accepted.
- Inside a connection, **only one codec is in use**. The server replies in the negotiated codec for every server-emitted message.
- HTTP REST endpoints accept both via `Content-Type` and respond per `Accept` (see API §1.5):
  - `application/json` — JSON
  - `application/toon` — TOON

#### 9.1.1 Sample location.broadcast — JSON vs TOON

JSON (≈ 220 bytes, pre-compression):
```json
{"type":"location.broadcast","ride_id":"ride_123","from_user_id":"user_1","timestamp":"2026-05-02T10:30:00Z","payload":{"latitude":12.9716,"longitude":77.5946,"speed_kmph":42.5,"heading":87.2,"accuracy_meters":6.4,"battery_percent":74}}
```

TOON (≈ 130 bytes, pre-compression — illustrative tabular form):
```
type: location.broadcast
ride_id: ride_123
from_user_id: user_1
timestamp: 2026-05-02T10:30:00Z
payload{lat,lng,spd,hdg,acc,bat}: 12.9716,77.5946,42.5,87.2,6.4,74
```

Field-name shortening (`latitude → lat`, `longitude → lng`, `speed_kmph → spd`, `heading → hdg`, `accuracy_meters → acc`, `battery_percent → bat`) applies **only inside the WS hot-path payload header for `location.update` / `location.broadcast`**. REST request/response bodies retain full field names per API.md.

> Note: the precise TOON syntax follows the TOON spec; the example above is representative. The implementation phase pins the exact library and version (see IMPLEMENTATION_PLAN risk). If TOON's Go ecosystem proves immature, the team falls back to **CBOR** (`application/cbor`, subprotocol `cbor.v1`) without changing the envelope or message types — the codec is a swap-out concern.

### 9.2 Compression

- **WebSocket** — `permessage-deflate` is enabled per RFC 7692 with:
  - `server_max_window_bits=15`, `client_max_window_bits=15`
  - `server_no_context_takeover=false` (keep dictionary across frames inside a connection — biggest win for repetitive `location.update`)
  - `client_no_context_takeover=false`
- **HTTP** — accept and emit `gzip` and (where supported) `br` per `Accept-Encoding`. Auth endpoints (`/auth/*`) are served with `Content-Encoding: identity` to defuse any CRIME/BREACH-class concern.
- **Static dictionary (optional, v1.1)** — A small precomputed Brotli dictionary covering the location-update keyword set (`location.broadcast`, `latitude`, `longitude`, etc., and the ride/user UUIDs once captured per connection) further shrinks the hot path. Negotiated via `?dict=ride.v1` on upgrade. Default off in v1; tracked as an enhancement.

Decompressed-size cap: `WS_MAX_MSG_SIZE` (default 8192 bytes). Wire-size cap: `WS_MAX_WIRE_BYTES` (default 16 KB). Either breach closes with `4011 MESSAGE_TOO_LARGE`.

### 9.3 Byte budget across the 1–60 kbps range

After framing overhead, available payload bandwidth is roughly:

| Link | Payload budget |
|---|---|
| 1 kbps | ~125 B/s |
| 3 kbps | ~370 B/s |
| 10 kbps | ~1,250 B/s |
| 30 kbps | ~3,750 B/s |
| 60 kbps | ~7,500 B/s |

Steady-state per-tier WS consumption (3-rider ride, TOON + permessage-deflate):

| Tier | Loc out | Loc in (×2) | Ping | Voice signaling | Advisories | **Subtotal** | Fits in |
|---|---|---|---|---|---|---|---|
| `high` | 2 Hz × 70 B = 140 B/s | 4 Hz × 70 B = 280 B/s | 0.04 Hz × 30 B = 1 B/s | up to 30 / 10 s × ~120 B = 360 B/s | ~5 B/s | **~786 B/s (≈ 6.3 kbps)** | 30 kbps + |
| `medium` | 1 Hz × 70 B = 70 B/s | 2 Hz × 70 B = 140 B/s | 0.04 Hz × 30 B = 1 B/s | bursty signaling, capped | ~5 B/s | **~250 B/s (≈ 2.0 kbps)** | 10 kbps + |
| `low` | 0.5 Hz × 70 B = 35 B/s | 1 Hz × 70 B = 70 B/s | 0.04 Hz × 30 B = 1 B/s | deferred / minimal | ~5 B/s | **~115 B/s (≈ 0.9 kbps)** | 3 kbps + |
| `critical` | 0.2 Hz × 70 B = 14 B/s | 0.4 Hz × 70 B = 28 B/s | 1/60 s × 30 B = 0.5 B/s | **shed** (0 B/s) | ~5 B/s | **~50 B/s (≈ 0.4 kbps)** | 1 kbps |

Voice (Opus, peer-to-peer WebRTC) is **not on this budget** — it traverses the peer connection between clients, not the backend. At `critical` the backend recommends pausing voice entirely; at `low` it recommends Opus ≤ 8 kbps; at `medium`/`high` Opus 12–16 kbps.

## 10. QoS, Priority & Low-Bandwidth Mode

### 10.1 Bandwidth tiers

The server tracks an effective tier per connection. Tier inputs (in order of weight):

1. Server-measured ping RTT (EWMA over the last 8 ping/pong round-trips).
2. Per-connection deflate compression ratio and payload backlog.
3. The most recent `client.advisory.requested_tier` (clamped by 1 + 2).

Tier thresholds (defaults, configurable). Estimated link kbps comes from `client.advisory.observed_kbps_*` clamped by RTT-derived bounds:

| Tier | Estimated link | RTT p95 | Server behavior |
|---|---|---|---|
| `high` | ≥ 30 kbps | < 250 ms | Default rates from §5; voice signaling normal priority |
| `medium` | 10–30 kbps | 250–600 ms | Cap inbound `location.update` to 1 Hz; voice signaling normal priority |
| `low` | 3–10 kbps | 600–1500 ms | Cap location to 0.5 Hz; defer non-targeted voice signaling by up to 1 s; recommend Opus ≤ 8 kbps |
| `critical` | < 3 kbps (incl. ~1 kbps floor) | ≥ 1500 ms OR send queue ≥ 75% full | Location-only mode at 0.2 Hz; **shed `voice.*`**; emit `network.advisory{voice_signaling_allowed:false}`; reduce ping interval to 60 s |

A tier change is persisted on the connection and announced via `network.advisory` (rate-limited to 1 / 5 s — reduced to 1 / 30 s in `critical`).

### 10.2 Strict-priority send scheduler

Each connection has **three priority queues** drained by the write pump in strict order:

| Priority | Queue | Cap | Drop policy |
|---|---|---|---|
| **P0 — Location & lifecycle** | `location.broadcast`, `location.update` ack-ish replies, `ride.ended`, `member.joined/left`, `network.advisory`, `error` | 32 | Coalesce: keep **only the latest** `location.broadcast` per `from_user_id` when queue depth > 8 |
| **P1 — Control** | `destination.updated`, `route.updated`, `rider.online/offline`, `ping`, `pong` | 16 | Drop oldest |
| **P2 — Voice signaling** | `voice.offer`, `voice.answer`, `voice.ice_candidate` | 16 | Drop oldest; in `critical` tier, refuse new with `error{code=DEGRADED}` instead of enqueue |

The write pump emits up to N messages per scheduler tick, where N decays by tier (`high=8`, `medium=4`, `low=2`, `critical=1`).

### 10.3 Inbound throttling per tier

Inbound caps for client→server messages override §5 defaults under degraded tiers:

| Type | high (≥30 kbps) | medium (10–30 kbps) | low (3–10 kbps) | critical (<3 kbps) |
|---|---|---|---|---|
| `location.update` | 2 Hz | 1 Hz | 0.5 Hz | 0.2 Hz |
| `voice.*` | 30 / 10 s | 30 / 10 s | 10 / 10 s | 0 (rejected) |
| `route.request` | 1 / 30 s | 1 / 30 s | 1 / 60 s | 1 / 120 s |
| `client.advisory` | 1 / 5 s | 1 / 5 s | 1 / 5 s | 1 / 30 s |
| `ping` (server-initiated) | 25 s interval | 25 s interval | 30 s interval | 60 s interval |

Excess emits `error{code=RATE_LIMITED}` and may close `4004 RATE_LIMITED` on sustained violation, same as §5.

### 10.4 Voice prioritization rule

WebRTC audio runs peer-to-peer; the backend does not relay it. The backend's lever is signaling. Rules:

1. **Location is never starved by voice.** P0 always wins over P2 in the scheduler.
2. **In `critical` tier, voice signaling is shed**, not buffered indefinitely. Clients receive `network.advisory{voice_signaling_allowed:false}` and must pause `voice.*` traffic until a `network.advisory` reverses it.
3. **Recommended Opus bitrate** is communicated via `network.advisory.recommended_voice_bitrate_kbps` (e.g. 8 at `low`, 12 at `medium`, 16 at `high`). Clients adjust their RTCPeerConnection sender parameters; the backend does not enforce.
4. The backend MAY emit a synthetic `voice.offer`-cancellation hint via `error{code=DEGRADED, details:{voice:"pause"}}` to a client when the link is critical and a signaling burst is detected.

### 10.5 Heartbeat as bandwidth probe

`ping` / `pong` carry a `nonce` and a `payload.send_ts`. The server records (recv_ts − send_ts) round-trip time per ping and feeds the EWMA used by §10.1. No additional probe traffic is added — heartbeat doubles as the bandwidth signal.

```json
{ "type": "ping", "ride_id": "ride_123", "timestamp": "...", "payload": { "nonce": "abc", "send_ts": 1714646400000 } }
```

### 10.6 Reconnect strategy (client guidance)

Documented for client implementers, not enforced by backend:

- On WS close at `low` or `critical` tier: backoff with jitter (250 ms → 5 s, ×1.5).
- **At ~1 kbps the TLS+WS upgrade alone may take several seconds.** Avoid voluntary reconnect at this tier — only reconnect when the underlying link recovers (client-side connectivity signal) or when an explicit close is received.
- On reconnect, send `client.advisory` immediately with last observed kbps so the server starts in the right tier rather than defaulting to `high`.
