# Product Requirements Document — RideAndConnect Backend

> Status: Draft v1 · Owner: Backend · Target stack: Go 1.26.2 (conda env `RideAndConnect`), PostgreSQL, REST + WebSocket
> Companion docs: ARCHITECTURE, API, WEBSOCKET_PROTOCOL, DATABASE_SCHEMA, ROUTE_PROVIDER, SECURITY, IMPLEMENTATION_PLAN, POSTMAN_COLLECTION_PLAN
> Coding Standards: see `coding_standards.json` (root). All code generated for this product MUST comply.

## 1. Overview
RideAndConnect is a private bike-ride companion backend for closed groups of **exactly 3 riders**. The backend powers a single shared "ride" session in which all members:

- See each other's live GPS position on the client map (client renders; server only broadcasts).
- Follow a backend-computed route from each rider's current origin to a single shared destination.
- Hold a low-latency voice call via WebRTC, with the backend acting only as a **signaling relay** (no media).

The backend exposes REST APIs and a WebSocket service. It does not render UI, render maps, or relay audio.

## 2. Problem Statement
Group bike rides routinely fragment at intersections, traffic, or speed differences. Generic location-sharing apps lack a tight 3-person private channel, do not bind a ride to a shared destination, and don't combine real-time voice. RideAndConnect provides a focused private session for a closed group of three riders for the duration of a single ride.

## 3. Goals
- **G1** Authenticated users can create a ride and invite up to 2 others via an invite code.
- **G2** Hard cap: **exactly 3 active members per ride** (creator + 2 joiners).
- **G3** Members see peers' live GPS within ≤ 2 s end-to-end of a publish.
- **G4** Backend computes a bike-friendly route per rider to the shared destination using a server-side route provider.
- **G5** Backend relays WebRTC signaling so members can hold a voice call.
- **G6** Membership and authorization enforced on every REST and WebSocket message.
- **G7** Route provider API key is never sent to clients; raw GPS coordinates and polylines are never written to production logs.

## 4. Non-Goals
- Public discovery, friend graph, social feed, persistent chat history.
- Rides with ≠ 3 members.
- Server-generated turn-by-turn voice navigation.
- Recording, transcribing, or relaying voice audio.
- Long-term GPS history or analytics (MVP).
- Frontend (mobile or web), map screens, client-side WebRTC code.

## 5. Personas
- **Owner** — creates the ride, picks the destination, shares the invite code, may end the ride.
- **Member** — receives invite code out-of-band, joins, shares location, talks, may leave.

All personas are authenticated. No anonymous or guest access.

## 6. User Stories
1. As a user I can register with email (or phone) + password and receive access/refresh tokens.
2. As a user I can log in to receive tokens and refresh them before expiry.
3. As an authenticated user I can create a ride and become its owner; the system returns a 6-character invite code.
4. As an authenticated user I can join a ride by its invite code if it has fewer than 3 active members and is not ended.
5. As a member I can set or update the shared destination (name, lat/lng, optional `place_id`).
6. As a member I can request a route from my current origin to the shared destination and receive distance, ETA, encoded polyline, and travel mode.
7. As a member I can open a WebSocket to the ride and publish `location.update` messages.
8. As a member I receive `location.broadcast` messages for my peers' positions.
9. As a member I receive `rider.online` / `rider.offline` and `member.joined` / `member.left` events.
10. As a member I can exchange `voice.offer`, `voice.answer`, and `voice.ice_candidate` with a target peer via the WebSocket.
11. As a member I can leave the ride.
12. As an owner I can end the ride, which closes the room, marks the ride ended, and invalidates the invite code.
13. As a member I can fetch ride and member metadata via GraphQL queries.
14. **As a member I can tap on another rider in the UI and see the actual map-route distance and ETA between us** (`peerRoute` GraphQL query). The distance reflects road routing, not straight-line Haversine.
15. **As a pillion I can pair with the rider on the same bike** (`pairWithRider`) so we count as one entity on the map and only the rider's location is published. Saves both upstream WS bandwidth (no pillion `location.update`) and provider API spend (`peerRoute` against me reuses the rider's location). I can tap **"Detach"** (`detachFromRider`) to leave the pair and start sharing my own GPS independently.
16. **As an owner I can invite someone by email, phone, OR license plate** (`inviteByIdentifier`). Plate match may resolve to multiple users (e.g. a rider+pillion pair on the same bike); the system creates one pending invitation per matched user. The invited user calls `acceptInvitation` to join.
17. **As any user I can store an emergency contact** (`updateProfile.emergencyContact`) — name, phone, email, and optionally a link to another user in the system (e.g. Aditi as Sohan's emergency contact). Emergency contacts can be in-system riders or external (non-rider) people.
18. **As any user I can trigger an emergency** (`triggerEmergency`). The server snapshots the active ride membership at that instant and records the event; the immediate riding group is notified (poll via `myActiveEmergency`; WS push delivered in Phase 7). The user or any active member can call `resolveEmergency` to clear it.
19. **As any user I can record bike/health profile data** — blood type, age, height, weight, and tyre pressures for two states (solo and solo+pillion). All optional; surfaced to the caller on `me` and to other users only via per-field privacy gates (see API §3.6 Privacy).

## 7. Functional Requirements
- **FR-1** Authentication: register, login, refresh; password hashing; access + refresh tokens; auth middleware for HTTP and WS.
- **FR-2** Ride lifecycle: create → (members join) → start (implicit on first activity or explicit owner action) → end. End is final and irreversible.
- **FR-3** Invite code: 6 chars, base32-style alphabet excluding ambiguous chars (`0`, `O`, `1`, `I`, `L`); becomes invalid the instant ride is ended.
- **FR-4** 3-member cap enforced transactionally (`SELECT … FOR UPDATE` on ride row before insert into `ride_members`).
- **FR-5** Destination set/get with validation (lat/lng bounds, name 1–120 chars, optional `place_id`).
- **FR-6** Route compute via pluggable provider behind a single Go interface; v1 ships **two adapters — Google Routes API v2 and Mapbox Directions API** — with the active provider chosen by the `ROUTE_PROVIDER` env var. Supports `TWO_WHEELER` where available; falls back to bicycle/cycling otherwise.
- **FR-6b** **Peer-route distance** — `peerRoute(rideId, peerUserId)` GraphQL query returns the routed distance/ETA between the caller and a peer in the same ride, computed via the same Provider interface. Origin = caller's last-known location (or `originOverride`); destination = peer's last-known location. Refuses if either side is stale (> `PEER_ROUTE_STALE_TTL`, default 30 s) or missing.
- **FR-8** **Multi-identifier invite** — `inviteByIdentifier(rideId, identifier)` accepts an email, phone, or license plate. Resolution: email → exact match; phone → normalized digit comparison; plate → uppercased + trimmed exact match. Plate matches multiple users → an invitation per matching user is created.
- **FR-9** **Emergency contact** — every user may persist an emergency contact via `updateProfile`. Contact can be either an in-system user (`emergencyContactUserId`) or an external triple (name + phone + email). Both can coexist; clients pick which to surface.
- **FR-10** **Emergency event** — `triggerEmergency(message?)` snapshots the caller's active ride members at trigger time and records an event. `myActiveEmergency` returns it for any active ride member. `resolveEmergency(eventId)` closes it. WS push of `emergency.triggered` lands in Phase 7.
- **FR-11** **Profile fields** — bike/health data on User: `bloodType`, `age`, `heightCm`, `weightKg`, `tyrePressure { frontSoloPsi, rearSoloPsi, frontPillionPsi, rearPillionPsi }`. All optional; validated to safe ranges (5–100 PSI; 50–250 cm; 20–500 kg; age 5–120).
- **FR-7** **Rider/pillion pairing** — two members of the same ride may form an active pair via `pairWithRider(rideId, riderUserId)` (pillion-initiated). While paired:
  - The pillion's WS suppresses `location.update`. The server uses the rider's last-known location for the pillion in all routing and broadcast.
  - `location.broadcast` from the rider carries `paired_user_ids: [pillionUserId]` so clients render both icons co-located.
  - `peerRoute(X, pillionUserId)` reuses the rider's location (no provider call for the same point).
  - `peerRoute(rider, pillion)` returns 0 distance, 0 duration, empty polyline, with a sentinel `provider="self_pair"` so clients can short-circuit.
  - `detachFromRider(rideId)` (pillion-initiated) ends the pair; the pillion resumes independent location sharing.
- **FR-7** Route response includes `distance_meters`, `duration_seconds`, `encoded_polyline`, `provider`, `travel_mode`.
- **FR-8** Per-user and per-ride throttling on route requests (default 1 / 30 s per user, 6 / 10 min per ride) with 429 + `Retry-After`.
- **FR-9** WebSocket per ride: `GET /api/v1/ws/rides/{ride_id}`. Auth + membership + ride status are validated on upgrade.
- **FR-10** WebSocket message envelope: `type`, `ride_id`, `timestamp`, `payload`, with `from_user_id` for sender messages and `to_user_id` for targeted messages.
- **FR-11** Online/offline tracking is membership-scoped and ephemeral; not persisted.
- **FR-12** WebRTC signaling pass-through with sender/target validation; server never inspects SDP/ICE bodies beyond schema/length checks.
- **FR-13** Heartbeat with `ping` / `pong`; idle disconnect after configurable timeout.
- **FR-14** Structured JSON logs with sensitive-field redaction (lat/lng, polyline, tokens, password).
- **FR-15** Optional cached `ride_routes` rows with 30-min TTL keyed by (ride, user, origin geohash).

## 8. Non-Functional Requirements
- **NFR-1 Performance** — p95 location fanout ≤ 500 ms in-region; p95 route compute ≤ 1 s (cache hit ≤ 50 ms).
- **NFR-2 Availability** — 99.5% monthly for HTTP and WS endpoints.
- **NFR-3 Security** — TLS for HTTP, WSS for WS in production; secrets only via env or secret manager; OWASP ASVS L1 baseline.
- **NFR-4 Privacy** — raw lat/lng and encoded polylines never appear in production logs; redaction at the logger layer.
- **NFR-5 Observability** — structured logs with request IDs; metrics for HTTP/WS rates, error rates, WS connections, route provider calls and cache hit ratio.
- **NFR-6 Cost control** — provider call rate cap-able per ride and per minute; cache short-circuits identical lookups.
- **NFR-7 Portability** — single-binary Go service, containerized, runs on any Linux host or container platform.
- **NFR-8 Compliance** — code complies with `coding_standards.json` (root); CI fails on lint/standard violations.
- **NFR-9 Low-bandwidth operation** — riders won't always have 4G/5G. The system must function over cellular links from **1 kbps up to 60 kbps** sustained throughput. Behavior across the range:
  - **Location publish/broadcast must always take priority over voice signaling.** Voice may be degraded or suspended; location must not be starved.
  - Wire format defaults to **TOON** (Token-Oriented Object Notation) instead of JSON for ~30–50% byte savings on the hot path.
  - WebSocket `permessage-deflate` is enabled; HTTP responses are gzip/br compressed (auth endpoints excluded — see SECURITY).
  - Server emits `network.advisory` messages to recommend a tier (`high`/`medium`/`low`/`critical`) and target publish cadence; client may report observed link quality via `client.advisory`.
  - Hard caps shrink under degraded tiers: `location.update` 2 Hz → 1 Hz → 0.5 Hz → 0.2 Hz across `high`/`medium`/`low`/`critical`. `voice.*` is shed entirely in `critical`.
  - At the 1 kbps floor the system runs as a sparse location-ticker (~50 B/s) plus minimal control plane; reconnection is expensive at this tier and is avoided.

## 9. Success Metrics
- ≥ 95% of join attempts succeed within 1 s when capacity exists.
- ≥ 99% of `location.update` publishes are delivered to all peers within 1 s.
- 0 incidents of provider API key disclosure.
- Provider spend ≤ configurable monthly budget per ride.

## 10. Constraints
- **Exactly 3 riders** per ride (creator + 2). Enforced server-side.
- **No frontend** is in scope. Client-side WebRTC, maps, and UI are explicitly excluded.
- **No media relay** for voice. Backend only forwards SDP/ICE.
- **No location history** persisted in MVP unless explicitly enabled by feature flag.
- **Bandwidth target** — supported link range is **1–60 kbps**. Below ~3 kbps the system operates in `critical` tier (location-only at 0.2 Hz) with voice signaling shed.

## 11. Out of Scope (v1)
- Push notifications, email, SMS.
- Group text chat.
- Voice recording or transcription.
- Multi-device per user (single active session per user per ride).
- Public sharing.

## 12. Risks
- **R1** Provider quota or pricing changes — mitigated by provider abstraction and rate limit/cache controls.
- **R2** WebSocket scale on a single instance — single-region single-instance for v1; horizontal scale with Redis pub/sub deferred to v2.
- **R3** Battery / GPS noise from client — server validates accuracy/speed and may drop low-quality samples.
- **R4** Coding standards compliance — `coding_standards.json` is missing at the time of writing; implementation must wait until it lands.
- **R5** TOON Go ecosystem maturity — TOON is a recent format; the Go library and version must be vetted in implementation Phase 0. Documented fallback: CBOR (`application/cbor`, subprotocol `cbor.v1`) without changing the envelope or message types.
- **R6** Two-wheeler mapping ambiguity — `TWO_WHEELER` is a motorcycle profile in some apps and a bicycle profile in others. v1 maps to `cycling` on Mapbox by default (this is a *bike* companion); set `MAPBOX_PROFILE_TWO_WHEELER=driving` for motorcycle deployments.
- **R7** Peer-route freshness — `peerRoute` depends on in-memory presence; if the peer's WS is disconnected or sparse, the resolver returns `PEER_LOCATION_STALE` / `PEER_LOCATION_MISSING`. Documented for client UX guidance ("ask Alice to share location"). No fallback to Haversine — by user requirement.

## 13. Open Questions
- Do we mandate email or also support phone? *Schema accommodates either; v1 ships email; phone schema-ready behind a feature flag.*
- Do we offer an explicit "start" transition, or is the ride active on creation? *Default: rides are `active` from creation; owner ends explicitly.*
- Mid-ride destination change — allowed? *Yes, owner can update destination; any active route cache for that ride is invalidated and `destination.updated` is fanned out.*
