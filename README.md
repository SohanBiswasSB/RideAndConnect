# RideAndConnect — Backend

Private 3-rider bike-ride companion backend. Go service exposing **GraphQL** at `POST /api/v1/graphql` plus a custom **WebSocket** at `/api/v1/ws/rides/{rideId}`, backed by PostgreSQL. Integrates **two route providers** behind a single interface — Google Routes API v2 and Mapbox Directions API — switchable via the `ROUTE_PROVIDER` env var. Includes a peer-route distance feature (tap rider B to see actual map-route distance). WebRTC signaling pass-through. Designed for cellular links between **1 and 60 kbps** — TOON wire format on the WS, `permessage-deflate`, strict-priority QoS that always favors location over voice.

See [docs/](docs/) for the full design:

- [PRD](docs/PRD.md)
- [Architecture](docs/ARCHITECTURE.md)
- [API contract](docs/API.md)
- [WebSocket protocol](docs/WEBSOCKET_PROTOCOL.md)
- [Database schema](docs/DATABASE_SCHEMA.md)
- [Route provider](docs/ROUTE_PROVIDER.md)
- [Security](docs/SECURITY.md)
- [Implementation plan](docs/IMPLEMENTATION_PLAN.md)
- [Postman collection plan](docs/POSTMAN_COLLECTION_PLAN.md)

## Prerequisites

- macOS or Linux
- conda env `RideAndConnect` with Go 1.26.2 installed
- Docker (for local PostgreSQL)

## Quick Start

```sh
# 1. Activate the conda env
conda activate RideAndConnect

# 2. Install dev tools (one-time)
make tools

# 3. Bring up Postgres
make db-up

# 4. Apply migrations
make migrate-up

# 5. Copy and edit env
cp .env.example .env
# fill in JWT_SECRET (≥32 bytes) and GOOGLE_ROUTES_API_KEY before running route endpoints

# 6. Run the API
make run
```

The API binds to `:8080` by default. Smoke test:

```sh
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
curl -s http://localhost:8080/version
```

GraphQL (once Phase 3+ resolvers are wired):

```sh
curl -sX POST http://localhost:8080/api/v1/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ __typename }"}'
```

To regenerate GraphQL code after editing `schema/*.graphqls`:

```sh
make tools          # one-time, installs gqlgen
make graphql-gen
```

To swap route providers, change one env var:

```sh
ROUTE_PROVIDER=google   # uses GOOGLE_ROUTES_API_KEY
ROUTE_PROVIDER=mapbox   # uses MAPBOX_ACCESS_TOKEN
```

## Project Layout

```
cmd/api/                  # entrypoint
internal/
  app/                    # composition root
  config/                 # env parsing
  auth/                   # password, JWT, refresh, WS ticket
  users/                  # user profile
  rides/                  # ride lifecycle, members, invite codes
  destination/            # destination set/get + broadcast
  routes/                 # Provider interface + factory + sentinels
    google/               # Google Routes API v2 adapter ("code page 1")
    mapbox/               # Mapbox Directions API adapter ("code page 2")
  graph/                  # gqlgen resolvers + scalar types
  realtime/               # WS hub, room, conn, fanout, presence, QoS, last-known location
  signaling/              # WebRTC signaling validators
  wire/                   # codec (TOON/JSON) + compression
  database/               # pgxpool + sqlc-generated dbq/
  middleware/             # http middleware
  validation/             # shared validators
  logging/                # zerolog setup + redaction
  httpapi/                # chi router, GraphQL HTTP handler, WS upgrade, meta
migrations/               # golang-migrate SQL files
sql/queries/              # sqlc input queries
schema/                   # GraphQL SDL — source of truth for the API
docs/                     # design docs
postman/                  # Postman collection
tests/                    # cross-package integration tests
scripts/                  # dev helpers
```

> Note: per Go convention `internal/httpapi` replaces the `internal/http` from the suggested layout to avoid shadowing the stdlib `net/http` package.

## Make Targets

`make help` lists everything. Common ones:

- `make run` — run the API
- `make test` — `go test -race`
- `make lint` — `golangci-lint run`
- `make migrate-up` / `migrate-down` / `migrate-new NAME=foo`
- `make sqlc` — regenerate `internal/database/dbq`
- `make db-up` / `db-down` — Postgres via docker compose

## Status

| Phase | State |
|---|---|
| 0 Bootstrap | ✅ done |
| 1 Migrations (12 SQL files) | ✅ done |
| 2 Config / logging / middleware | ✅ done |
| 3 Auth (argon2id + JWT + refresh + ws-ticket) | ✅ done |
| 4 Rides (lifecycle + 3-cap + pair/detach + invitations + emergency) | ✅ done |
| 5 Destination (set/get + cache invalidation + WS broadcast) | ✅ done |
| 6 Routes (Google + Mapbox; cache + throttle + breaker; computeRoute + peerRoute + myRoute) | ✅ done |
| 7 WebSocket realtime (JSON codec, location + voice signaling, presence, heartbeat, emergency push) | ✅ done core; TOON / permessage-deflate / strict-priority scheduler / bandwidth tiers staged for follow-up |
| 8 WebRTC signaling validators | 🟡 schema + size guards in place; WebRTC-specific advisories (`network.advisory`) staged |
| 9 Observability (Prometheus `/metrics`) | ✅ basic |
| 10 Tests (auth, rides, geohash, identifier classification, sanitizer) | ✅ critical paths; broader integration matrix staged |
| 11 DevOps (Dockerfile, compose, GitHub Actions) | ✅ done |
