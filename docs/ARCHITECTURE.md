# Backend Architecture — RideAndConnect

> Companion to PRD, API, WEBSOCKET_PROTOCOL, DATABASE_SCHEMA, ROUTE_PROVIDER, SECURITY, IMPLEMENTATION_PLAN.
> Coding Standards: must comply with `coding_standards.json` (root).

## 1. Scope
A single Go service exposing:
- **GraphQL** at `POST /api/v1/graphql` (queries and mutations).
- **WebSocket** at `GET /api/v1/ws/rides/{rideId}` for realtime location and WebRTC signaling (TOON + QoS at 1–60 kbps — see WEBSOCKET_PROTOCOL.md).
- Operational HTTP at `/healthz`, `/readyz`, `/version`.

Backed by PostgreSQL. Integrates with **two route providers** behind a single Go interface — Google Routes API v2 and Mapbox Directions API; the active one is chosen by the `ROUTE_PROVIDER` env var. Optimized for tens to low-thousands of concurrent rides on a single regional instance.

The backend does NOT render maps, does NOT relay voice audio, and does NOT contain frontend or UI logic.

## 2. High-Level Diagram

```
                       ┌──────────────────────────┐
   Mobile/Web client ─►│   HTTPS  (REST + WS)     │
                       │   Go API service         │
                       │  ┌────────────────────┐  │
                       │  │ chi router         │  │
                       │  │ middleware chain   │  │
                       │  ├────────────────────┤  │
                       │  │ HTTP handlers      │  │
                       │  ├────────────────────┤  │
                       │  │ WS hub + rooms     │◄─┼─── in-process room manager
                       │  ├────────────────────┤  │
                       │  │ services           │  │      ┌─────────────────────┐
                       │  │  auth/users        │  │      │ Google Routes API   │
                       │  │  rides/destination │  │      │ v2 computeRoutes    │
                       │  │  routes (provider) │◄─┼──────┤                     │
                       │  │  realtime          │  │      └─────────────────────┘
                       │  │  signaling         │  │      ┌─────────────────────┐
                       │  ├────────────────────┤  │      │ PostgreSQL 16+      │
                       │  │ pgx pool           │◄─┼──────┤ pgxpool, sqlc       │
                       │  └────────────────────┘  │      └─────────────────────┘
                       └──────────────────────────┘
                                        │
                                        ▼
                               (optional v2) Redis pub/sub
```

## 3. Service Components

### 3.1 HTTP layer (`internal/httpapi`, `internal/middleware`)
- Router: `go-chi/chi/v5` (mounts the GraphQL handler at `POST /api/v1/graphql`, the WS upgrade at `/api/v1/ws/rides/{rideId}`, and the meta endpoints).
- Middleware (in order): request ID → recoverer → CORS allowlist → structured logger → request-size cap → rate limit → JSON content-type guard → JWT auth (selective) → ride membership (selective, applied inside GraphQL resolvers via context helper).
- The GraphQL HTTP handler (gqlgen-generated) sits inside `internal/httpapi` and dispatches to resolvers in `internal/graph`.

### 3.1.1 GraphQL layer (`internal/graph`)
- Library: `github.com/graph-gophers/graphql-go` (schema-first, reflection-based — no codegen step). The SDL is `schema/schema.graphqls` (developer-facing) and is mirrored into `internal/graph/schema.graphqls` for `//go:embed`. `make schema-sync` keeps them in sync.
- Hardening: introspection auto-disabled in `prod` (`GRAPHQL_INTROSPECTION=auto`), `MaxDepth(8)`, query operation allowlist (no subscriptions — realtime lives on the custom WS).
- Resolvers are thin: they unmarshal inputs, validate via `internal/validation`, dispatch to domain services (`internal/auth`, `internal/users`, `internal/rides`, `internal/routes`), and map domain sentinels to GraphQL `extensions.code` values via `mapDomainErr`.
- Custom scalars `UUID` and `DateTime` live in `internal/graph/scalars.go` and implement `ImplementsGraphQLType` / `UnmarshalGraphQL` / `MarshalJSON`.

### 3.2 WebSocket layer (`internal/realtime`)
- Library: `github.com/coder/websocket` (context-aware, idiomatic).
- One **Hub** with a map `ride_id → *Room`.
- Each **Room** owns its connected members, a bounded fan-out goroutine, and an idle TTL (room torn down after 30 s empty or when ride is ended).
- Each **Connection** has its own read pump and write pump goroutines coordinated by `context.Context`.
- One active connection per `(user_id, ride_id)`. A new connection from the same pair supersedes the old one (`4009 SUPERSEDED`).
- Per-connection bounded send channel (cap 64). Slow consumers are dropped (`4008 SLOW_CONSUMER`).

### 3.3 Auth (`internal/auth`)
- JWT access tokens (15 min) signed HS256 from env secret.
- Opaque refresh tokens (30 days) — stored hashed (SHA-256) in `refresh_tokens`, single-use rotated on refresh.
- WS-ticket: short-lived (5 min, single use, scope=`ws`) issued via `POST /api/v1/auth/ws-ticket` for browser-based WS clients that cannot set headers.
- Password hashing: argon2id (`golang.org/x/crypto/argon2`), tuned for ~250 ms server CPU.

### 3.4 Domain services
- `internal/users` — user profile reads.
- `internal/rides` — ride creation, invite-code lifecycle, member admission with transactional cap check, ownership transfer on owner-leave.
- `internal/destination` — set/get destination, fan-out via `internal/realtime`.
- `internal/routes` — provider abstraction, cache, throttle, fan-out via `internal/realtime`.
- `internal/realtime` — Hub, Room, Connection, fan-out, online/offline tracking, heartbeat, **strict-priority send scheduler**, **per-connection bandwidth tier (`high`/`medium`/`low`/`critical`)**, RTT EWMA from heartbeats, and the in-memory **last-known-location store** consumed by the `peerRoute` GraphQL resolver.
- `internal/signaling` — WebRTC signaling validators and authorization. Voice signaling is **deprioritized below location** in the per-connection scheduler and is shed entirely in `critical` tier.
- `internal/wire` — codec abstraction (TOON default, JSON fallback, optional CBOR), `permessage-deflate` configuration, and the field-name shortener for the location hot-path payload.
- `internal/validation` — shared request/payload validators (lat/lng bounds, invite code regex, etc.).

### 3.5 Persistence (`internal/database`)
- Driver: `jackc/pgx/v5` with `pgxpool`.
- Query layer: `sqlc` for type-safe Go from `sql/queries/*.sql`.
- Migrations: `golang-migrate/migrate/v4`. Migrations are applied at deploy time, never at boot in production.
- Transactional invariants: 3-member cap, ownership transfer, refresh-token rotation, ride end → invite-code invalidation.

### 3.6 Route provider (`internal/routes`)
- `routes.Provider` interface lives in `internal/routes/provider.go`.
- Two adapters ship in v1, both behind the same interface:
  - `internal/routes/google/` — Google Routes API v2 `computeRoutes` (key from `GOOGLE_ROUTES_API_KEY`).
  - `internal/routes/mapbox/` — Mapbox Directions API v5 (token from `MAPBOX_ACCESS_TOKEN`).
- Active provider is chosen at startup from `ROUTE_PROVIDER` (`google` | `mapbox`) via `internal/routes/factory.go`. Both packages stay imported so swap is a config change.
- Provider keys/tokens are server-side only; never returned to clients. The HTTP transport for each adapter strips the auth token from any error/log context.
- Cache (PostgreSQL `ride_routes`) supports two route kinds: **destination** (TTL 30 min) and **peer** (TTL 60 s).
- Throttle: token bucket per ride/user for destination route; per (caller, peer) pair plus per-ride for peer route. Circuit breaker (`sony/gobreaker`) per provider.
- Two-wheeler handling: Google maps to `TWO_WHEELER` (region-permitting, else `BICYCLE`); Mapbox maps via env profile (`MAPBOX_PROFILE_TWO_WHEELER`, default `cycling`).
- Peer-route distance feature (`peerRoute` GraphQL query) reuses this layer with origin = caller's last-known location and destination = peer's last-known location; staleness guarded by `PEER_ROUTE_STALE_TTL`.

### 3.7 Logging & telemetry (`internal/logging`)
- Logger: `rs/zerolog`, JSON output.
- Field-allowlist redactor strips: `latitude`, `longitude`, `lat`, `lng`, `coords`, `coordinates`, `encoded_polyline`, `polyline`, `sdp`, `candidate`, `password`, `password_hash`, `token`, `refresh_token`, `access_token`, `authorization`.
- Metrics: `/metrics` Prometheus endpoint (auth-protected in non-prod).
- Tracing: OpenTelemetry SDK plumbed; exporter optional (default no-op).

## 4. Project Layout

```
ride-backend/
├── cmd/
│   └── api/
│       └── main.go
├── internal/
│   ├── config/             # env parsing, validation
│   ├── auth/               # password, JWT, refresh, WS ticket
│   ├── users/              # user profile
│   ├── rides/              # ride lifecycle, members, invite codes
│   ├── destination/        # destination set/get + broadcast
│   ├── routes/             # provider iface + factory
│   │   ├── google/         # Google Routes API v2 adapter
│   │   └── mapbox/         # Mapbox Directions API adapter
│   ├── graph/              # gqlgen resolvers + generated code
│   ├── httpapi/            # chi router, GraphQL HTTP handler, WS upgrade, meta
│   ├── realtime/           # WS hub, room, connection, fanout, presence, QoS scheduler, tiers
│   ├── signaling/          # WebRTC signaling validators
│   ├── wire/               # codec (TOON default, JSON fallback) + compression
│   ├── database/           # pgxpool, sqlc-generated queries, migrations runner
│   ├── middleware/         # http middleware (auth, ratelimit, etc.)
│   ├── validation/         # shared validators
│   └── logging/            # zerolog setup, redaction
├── migrations/             # golang-migrate SQL files
├── sql/                    # sqlc queries
├── schema/                 # GraphQL schema (.graphqls) — gqlgen input
├── docs/                   # this directory
├── tests/                  # cross-package integration tests, fixtures
├── postman/                # Postman collection + environment
├── scripts/                # dev helpers (run, lint, migrate)
├── .env.example
├── Dockerfile
├── docker-compose.yml
├── go.mod
├── go.sum
├── README.md
└── coding_standards.json
```

This matches the suggested project structure in the requirements verbatim.

## 5. Toolchain
- **Go 1.26.2** inside conda env `RideAndConnect`. All build/test/lint commands assume `conda activate RideAndConnect`.
- Linters: `golangci-lint` (config `.golangci.yml`), `gofumpt`, `goimports`.
- SQL: `sqlc` for code generation, `sqlfluff` (optional) for SQL lint.
- Test runner: `go test -race ./...` in CI.
- Container: multi-stage `Dockerfile` producing a minimal distroless image.

## 6. Configuration (env)

| Var | Purpose | Default |
|---|---|---|
| `APP_ENV` | `dev`/`staging`/`prod` | `dev` |
| `HTTP_ADDR` | bind for HTTP server | `:8080` |
| `DATABASE_URL` | PostgreSQL DSN | — |
| `JWT_SECRET` | HS256 secret | — |
| `JWT_ACCESS_TTL` | access token TTL | `15m` |
| `JWT_REFRESH_TTL` | refresh token TTL | `720h` |
| `WS_TICKET_TTL` | WS ticket TTL | `5m` |
| `ROUTE_PROVIDER` | `google` \| `mapbox` | `google` |
| `GOOGLE_ROUTES_API_KEY` | Google Routes API key | — |
| `MAPBOX_ACCESS_TOKEN` | Mapbox access token | — |
| `MAPBOX_BASE_URL` | optional override of Mapbox base URL | `https://api.mapbox.com` |
| `MAPBOX_PROFILE_TWO_WHEELER` | profile for `TWO_WHEELER` | `cycling` |
| `MAPBOX_PROFILE_BICYCLE` | profile for `BICYCLE` | `cycling` |
| `MAPBOX_PROFILE_WALK` | profile for `WALK` | `walking` |
| `ROUTE_CACHE_TTL` | destination route cache TTL | `30m` |
| `ROUTE_RATE_USER` | per-user rps for `computeRoute` | `1 / 30s` |
| `ROUTE_RATE_RIDE` | per-ride rps for `computeRoute` | `6 / 10m` |
| `PEER_ROUTE_CACHE_TTL` | peer route cache TTL | `60s` |
| `PEER_ROUTE_STALE_TTL` | reject `peerRoute` if either side's last-known location is older than this | `30s` |
| `PEER_ROUTE_RATE_PAIR` | per `(caller, peer)` ordered pair limit | `1 / 30s` |
| `PEER_ROUTE_RATE_RIDE` | per-ride limit for peer routes | `12 / 10m` |
| `GRAPHQL_DEPTH_LIMIT` | max GraphQL query depth | `8` |
| `GRAPHQL_COMPLEXITY_LIMIT` | max GraphQL query complexity score | `200` |
| `GRAPHQL_INTROSPECTION` | allow introspection (auto-disabled in `prod`) | `auto` |
| `WS_PING_INTERVAL` | server ping interval (high/medium tier) | `25s` |
| `WS_PING_INTERVAL_LOW` | server ping interval (low tier) | `30s` |
| `WS_PING_INTERVAL_CRITICAL` | server ping interval (critical tier) | `60s` |
| `WS_IDLE_TIMEOUT` | close idle connection after | `60s` |
| `WS_MAX_MSG_SIZE` | max **decoded** WS message bytes | `8192` |
| `WS_MAX_WIRE_BYTES` | max compressed WS frame bytes | `16384` |
| `WS_DEFAULT_CODEC` | `toon` or `json` | `toon` |
| `WS_PERMESSAGE_DEFLATE` | enable RFC 7692 compression | `true` |
| `WS_TIER_RTT_HIGH_MS` | RTT p95 cutoff for `high` | `250` |
| `WS_TIER_RTT_LOW_MS` | RTT p95 cutoff above which tier drops below `medium` | `600` |
| `WS_TIER_RTT_CRITICAL_MS` | RTT p95 cutoff for `critical` | `1500` |
| `WS_LOC_HZ_HIGH` | inbound `location.update` cap, high | `2` |
| `WS_LOC_HZ_MEDIUM` | inbound `location.update` cap, medium | `1` |
| `WS_LOC_HZ_LOW` | inbound `location.update` cap, low | `0.5` |
| `WS_LOC_HZ_CRITICAL` | inbound `location.update` cap, critical | `0.2` |
| `HTTP_COMPRESSION` | `gzip,br` accept/encode list | `gzip,br` |
| `LOG_LEVEL` | `debug`/`info`/`warn`/`error` | `info` |
| `CORS_ALLOWED_ORIGINS` | comma-separated allowlist | `` |
| `RATE_LIMIT_RPS` | global rps per IP | `20` |
| `LOCATION_PERSIST` | toggle location history persistence | `false` |

The application refuses to start if required vars are missing or malformed when `APP_ENV=prod`.

## 7. Concurrency Model
- HTTP requests: standard `net/http` server with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` set explicitly.
- WS: per connection, two goroutines (read pump, write pump) coordinated via `ctx.WithCancel`.
- Hub: one fan-out goroutine per room with a bounded `chan envelope`.
- Cross-component dependencies wired in `internal/app` (composition root) using interfaces — keeps everything mockable.

## 8. Failure Modes
| Failure | Effect | Mitigation |
|---|---|---|
| DB outage | HTTP 503 on persistence-bound endpoints | `/readyz` flips, restarts behind LB |
| Route provider outage | Route HTTP 503 + `Retry-After` | Cache continues serving until TTL; circuit breaker opens |
| WS overload | New WS upgrades rejected with `4013 ROOM_FULL` or `4004 RATE_LIMITED` | Bounded queues, per-IP rate limit |
| Crash | In-flight WS dropped | State rebuilt from DB on restart; clients reconnect |
| Bad client clock | Timestamps off | Server stamps authoritative `timestamp` on broadcast |

## 9. Deployment Topology (v1)
- Single VM or container behind a TLS-terminating reverse proxy (Caddy or NGINX) terminating HTTPS/WSS.
- PostgreSQL on managed service (RDS, Supabase, or Neon) with daily snapshots.
- Vertical scale first; horizontal scale would require sticky sessions or Redis pub/sub backing the Hub (deferred to v2).

## 10. Development Workflow
1. `conda activate RideAndConnect`
2. `cp .env.example .env` and fill values
3. `docker compose up -d postgres`
4. `make migrate-up` — apply migrations
5. `make sqlc` — regen queries
6. `make run` — start API
7. `make test` / `make lint`

A `Makefile` is part of the implementation plan deliverables.

## 11. Coding Standards
All Go code must comply with `coding_standards.json` at repo root. Compliance is enforced in CI via `golangci-lint` configuration derived from the JSON. See IMPLEMENTATION_PLAN §10 for the compliance checklist.
