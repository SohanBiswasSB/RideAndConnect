# Coding Standards — RideAndConnect Backend

> Authoritative for this codebase. Subsumes the baseline stub in `docs/IMPLEMENTATION_PLAN.md §8`. CI lint config (`.golangci.yml`) is derived from these rules.
> Target toolchain: Go 1.26.2 inside conda env `RideAndConnect`.

## 1. Package layout

- All application code lives under `internal/`. No `pkg/` until we deliberately publish a stable API.
- One concept per package. Package names are short, lowercase, no underscores or camelCase.
- Domain packages are nouns (`auth`, `rides`, `routes`, `signaling`). Subpackages narrow scope (`routes/google`, `routes/mapbox`).
- The composition root lives in `internal/app`. Concrete dependencies are wired there and injected via interfaces into domain packages.
- Do not name a package `http`, `log`, `context`, or any other stdlib package. Prefer `httpapi`, `logging`, `appcontext`.

## 2. Naming

- Exported identifiers: `MixedCaps`. Unexported: `mixedCaps`. Never underscores.
- Acronyms keep case: `UserID`, `HTTPClient`, `URL`, `JWT`, `JSON`. Don't write `UserId` or `Url`.
- Receivers: short, consistent across the type. `func (p *Provider) ...`, never `func (this *Provider) ...`.
- Constructors: `New<Type>` for the canonical case; descriptive names for variants (`NewWithConfig`, `NewMock`).
- Errors: `var ErrXxx = errors.New("pkg: ...")` at package scope, message lowercase, prefixed with the package name.
- Test files: `*_test.go`. Test functions: `TestXxx`. Tabular subtests via `t.Run(name, ...)`.

## 3. Errors

- Wrap with `fmt.Errorf("...: %w", err)`. Never strip context with `fmt.Errorf("%v", err)` unless deliberately hiding internal details.
- Discriminate with `errors.Is` / `errors.As`. Don't compare error strings.
- One sentinel set per package, defined together at the top of `errors.go`. Sentinels start with `Err`.
- Public APIs document which sentinels they may return. Resolvers map domain sentinels to GraphQL `extensions.code` values from `docs/API.md §1.3`.
- Never `panic` outside `main`, package init, or `must*` test helpers. Recover boundaries are explicit middleware/Hub goroutines.
- An ignored error must be assigned to `_` with a brief one-line `//` justification, OR linted with a `//nolint:errcheck` comment that names the rule.

## 4. Logging

- Single logger constructed in `cmd/api/main.go` via `internal/logging`.
- All log calls go through `*zerolog.Logger`. No `fmt.Println`, no `log.Print*` in production code (allowed in `*_test.go` and `cmd/` bootstrapping before the logger exists).
- Required fields on every request log: `request_id`, `method`, `path`, `status`, `latency_ms`, `user_id` (if authed), `ride_id` (if applicable).
- **Forbidden in production logs** (enforced by `internal/logging.Sanitize` and tested):
  `latitude`, `longitude`, `lat`, `lng`, `coords`, `coordinates`, `encoded_polyline`, `polyline`, `password`, `password_hash`, `token`, `refresh_token`, `access_token`, `authorization`, `sdp`, `candidate`, `mapbox`, `x_goog_api_key`, `mapbox_access_token`.
- Email and phone are masked (`a***@example.com`) before logging.
- Provider raw response bodies: digest only (`sha256[:8]`); never the body.
- Levels: `debug` for inner-loop tracing, `info` for state transitions and request lines, `warn` for recoverable anomalies, `error` for actionable failures, `fatal` only at startup.

## 5. Concurrency

- Plumb `context.Context` end-to-end as the **first** argument of any I/O, DB, or third-party call.
- No `time.Sleep` in production code paths. For backoff use `time.NewTimer` + `select` with the context.
- Goroutines are owned. Each spawn pairs with a deterministic shutdown path (channel close, context cancel, or `sync.WaitGroup`).
- Channels: bounded for fan-out. Slow consumers are dropped, never blocked.
- No package-level mutable state except for sentinel errors and immutable config.

## 6. HTTP / GraphQL

- HTTP handlers are thin: parse → validate → call service → encode response. No SQL or external HTTP in handlers.
- GraphQL resolvers follow the same rule. Authorization checks (`requireRideMember`, `requireRideOwner`) run before any service call.
- Always set `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` on `*http.Server`.
- Reject unknown JSON fields (`json.Decoder.DisallowUnknownFields`) on REST/Graph inputs.
- Cap request body size at the middleware layer (default 64 KiB).
- Errors returned to clients are sanitized: code + safe message + request_id only. Internal details land in logs, not responses.

## 7. Database

- All queries authored as `.sql` files under `sql/queries/` and code-genned via `sqlc` into `internal/database/dbq/`. Hand-rolled queries in services require a review note.
- Use `pgx/v5` + `pgxpool`. No `database/sql` driver.
- Transactions wrap any multi-statement invariant (3-member cap, refresh rotation, ownership transfer, ride end + invite invalidation).
- Pass `*pgxpool.Pool` or `dbq.Querier` interfaces into services; never the concrete `*pgxpool.Conn`.
- Migrations are forward-only and applied at deploy, not at boot.

## 8. Testing

- Table-driven tests for any non-trivial logic. Subtests via `t.Run(name, func)`.
- Test fixtures under `testdata/`. Recorded provider responses live in `internal/routes/<name>/testdata/`.
- Integration tests guarded by `//go:build integration` and run via `make test-integration`.
- Concurrency-sensitive code (3-cap, hub fanout) has a test that exercises parallelism with `-race`.
- Test helpers named `mustXxx`/`newTestXxx`. They take `t *testing.T` and call `t.Helper()`.
- Coverage gate: `≥ 75%` on `internal/`. Generated code excluded.

## 9. Imports

- Three groups separated by blank lines, ordered by `goimports`:
  1. stdlib
  2. third-party
  3. `github.com/sohanbiswas/rideandconnect/...`
- Aliasing only on collision (`pgxv5 "github.com/jackc/pgx/v5"` is unnecessary; `pgx` is fine).
- No dot-imports outside `_test.go` (and rarely there).

## 10. Comments

- Default to no comments. The why is non-obvious — write a comment. The what — let the code say it.
- Package doc comment lives in `doc.go` (or the lead file). Two sentences max for the summary, then bullet the public surface.
- Public types and funcs have a single-sentence doc starting with the identifier.
- TODO/FIXME include an owner: `// TODO(sohan): ...`. Bare TODO is a lint warning.

## 11. Security

- Secrets only via env or secret manager. Never in code, comments, or tests (use `t.Setenv`).
- Required prod secrets are validated in `internal/config.Validate`; the app refuses to start if any are missing or weak.
- Each external HTTP client wraps a `Transport` that strips its auth header/query from error context.
- Inputs from the network are validated at the boundary (handler/resolver), not deep in the call stack.
- Treat database query parameters as untrusted; never interpolate into SQL.

## 12. Style nits enforced by lint

- `gofumpt` formatting (stricter than `gofmt`).
- `goimports` import ordering.
- `golangci-lint` runs `errcheck`, `errorlint`, `gosec`, `govet`, `ineffassign`, `revive`, `staticcheck`, `unconvert`, `unparam`, `unused`, `bodyclose`, `contextcheck`, `nilerr`, `prealloc`, `rowserrcheck`, `sqlclosecheck`, `copyloopvar`.
- CI fails on any lint warning. New rules added with a follow-up PR plus a mass auto-fix.

## 13. GraphQL

- SDL lives in `schema/schema.graphqls` and is mirrored to `internal/graph/schema.graphqls` for `//go:embed`. `make schema-sync` keeps them aligned.
- Library: `github.com/graph-gophers/graphql-go` (reflection-based, no codegen).
- Resolver method names match GraphQL field names in UpperCamelCase.
- Each resolver method takes `ctx context.Context` and returns `(<*Type>, error)`. Mutations that return `Boolean!` return `bool`.
- Domain sentinels are mapped to `extensions.code` strings in `internal/graph/resolver.go::mapDomainErr`. Add new mappings there when adding new sentinels.
- Custom scalar types (`UUID`, `DateTime`) wrap stdlib types in `internal/graph/scalars.go`. Don't decode them ad-hoc in resolvers.
- Authorization: every resolver that requires auth begins with `requireUser(ctx)`. Membership/role checks happen inside the domain service (`rides.Service`), not the resolver.

## 14. PR / change discipline

- One concern per change. A "bug fix + cleanup" is two PRs.
- A change that affects `docs/API.md`, `docs/WEBSOCKET_PROTOCOL.md`, or `docs/DATABASE_SCHEMA.md` updates the doc in the same change.
- Never `--no-verify` past a hook unless explicitly requested.
- Migrations are additive once merged. New requirement → new migration.
