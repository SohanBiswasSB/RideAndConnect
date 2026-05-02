# Postman Collection Plan — RideAndConnect (GraphQL)

> Companion to API.md and WEBSOCKET_PROTOCOL.md. The collection is a planning artifact here; the JSON is generated during implementation Phase 11 and saved under `postman/`.
> The API surface is **GraphQL** at `POST /api/v1/graphql`; Postman supports GraphQL request bodies with autocomplete from the SDL or a fetched schema. The realtime channel is the custom WebSocket described in WEBSOCKET_PROTOCOL.md.
> Coding Standards: `coding_standards.json` governs implementation; this doc governs the collection layout and test scripts.

## 1. Files
- `postman/RideAndConnect.postman_collection.json`
- `postman/RideAndConnect.postman_environment.local.json`
- `postman/RideAndConnect.postman_environment.staging.json`

## 2. Environment Variables

| Variable | Example (local) | Notes |
|---|---|---|
| `base_url` | `http://localhost:8080` | dev only — staging/prod use HTTPS |
| `ws_base_url` | `ws://localhost:8080` | dev only — staging/prod use WSS |
| `access_token` | (set by login script) | bearer token |
| `refresh_token` | (set by login script) | opaque |
| `ws_ticket` | (set by ws-ticket script) | optional |
| `user_id` | (set by login script) | uuid |
| `ride_id` | (set by create-ride script) | uuid |
| `invite_code` | (set by create-ride script) | 6-char string |
| `peer_user_id` | (set by join-ride script for second runner) | uuid |

The collection has Postman variable references, never literals, for any value that is environment-specific.

## 3. Folder Structure

All GraphQL requests target the same URL `{{base_url}}/api/v1/graphql` and differ only by `query`/`variables`. Postman renders these under each folder.

```
RideAndConnect (collection)
├── 00 Smoke (HTTP, not GraphQL)
│   ├── GET /healthz
│   ├── GET /readyz
│   └── GET /version
│
├── 01 Auth (GraphQL)
│   ├── mutation register
│   ├── mutation login
│   ├── mutation refresh
│   ├── mutation logout
│   ├── mutation wsTicket
│   └── query    me
│
├── 02 Rides (GraphQL)
│   ├── mutation createRide
│   ├── mutation joinRide
│   ├── query    ride
│   ├── query    rideMembers
│   ├── mutation leaveRide
│   └── mutation endRide
│
├── 03 Destination (GraphQL)
│   ├── mutation setDestination
│   └── query    destination
│
├── 04 Route — to destination (GraphQL)
│   ├── mutation computeRoute
│   └── query    myRoute
│
├── 05 Peer Route (GraphQL)
│   ├── query    peerRoute (happy path — both fresh)
│   ├── query    peerRoute with originOverride
│   ├── query    peerRoute (PEER_LOCATION_STALE)
│   └── query    peerRoute (PEER_LOCATION_MISSING)
│
├── 06 Realtime (WS)
│   ├── WS Connect (Authorization header)
│   ├── WS Connect (ws-ticket query param)
│   ├── Send: location.update
│   ├── Send: destination.set
│   ├── Send: route.request
│   ├── Send: voice.offer / answer / ice_candidate
│   └── Send: ping (expect pong)
│
├── 07 Negative Cases (GraphQL)
│   ├── UNAUTHENTICATED — no token
│   ├── FORBIDDEN — non-member querying ride
│   ├── RIDE_FULL — 4th join
│   ├── ALREADY_MEMBER — duplicate join
│   ├── INVITE_INVALID — ended ride
│   ├── NOT_FOUND — destination unset, computeRoute called
│   ├── RATE_LIMITED — repeated computeRoute
│   ├── PEER_LOCATION_STALE — peer hasn't broadcast in > 30 s
│   └── WS 4007 SPOOFED_SENDER
│
└── 99 Cleanup
    └── mutation endRide
```

## 4. Auth Flow Scripts

### 4.1 Pre-request script (collection level)
- If `access_token` is set: attach `Authorization: Bearer {{access_token}}`.
- Generate `X-Request-Id` per request (UUIDv4 for visibility) and assert echo in response.

### 4.2 Login test script (saves tokens)
```js
const r = pm.response.json();
pm.environment.set("access_token",  r.access_token);
pm.environment.set("refresh_token", r.refresh_token);
pm.environment.set("user_id",       r.user.id);
pm.expect(pm.response.code).to.equal(200);
pm.expect(r.access_token).to.be.a("string");
```

### 4.3 Refresh test script
```js
const r = pm.response.json();
pm.environment.set("access_token",  r.access_token);
pm.environment.set("refresh_token", r.refresh_token);
pm.expect(pm.response.code).to.equal(200);
```

### 4.4 ws-ticket test script
```js
const r = pm.response.json();
pm.environment.set("ws_ticket", r.ticket);
pm.expect(r.expires_in).to.equal(300);
```

## 5. Request Examples

Each request includes:
- A `mode: "graphql"` body with `query` and `variables` matching API.md §2.
- A short description with success/error codes (the `extensions.code` from API §1.3).
- A test script asserting:
  - HTTP status is 200 (GraphQL semantic errors are inside the body).
  - `data` is non-null OR `errors[0].extensions.code` matches the expected code.
  - Side effects (token capture, ride_id capture).

### 5.1 Create Ride (mutation)
```graphql
mutation CreateRide($dest: DestinationInput) {
  createRide(destination: $dest) {
    id ownerUserId inviteCode status
    destination { name latitude longitude }
    members { userId name role joinedAt }
  }
}
```
Variables:
```json
{
  "dest": {
    "name": "Nandi Hills",
    "placeId": "ChIJxxxxxxxxxxxx",
    "latitude": 13.3702,
    "longitude": 77.6835
  }
}
```
Test script:
```js
const r = pm.response.json();
pm.expect(pm.response.code).to.equal(200);
pm.expect(r.errors).to.be.undefined;
pm.environment.set("ride_id",     r.data.createRide.id);
pm.environment.set("invite_code", r.data.createRide.inviteCode);
pm.expect(r.data.createRide.members.length).to.equal(1);
pm.expect(r.data.createRide.members[0].role).to.equal("OWNER");
```

### 5.2 Join Ride (mutation)
```graphql
mutation JoinRide($rideId: UUID!, $code: String!) {
  joinRide(rideId: $rideId, inviteCode: $code) {
    id status members { userId role }
  }
}
```
Variables: `{ "rideId": "{{ride_id}}", "code": "{{invite_code}}" }`

### 5.3 Set Destination (mutation)
```graphql
mutation SetDest($rideId: UUID!, $input: DestinationInput!) {
  setDestination(rideId: $rideId, input: $input) {
    name placeId latitude longitude updatedAt
  }
}
```

### 5.4 Compute Route (mutation)
```graphql
mutation Compute($rideId: UUID!, $input: ComputeRouteInput!) {
  computeRoute(rideId: $rideId, input: $input) {
    distanceMeters durationSeconds encodedPolyline provider travelMode cacheHit
  }
}
```
Variables:
```json
{
  "rideId": "{{ride_id}}",
  "input":  { "origin": { "latitude": 12.9716, "longitude": 77.5946 }, "travelMode": "TWO_WHEELER" }
}
```

### 5.5 Peer Route (query — NEW)
```graphql
query PeerRoute($rideId: UUID!, $peerUserId: UUID!, $input: PeerRouteInput) {
  peerRoute(rideId: $rideId, peerUserId: $peerUserId, input: $input) {
    fromUserId toUserId
    distanceMeters durationSeconds provider cacheHit
    fromLocation { latitude longitude }
    toLocation   { latitude longitude }
    fromLocationAt toLocationAt
  }
}
```
Variables (no override — use server's last-known origin):
```json
{ "rideId": "{{ride_id}}", "peerUserId": "{{peer_user_id}}" }
```
Variables (with `originOverride` for a fresher caller position):
```json
{
  "rideId": "{{ride_id}}",
  "peerUserId": "{{peer_user_id}}",
  "input": { "originOverride": { "latitude": 12.9722, "longitude": 77.5949 }, "travelMode": "TWO_WHEELER" }
}
```
Test:
```js
const r = pm.response.json();
pm.expect(pm.response.code).to.equal(200);
if (r.errors) {
  pm.expect(["PEER_LOCATION_STALE", "PEER_LOCATION_MISSING"]).to.include(r.errors[0].extensions.code);
} else {
  pm.expect(r.data.peerRoute.distanceMeters).to.be.a("number");
  pm.expect(r.data.peerRoute.durationSeconds).to.be.a("number");
}
```

### 5.6 End Ride (mutation)
```graphql
mutation EndRide($rideId: UUID!) {
  endRide(rideId: $rideId) { id status endedAt }
}
```

## 6. WebSocket Requests

Postman supports WS as a separate request type. Each WS request lists the messages a tester would send/expect.

### 6.1 Connect (header auth)
- URL: `{{ws_base_url}}/api/v1/ws/rides/{{ride_id}}`
- Header: `Authorization: Bearer {{access_token}}`
- On connect, send `ping`:
```json
{ "type": "ping", "ride_id": "{{ride_id}}", "timestamp": "{{now_iso}}", "payload": { "nonce": "abc" } }
```
- Expect server `pong` with same `nonce`.

### 6.2 Connect (ticket query)
- URL: `{{ws_base_url}}/api/v1/ws/rides/{{ride_id}}?access_token={{ws_ticket}}`
- (Use after calling `POST /api/v1/auth/ws-ticket`.)

### 6.3 Send `location.update`
```json
{
  "type": "location.update",
  "ride_id": "{{ride_id}}",
  "from_user_id": "{{user_id}}",
  "timestamp": "{{now_iso}}",
  "payload": {
    "latitude": 12.9716,
    "longitude": 77.5946,
    "speed_kmph": 22.5,
    "heading": 90.0,
    "accuracy_meters": 5.0,
    "battery_percent": 80
  }
}
```
- A second client (logged in as a different user, also a ride member) should observe a `location.broadcast` mirroring the payload.

### 6.4 Send signaling triplet
- `voice.offer` from user A to user B.
- `voice.answer` from user B to user A.
- `voice.ice_candidate` exchanged both directions.
- Validate each is delivered only to `to_user_id` and not echoed back to sender.

### 6.5 Force close cases
- Send a message with mismatched `from_user_id` → expect close `4007 SPOOFED_SENDER`.
- Send a message with mismatched `ride_id` → expect close `4012 RIDE_MISMATCH`.
- Send oversized message → expect close `4011 MESSAGE_TOO_LARGE`.

## 7. Negative Test Cases (folder `06 Negative Cases`)

Each request asserts the specific HTTP status and `error.code` listed in API §1.2:

| Test | Setup | Expect |
|---|---|---|
| Missing token | Strip `Authorization` | 401 `UNAUTHENTICATED` |
| Non-member ride read | Use `access_token` of a non-member | 403 `FORBIDDEN` |
| RIDE_FULL | 3 members + a 4th join attempt | 409 `RIDE_FULL` |
| ALREADY_MEMBER | Same user joins twice | 409 `ALREADY_MEMBER` |
| INVITE_INVALID | Use invite code from an ended ride | 410 `INVITE_INVALID` |
| Route without destination | Create ride, do not set destination, request route | 422 or 404 with `NOT_FOUND` |
| Route throttle | 2 calls within 30 s by same user | 429 `RATE_LIMITED` with `Retry-After` |

## 8. Collection-Level Tests

A single Newman-friendly collection-level test script asserts the same redaction baseline as SECURITY §8.2: response bodies do not contain forbidden keys when not expected (e.g. token leakage in non-auth endpoints).

```js
const forbidden = ["password", "password_hash", "refresh_token_hash"];
const body = pm.response.text();
forbidden.forEach(k => pm.expect(body.toLowerCase()).to.not.include(k.toLowerCase()));
```

## 9. Running with Newman (CI smoke)

```sh
conda activate RideAndConnect
docker compose up -d postgres
make migrate-up
make run &
sleep 2
newman run postman/RideAndConnect.postman_collection.json \
  -e postman/RideAndConnect.postman_environment.local.json \
  --folder "00 Smoke" --folder "01 Auth" --folder "02 Rides" \
  --bail --color on
```

The CI smoke folder (00 + 01 + 02) is gating; the full negative folder runs nightly.

## 10. Generation Plan

The Postman collection JSON is hand-written for the most part during Phase 11, with the following rules:
- One request per endpoint, named `<METHOD> <PATH>`.
- Every request has a `description` linking to the relevant `docs/API.md` section.
- Every request has tests asserting status code and schema.
- WebSocket requests live alongside REST in `05 Realtime (WS)`.
- The collection is committed to `postman/` and never edited in the Postman cloud — desktop export only, formatted with stable JSON ordering.

## 11. Deliverables Checklist
- [ ] `postman/RideAndConnect.postman_collection.json` covers every endpoint and every WS message type.
- [ ] `postman/RideAndConnect.postman_environment.local.json` and `staging` variants.
- [ ] Collection passes against a fresh local instance via Newman.
- [ ] Negative-case folder covers each error code in API §1.2 at least once.
- [ ] WS folder covers happy path + each close code listed in WEBSOCKET_PROTOCOL §4 (where reproducible).
