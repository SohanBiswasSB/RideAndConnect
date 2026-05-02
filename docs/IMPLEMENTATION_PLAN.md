# Implementation Plan — RideAndConnect Backend

> Companion to all docs in `docs/`. Implementation does NOT begin until `coding_standards.json` is added at the repo root.
> Toolchain: Go 1.26.2 inside conda env `RideAndConnect`. Activate with `conda activate RideAndConnect` before any command.

## 1. Pre-Implementation Gates

The Implementation Agent (per Master Orchestrator workflow) must verify ALL of the following before writing code:

- [ ] All 9 docs in `docs/` present and reviewed: PRD, ARCHITECTURE, API, WEBSOCKET_PROTOCOL, DATABASE_SCHEMA, ROUTE_PROVIDER, SECURITY, IMPLEMENTATION_PLAN, POSTMAN_COLLECTION_PLAN.
- [ ] `coding_standards.json` present at repo root with rules covering: package layout, naming, error handling, logging, dependency injection, test patterns, and lint config.
- [ ] `.env.example` filled with every variable from ARCHITECTURE §6.
- [ ] PostgreSQL 16+ available locally via `docker compose up -d postgres`.
- [ ] `golangci-lint`, `gofumpt`, `goimports`, `sqlc`, `golang-migrate` installed in the conda env.
- [ ] **Codec library selected and pinned** — TOON Go library evaluated; if not production-ready, CBOR (`fxamacker/cbor/v2`) selected as fallback. Decision recorded in `docs/decisions/0001-wire-codec.md`.
- [ ] **Bandwidth-shaping test harness** available — `tc netem` or a Go-level rate-limiting `net.Listener` shim that can simulate 1, 3, 10, 30, 60 kbps for the Phase 7 acceptance benchmarks.
- [x] **GraphQL toolchain** — `github.com/graph-gophers/graphql-go` (no codegen). SDL in `schema/schema.graphqls` is the source of truth; mirrored to `internal/graph/schema.graphqls` for `//go:embed`. Resolvers are hand-written under `internal/graph/`. Decision recorded inline (see ARCHITECTURE §3.1.1) — switched away from gqlgen to avoid the codegen step for this small schema.
- [ ] **Both route provider keys obtained** for dev — at least one of `GOOGLE_ROUTES_API_KEY` or `MAPBOX_ACCESS_TOKEN` set in `.env`. Provider unit tests use recorded fixtures and don't require live credentials.

## 2. Phases & Milestones

### Phase 0 — Repo Bootstrap (deliverables, not code)
- [ ] Create the project layout from ARCHITECTURE §4.
- [ ] Add `Makefile` targets: `run`, `test`, `lint`, `migrate-up`, `migrate-down`, `sqlc`, `docker-build`, `docker-run`.
- [ ] Add `.golangci.yml` derived from `coding_standards.json`.
- [ ] Add `Dockerfile` (multi-stage, distroless final).
- [ ] Add `docker-compose.yml` with `postgres:16` service and the API.
- [ ] Add `.env.example`.
- [ ] Add `README.md` with: prerequisites, conda activate, env setup, run commands, doc links.
- [ ] Add `sqlc.yaml`.
- [ ] Initialize `go.mod` with module path; pin direct deps.

**Done when:** `make lint` passes on an empty package skeleton; `make migrate-up` runs (no migrations yet).

### Phase 1 — Database & Migrations
- [ ] Author migrations 0001–0008 per DATABASE_SCHEMA §7.
- [ ] Author `sql/queries/*.sql` for every query needed by services (users, rides, ride_members, refresh_tokens, ride_routes, audit_events).
- [ ] Run `sqlc generate`; commit generated code.
- [ ] Add `internal/database` with `pgxpool` setup, health ping, and a thin `Querier` wrapper that wires sqlc.

**Done when:** `make migrate-up` succeeds against `docker compose` Postgres; sqlc-generated code compiles; `go test ./internal/database/...` passes a smoke test.

### Phase 2 — Config, Logging, Middleware
- [ ] `internal/config`: env parsing + validation (fail-fast in prod).
- [ ] `internal/logging`: zerolog setup; redactor with the deny-list from SECURITY §8.2.
- [ ] `internal/middleware`: request ID, recoverer, structured logger, CORS, rate limit (per-IP and per-user), JSON content-type, request-size cap.
- [ ] Add `cmd/api/main.go` minimal server bootstrapping middleware + healthz/readyz/version.

**Done when:** `make run` serves `/healthz`, `/readyz`, `/version`; logs are JSON; redactor unit tests pass.

### Phase 3 — Auth (register, login, refresh, wsTicket) — GraphQL
- [ ] `internal/auth`: argon2id password helper, JWT issue/verify, refresh-token issue/rotate/revoke, WS ticket issue/redeem.
- [ ] `internal/users`: profile read.
- [ ] GraphQL resolvers in `internal/graph` for `register`, `login`, `refresh`, `logout`, `wsTicket`, `me`.
- [ ] Auth middleware (`requireAuth`) attaches user identity to the request context; resolvers call helpers like `auth.MustUser(ctx)`.
- [ ] Tests: handler/resolver tests, service tests, validator tests.

**Done when:** API.md §2 mutations/queries behave per spec; rate limits applied at the transport layer; unit + integration tests green.

### Phase 4 — Rides Lifecycle — GraphQL
- [ ] `internal/rides`: invite-code generator (alphabet `[A-HJ-NP-Z2-9]`, length 6, uniqueness retry), create, get, members list, join (transactional 3-cap), leave (with owner transfer / auto-end), end (with WS room close + invite-code invalidation).
- [ ] GraphQL resolvers for `createRide`, `joinRide`, `leaveRide`, `endRide`, `ride`, `rideMembers`.
- [ ] `requireRideMember` and `requireRideOwner` helpers used inside resolvers.
- [ ] Concurrency tests for 3-cap (parallel joins).

**Done when:** API contract §3 endpoints behave per spec; concurrency test reliably rejects the 4th joiner; tests green.

### Phase 5 — Destination — GraphQL
- [ ] `internal/destination`: set/get + emit `destination.updated` to room.
- [ ] Cache invalidation: clear `ride_routes` rows where `route_kind='DESTINATION'` for the ride on destination update; peer-route rows untouched.
- [ ] GraphQL resolvers `setDestination`, `destination`.
- [ ] Tests: validator, resolver, integration with WS broadcast.

**Done when:** API §4 endpoints behave per spec; broadcast emitted to connected members; tests green.

### Phase 6 — Route Providers (Google + Mapbox) + Peer Route
- [x] `internal/routes`: `Provider` interface, factory, error sentinels (Phase 0 deliverable, ahead of schedule).
- [x] `internal/routes/google`: Google Routes v2 adapter per ROUTE_PROVIDER §3.
- [x] `internal/routes/mapbox`: Mapbox Directions adapter per ROUTE_PROVIDER §4.
- [ ] Geohash7 helper.
- [ ] Cache lookup/insert via sqlc against `ride_routes` (handle both `route_kind` values).
- [ ] Throttles for `computeRoute` (per user × ride, per ride) and `peerRoute` (per (caller, peer) pair, per ride).
- [ ] Circuit breaker per provider.
- [ ] GraphQL resolvers: `computeRoute` mutation, `myRoute` query, `peerRoute` query.
- [ ] Peer-route presence integration: read last-known location from `internal/realtime` Hub; staleness guard.
- [ ] Tests: provider fixture/contract tests (Google + Mapbox), cache hit/miss tests, throttle tests, breaker tests, peer-route stale/missing tests.

**Done when:** Both providers swap with a single env change; `peerRoute` resolves road-routed distance between two riders' last-known positions; metrics emitted per provider and per route kind; logs contain no coordinates or polylines.

### Phase 7 done-bits (current state)
- ✅ Hub, Room, Connection, presence store wired through to `peerRoute` (`internal/realtime`).
- ✅ Auth on upgrade (Bearer header OR `?access_token=` ws-ticket).
- ✅ Pair-aware location.broadcast: paired pillion's `location.update` is suppressed; rider's broadcast carries `paired_user_ids`.
- ✅ Voice signaling relay (`voice.offer` / `voice.answer` / `voice.ice_candidate`) with sender/target validation and 8 KB SDP / 1 KB ICE size caps.
- ✅ Heartbeat (server-initiated 25 s ping; idle close at 60 s).
- ✅ Emergency push (`hub.BroadcastEmergency` invoked from the `triggerEmergency` resolver).
- ✅ Destination broadcast (`hub.BroadcastDestination`) and ride-end broadcast (`hub.BroadcastRideEnded`).

### Phase 7 staged for follow-up
- ⏭ TOON codec (the seam is `internal/realtime/connection.go::writeEnvelope` and `readLoop`'s `json.Unmarshal` call — both swap to the codec interface in one diff).
- ⏭ `permessage-deflate` (coder/websocket flag + acceptance opts).
- ⏭ Strict-priority send scheduler (per `WEBSOCKET_PROTOCOL.md §10.2`) — the per-connection send channel is a single FIFO today; QoS prioritization between location/control/voice queues is the next layer.
- ⏭ Bandwidth tiers (`high`/`medium`/`low`/`critical`) and `network.advisory` emission.
- ⏭ Slow-consumer drop policy is in place but the per-`from_user_id` location coalescing under backlog is not yet implemented.

### Phase 7 — WebSocket Realtime + Codec + QoS
- [ ] `internal/realtime`: Hub, Room, Connection, fan-out, presence, heartbeat, supersede, slow-consumer drop.
- [ ] WS upgrade handler at `/api/v1/ws/rides/{id}` with auth + membership check.
- [ ] Subprotocol negotiation: `toon.v1` preferred, `json.v1` fallback. `permessage-deflate` enabled by default.
- [ ] `internal/wire`: codec abstraction with TOON (default) and JSON impls; field-name shortener for the location hot path; size-cap enforcement (`WS_MAX_MSG_SIZE`, `WS_MAX_WIRE_BYTES`); decompression-bomb defense.
- [ ] Envelope codec, server-stamped timestamp, `from_user_id` enforcement.
- [ ] **Strict-priority send scheduler** with three queues (P0 location/lifecycle, P1 control, P2 voice signaling) and per-`from_user_id` location coalescing under backlog.
- [ ] **Per-connection bandwidth tier** (`high`/`medium`/`low`/`critical`) driven by RTT EWMA from heartbeats and `client.advisory` reports. Adaptive ping interval and inbound caps per WEBSOCKET_PROTOCOL §10.3.
- [ ] Server-emitted advisories: `network.advisory` on connect / tier change (rate-limited).
- [ ] Voice prioritization rule: shed `voice.*` in `critical` tier; emit `network.advisory{voice_signaling_allowed:false}`.
- [ ] Message handlers: `location.update`, `destination.set`, `route.request`, `voice.offer`, `voice.answer`, `voice.ice_candidate`, `ping`/`pong`, `client.advisory`.
- [ ] Server-emitted: `location.broadcast`, `destination.updated`, `route.updated`, `rider.online`/`offline`, `member.joined`/`left`, `ride.ended`, `error`, `network.advisory`.
- [ ] WS rate limits per WEBSOCKET_PROTOCOL §5 and §10.3.

**Done when:** WS protocol matches WEBSOCKET_PROTOCOL.md; integration tests cover happy path and every close code; broadcast latency p95 ≤ 500 ms in local benchmark; **a 1 kbps-shaped link benchmark sustains location updates for 10 minutes without disconnect** while voice signaling is correctly shed; **a 60 kbps benchmark sustains all features at high tier**.

### Phase 8 — Signaling Hardening
- [ ] `internal/signaling`: validators for `voice.offer`/`answer`/`ice_candidate`; size caps; sender/target authorization.
- [ ] Tests: spoofed-sender close, target-not-member rejection, oversized payload rejection.

**Done when:** All signaling negative tests green; logs assert no `sdp`/`candidate` content stored.

### Phase 9 — Observability
- [ ] Prometheus `/metrics` (auth-protected outside `dev`).
- [ ] Counters/histograms per ROUTE_PROVIDER §8 and additional WS counters (`ws_connections_open`, `ws_messages_total{type, direction}`).
- [ ] OpenTelemetry plumbing (no-op exporter default).
- [ ] Log redaction tests.

**Done when:** Local `curl /metrics` returns metrics; redaction tests green.

### Phase 10 — Tests & Quality
- [ ] Unit tests across all packages.
- [ ] Handler tests via `httptest`.
- [ ] Integration tests (Docker-Compose-backed).
- [ ] WS integration tests using a real WS client.
- [ ] Provider integration test using recorded fixtures (no live API).
- [ ] `go test -race ./...` clean in CI.
- [ ] `golangci-lint run` clean.
- [ ] `govulncheck` clean.

**Done when:** Coverage ≥ 75% on `internal/`; CI pipeline runs all of the above.

### Phase 11 — DevOps
- [ ] CI workflow (GitHub Actions or equivalent): lint, test, build, govulncheck, docker build.
- [ ] Container image published on tag.
- [ ] Healthcheck wired in compose.
- [ ] Optional: deploy script / IaC stub.

**Done when:** A green CI build produces a tagged container image; deploy steps documented in README.

## 3. Acceptance Criteria (system-wide)

- All 14 architecture rules from the brief are satisfied (no map render, no UI logic, etc.).
- All 20 security requirements from the brief are satisfied (auth on REST + WS, membership validation, no sensitive logs, key protection, HTTPS/WSS, etc.).
- All listed REST endpoints, WS message types, and DB tables present and behaving per spec.
- Logs in production confirmed free of forbidden keys via a smoke scan job.

## 4. Estimated Effort (single senior backend engineer)

| Phase | Estimate |
|---|---|
| 0 Bootstrap | 0.5 day |
| 1 DB & Migrations | 1 day |
| 2 Config/Logging/Middleware | 1 day |
| 3 Auth | 1.5 days |
| 4 Rides | 1.5 days |
| 5 Destination | 0.5 day |
| 6 Route Providers (Google + Mapbox) + Peer Route | 3 days |
| 7 WebSocket + Codec + QoS | 3 days |
| 8 Signaling Hardening | 0.5 day |
| 9 Observability | 0.5 day |
| 10 Tests & Quality | 1.5 days |
| 11 DevOps | 0.5 day |
| **Total** | **~14.5 days** |

## 5. Testing Strategy

| Test type | Scope | Tools |
|---|---|---|
| Unit | Validators, helpers, codecs, geohash, invite-code generator | `go test` |
| Service | Each `internal/<domain>` service with a mocked `Querier` | `go test`, table-driven |
| Handler | HTTP handlers with `httptest` and a stubbed service | `go test` |
| WS | Real WS client, ride created via service layer | `go test` + `coder/websocket` test client |
| Integration | End-to-end against `docker compose` | `go test -tags integration` |
| Provider contract | Recorded fixture replay | custom replayer in `internal/routes/google/testdata` |
| Concurrency | 3-cap under parallel joins | `go test -race` with goroutines + barrier |
| Fuzz (optional) | Envelope parser, validators | `go test -fuzz` |

CI runs unit + service + handler + WS + concurrency on every PR; integration + provider contract on `main` and tags.

## 6. Quality Checks

- `golangci-lint run --timeout=5m` — config derived from `coding_standards.json`.
- `gofumpt -d` — formatting check.
- `goimports -d` — import grouping check.
- `go vet ./...`
- `govulncheck ./...`
- `go test -race -count=1 ./...`
- Test coverage gate: ≥ 75% on `internal/`.
- Log redaction smoke test on a synthetic request log.

## 7. Risks & Mitigations
- **Coding standards file missing** — implementation cannot start. *Mitigation: deliver the planning docs first; block code commits until the JSON lands.*
- **Provider two-wheeler unavailability in dev region** — fallback to bicycle. *Mitigation: documented; integration tests cover both modes.*
- **Single-instance WS** — limits scale. *Mitigation: documented; v2 plan calls for Redis pub/sub.*
- **Argon2id CPU cost** — affects login latency. *Mitigation: parameters tuned with benchmarks; document target ~250 ms; consider workload-appropriate downgrade if needed.*
- **Backfilling tests after the fact** — easy to skip. *Mitigation: testing gates per phase, not at the end.*
- **TOON Go ecosystem maturity** — the chosen wire format is recent and Go library support may be limited. *Mitigation: Phase 0 selects and pins a TOON library; if no production-ready library exists, fall back to **CBOR** (`cbor.v1` subprotocol, `application/cbor` over HTTP) using `fxamacker/cbor/v2`. The envelope, message types, and field semantics do not change — only the codec.*
- **Low-bandwidth tier benchmarks** — without a shaped-link test, the 1 kbps target is aspirational. *Mitigation: Phase 7 acceptance includes a `tc netem`-shaped 1 kbps link test sustaining 10 minutes of location-only operation, and a 60 kbps test sustaining full features.*
- **Compression ratio drift** — if real traffic compresses worse than the budget table assumes, tier thresholds may be too lax. *Mitigation: emit `ws_message_bytes_total{direction, codec, encoded}` Prometheus metrics from day one and review after a week of staging traffic.*

## 8. Coding Standards Compliance Checklist (stub)

> The full checklist is generated from `coding_standards.json` once it lands. Until then, the following baseline holds and will be subsumed by the JSON:
- Package paths under `internal/` only; no `pkg/` unless a stable public API is introduced.
- Errors: wrap with `%w`; sentinel errors per package (`var ErrXxx = errors.New(...)`); no panics outside `main`.
- Logging: structured only; no `fmt.Println`/`log.Print` in production code; redactor configured.
- Concurrency: contexts plumbed end-to-end; no `time.Sleep` in production; bounded channels for fan-out.
- HTTP: handlers thin; services testable via interfaces; no SQL in handlers.
- DB: queries authored as `.sql` and code-genned by sqlc; no raw `db.Query` in services except for dynamic cases (review-required).
- Tests: table-driven; fixtures under `testdata/`; integration tests guarded by `//go:build integration`.
- Imports: grouped (std, external, internal) by `goimports`; aliasing only for collisions.
- Naming: `MixedCaps` for exported, `mixedCaps` for unexported, no underscores.
- Comments: only where the why is non-obvious; no doc-comment for trivial helpers.

## 9. Documentation Outputs

By the end of implementation, these are present and current:
- All 9 `docs/*.md` files.
- `README.md` with quick start, env setup, doc links, lint/test/run instructions.
- `postman/RideAndConnect.postman_collection.json` and `postman/RideAndConnect.postman_environment.json` (per POSTMAN_COLLECTION_PLAN).
- `coding_standards.json` (root), authoritative.
- API & WS docs regenerated if any contract change is merged.

## 10. Master Orchestrator Final Review Checklist
- [ ] All endpoints from the brief implemented and documented.
- [ ] All WS message types implemented and documented.
- [ ] All DB tables migrated and code-genned.
- [ ] Security checklist complete (SECURITY §16).
- [ ] No frontend code in repo.
- [ ] No raw GPS / polyline / token / SDP in any log.
- [ ] CI green: lint, test, vuln, build.
- [ ] Postman collection imports cleanly and runs against a local instance.
