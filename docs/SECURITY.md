# Security Plan — RideAndConnect Backend

> Companion to API, WEBSOCKET_PROTOCOL, DATABASE_SCHEMA, ROUTE_PROVIDER.
> Coding Standards: must comply with `coding_standards.json` (root).

## 1. Threat Model (STRIDE)

Assets: user accounts, auth tokens, ride membership, real-time location, voice signaling, route provider API key, route polylines.

| Threat | Surface | Mitigation |
|---|---|---|
| **Spoofing** identity | Login, refresh, WS connect | Argon2id passwords, JWT signature verification, refresh-token rotation, WS auth + membership check, server-bound `from_user_id` |
| **Tampering** with messages | HTTP body, WS frame | TLS/WSS, JSON schema validation, server-stamped `timestamp`, max message size |
| **Repudiation** | Mutating actions | `audit_events` rows for ride lifecycle, request IDs in logs |
| **Info disclosure** | Logs, error messages, third-party API | Logger field-allowlist redaction, generic prod error messages, server-side route key, no polylines/coords in logs |
| **Denial of service** | Auth, route, WS | Per-IP and per-user rate limits, WS bounded queues, slow-consumer drop, circuit breaker on provider |
| **Elevation of privilege** | Owner-only endpoints, WS messages | Auth → membership → role check on every relevant call; transactional 3-cap |

## 2. Authentication

### 2.1 Passwords
- Hash: argon2id with time=2, memory=64 MiB, parallelism=1 (tune to ~250 ms server CPU).
- Min length 12, max 128, must mix character classes (≥3 of upper/lower/digit/symbol).
- No password reuse check (out of scope v1).
- Failed-login throttling: 5/min/IP and 10/min/email; emit `INVALID_CREDENTIALS` (never reveal which of email/password is wrong).

### 2.2 Tokens
- **Access token** — JWT HS256, 15 min TTL, payload claims: `sub` (user_id), `iat`, `exp`, `jti`, `nbf`, `scope=access`. No PII.
- **Refresh token** — opaque 32-byte URL-safe random. Stored only as SHA-256 hash in `refresh_tokens`. Single-use rotation: refresh emits a new token and revokes the old via `replaced_by`.
- **WS ticket** — opaque 32-byte URL-safe random, 5 min TTL, **single-use**. Stored as SHA-256 hash in `ws_tickets`; consumption is recorded server-side via `consumed_at`. A leaked ticket cannot be replayed because `VerifyWSTicket` uses `SELECT … FOR UPDATE` + atomic `consumed_at` update inside one transaction. Used as `?access_token=` only when headers are unavailable (browser `WebSocket` API).
- **JWT secret** — env-only, ≥ 32 bytes random; rotation supported via overlapping accept-set (current + previous valid).
- **Logout** — revokes the supplied refresh token; access tokens expire naturally.

### 2.3 Auth Middleware
- Order: request ID → recoverer → CORS → logger → rate limit → JWT verify → membership check (selective).
- Reject Bearer tokens with `alg=none` or unexpected `alg`.
- Reject when `exp < now`, `nbf > now`, signature fails, or `sub` is malformed.

## 3. Authorization

### 3.1 Membership
- Every ride-scoped GraphQL resolver and WS message handler resolves `(user_id, ride_id)` against `ride_members` with `left_at IS NULL` and `rides.status='active'` (or, for read-only fetches, also `ended` rides where the user was a member).
- A central helper `requireRideMember(ctx, rideID, userID)` is the single enforcement point — used by GraphQL resolvers via `auth.MustUser(ctx)` and by WS handlers on every relevant message.

### 3.2 Role
- `requireRideOwner(ctx, rideID, userID)` for `endRide` and any owner-only operation.

### 3.3 Peer-route additional checks
For `peerRoute(rideId, peerUserId, ...)`:
- Caller must be an active member of `rideId`.
- `peerUserId` must be an active member of `rideId`.
- `peerUserId ≠ caller`.
- The caller's `originOverride` (if provided) is treated as untrusted client input and validated like any coordinate.
- The peer's location is read from server-side presence only; clients cannot influence what the server believes about a peer's position.

### 3.3 WebSocket
- Membership and role checks happen on upgrade AND on every inbound message that has authorization implications (e.g. `voice.*` `to_user_id` must be a current active member).
- `from_user_id` MUST equal the authenticated user; mismatch → close with `4007 SPOOFED_SENDER`.

## 4. Input Validation

### 4.1 HTTP
- Strict JSON: reject unknown fields (`json.Decoder` with `DisallowUnknownFields`).
- Body size cap: 64 KB (configurable). Larger requests return 413 immediately.
- Content-Type must be `application/json`.

### 4.2 Coordinates
- Numeric, within bounds; reject `NaN`, `+Inf`, `-Inf`.
- Round to ≤ 6 decimal places before storage (no leakage of higher-precision noise).

### 4.3 Strings
- Unicode NFC normalize, then enforce byte-length caps.
- Reject control characters except tab/newline where applicable.
- `place_id`: ASCII printable, ≤ 256 bytes.

### 4.4 WS Payloads
- Same rules as HTTP for shared fields.
- `sdp` ≤ 8 KB, `candidate` ≤ 1 KB. Stored or relayed verbatim — never parsed.
- Throttling per §6.

### 4.5 IDs
- All IDs must parse as UUID before any DB call.
- Path-bound IDs are validated by middleware before handlers run.

## 5. Transport Security
- TLS 1.2+ with modern ciphers in production; HSTS `max-age=31536000; includeSubDomains` once a stable domain is in place.
- WSS only in production. The application refuses to start with `APP_ENV=prod` if it detects an `http://` or `ws://` listener without an upstream TLS terminator.
- `TRUST_PROXY_HEADERS` (env, default `false`) controls whether `X-Forwarded-For` / `X-Real-Ip` are honored when reading the client IP for rate limiting and access logs. Enable ONLY when the API is behind a reverse proxy that strips and replaces these headers; otherwise an attacker reaching the listener directly can spoof the source IP.
- HTTP→HTTPS redirect handled by reverse proxy.
- CORS: explicit allowlist via `CORS_ALLOWED_ORIGINS`; default empty (no cross-origin in dev). `Access-Control-Allow-Credentials: false`.

## 6. Rate Limiting

| Surface | Rule |
|---|---|
| Global per IP | 20 rps default (`RATE_LIMIT_RPS`) |
| `/auth/register`, `/auth/login` | 5 / min / IP |
| `/auth/refresh`, `/auth/ws-ticket` | 30 / min / IP |
| `/rides/*` mutating | 30 / min / user |
| `/rides/*/route` | 1 / 30 s / user, 6 / 10 min / ride |
| WS `location.update` | 2 / s / connection |
| WS `voice.*` | 30 / 10 s / connection |
| WS other client messages | 5 / s / connection |

429s include `Retry-After`. Repeated WS violations close with `4004 RATE_LIMITED`.

## 7. Secrets Management
- All secrets via environment variables in dev/test (`.env` not committed) and via a secret manager in prod.
- Required prod secrets depend on `ROUTE_PROVIDER`:
  - Always: `JWT_SECRET` (≥ 32 bytes), `DATABASE_URL`.
  - When `ROUTE_PROVIDER=google`: `GOOGLE_ROUTES_API_KEY`.
  - When `ROUTE_PROVIDER=mapbox`: `MAPBOX_ACCESS_TOKEN`.
  - App refuses to boot if any required value is missing or weak.
- Secrets are redacted from logs by the field-allowlist redactor (adds `mapbox`, `access_token`, `x_goog_api_key`).
- Each provider's HTTP client uses a wrapped transport that strips its auth token from any error/log context.

## 8. Logging

### 8.1 What we log
- One structured JSON line per HTTP request and per WS lifecycle event.
- Fields: `request_id`, `user_id` (if authed), `ride_id` (if applicable), `route`, `status`, `latency_ms`, `error.code`, `error.message` (sanitized).
- WS events: `ws.connect`, `ws.auth_failed`, `ws.message_in` (type + size), `ws.message_out` (type + size), `ws.close` (code).

### 8.2 What we never log (production)
- `latitude`, `longitude`, `lat`, `lng`, `coords`, `coordinates`
- `encoded_polyline`, `polyline`
- `password`, `password_hash`
- `token`, `refresh_token`, `access_token`, `authorization`
- `sdp`, `candidate`
- Provider raw response bodies (only digests permitted)
- Email and phone in unmasked form (mask to `a***@example.com`)

### 8.3 Implementation
- A redactor configured at logger init enforces the deny-list.
- Tests assert that a synthetic log of a sample request does not contain any forbidden key.

## 9. Privacy
- No real-time location persistence in MVP (`LOCATION_PERSIST=false` by default).
- `ride_routes` keeps the encoded polyline only; clients fetch through authenticated endpoints; no third-party access.
- Account deletion endpoint (out of scope v1) will purge `users`, cascade `ride_members`, and null `audit_events.actor_user_id`.

## 10. Database Hardening
- App connects with a non-superuser role with only the privileges it needs (`SELECT, INSERT, UPDATE, DELETE` on application tables; no `CREATE`).
- Migrations run as a separate role with DDL privileges, applied at deploy time.
- Connection requires TLS in prod (`sslmode=require` minimum, `verify-full` preferred).
- Statement timeout: `statement_timeout = '5s'` at the role level for the app.

## 11. WebRTC Signaling Specifics
- Server validates schema, sender identity, target membership, payload size.
- Server does not parse SDP or candidates.
- Server does not record voice metadata beyond connection-level counters.
- TURN server (if added later) is a separate service with its own credentials and is out of scope here.

## 12. Audit Trail
- `audit_events` rows for: `user.registered`, `user.logged_in`, `ride.created`, `ride.member_joined`, `ride.member_left`, `ride.ended`, `destination.updated`, `route.computed`, `auth.refresh_revoked`, `auth.logout`.
- Metadata excludes lat/lng/polyline by policy and validator.

## 13. Dependency & Supply Chain
- `go mod` with `GOFLAGS=-mod=readonly` in CI.
- `govulncheck` runs in CI on every PR.
- Container base: distroless or `gcr.io/distroless/static` for the final image.
- Reproducible build: `-trimpath`, deterministic `-buildvcs=false` plus build-info embedded via `-ldflags`.
- All third-party libraries are pinned; updates require a PR.

## 14. Testing
- Unit tests for validators (lat/lng, invite code, sizes).
- Integration tests asserting:
  - WS connection refused without auth.
  - WS connection refused for non-member.
  - 3-cap enforced under concurrent joins.
  - Owner-only endpoints reject non-owners.
  - `from_user_id` spoof closes connection.
  - Logs do not contain redaction-listed keys.
  - 429 returned and `Retry-After` set under throttle.
- Optional fuzzing (`go test -fuzz`) for envelope parser and validators.

## 15. Incident Response (lightweight)
- A runbook accompanies the implementation containing:
  - Provider key compromise: rotate via secret manager, redeploy, force token rotation by bumping `JWT_SECRET` (invalidates all access tokens).
  - DB compromise: rotate DB role password, re-issue connection strings, force refresh-token revocation (`UPDATE refresh_tokens SET revoked_at = now() WHERE revoked_at IS NULL`).
  - DoS: enable stricter rate limits via env hot-reload (deferred to v2; v1 requires redeploy).

## 16. GraphQL-specific Hardening
- Single endpoint `POST /api/v1/graphql`; `GET` not accepted (avoids CSRF and accidental caching).
- Introspection disabled in `prod`.
- Query depth limit (`GRAPHQL_DEPTH_LIMIT`, default 8) and complexity limit (`GRAPHQL_COMPLEXITY_LIMIT`, default 200) enforced by gqlgen middleware.
- Operation type allowlist: `query`, `mutation` only. No subscriptions — realtime lives on the custom WS.
- Resolvers do their own membership/role checks; gqlgen's directives are not relied on as the sole authorization layer.
- All GraphQL errors flow through a sanitizer that strips internal stack traces in `prod` and maps domain errors to a stable `extensions.code` value (see API §1.3).
- Persisted queries are not enabled in v1 — every request carries the full operation. v2 may introduce them for cost/throttle attribution.

## 17. Compression Considerations (CRIME / BREACH)

The system uses HTTP gzip/br and WebSocket `permessage-deflate` to fit within the 1–60 kbps target (see WEBSOCKET_PROTOCOL §9). CRIME/BREACH-class attacks require an attacker who can (a) inject chosen plaintext into the same compressed response that contains a secret, and (b) observe the response size. Our exposure is limited because:

- Authenticated responses contain only the caller's own data — there is no cross-user reflection of attacker input.
- Tokens (access, refresh, WS ticket) are returned only by `/auth/*` endpoints, **and those endpoints are forced to `Content-Encoding: identity`**.
- WebSocket frames don't mix attacker-controlled and victim-secret data on a single connection (a WS connection is bound to one authenticated user).

Mitigations applied:

- **Auth endpoints uncompressed.** `/auth/register`, `/auth/login`, `/auth/refresh`, `/auth/ws-ticket` always emit `Content-Encoding: identity`.
- **WS deflate per-connection only.** No shared dictionary across connections in v1; the optional ride-specific dictionary (WS §9.2) is opt-in and contains only public ride/user IDs already known to the connection's user.
- **Decompression-bomb defense.** Wire size capped (`WS_MAX_WIRE_BYTES`, default 16 KB) and decoded size capped (`WS_MAX_MSG_SIZE`, default 8 KB). Inbound HTTP request bodies are capped at 64 KB **decompressed**.
- **No secret reflection in error messages.** Error envelopes do not echo secrets back; only the request ID and a stable error code.
- **TLS-only.** Compression is layered above TLS. We do not rely on TLS-level compression (it is disabled by Go's TLS defaults).

## 18. Compliance Mapping
| OWASP ASVS L1 control | Implementation |
|---|---|
| V2 Authentication | §2 |
| V3 Session Management | §2.2 (rotation, single-use) |
| V4 Access Control | §3 |
| V5 Validation | §4 |
| V7 Cryptography | argon2id, HS256 (256-bit secret), TLS |
| V8 Error / Logging | §8 |
| V9 Data Protection | §9 |
| V10 Communications | §5 |
| V13 API and Web Service | All API rules |
