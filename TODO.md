# TODO

Deferred improvements from the Tier 2/3/4 review pass. Each is intentionally
unscheduled here — they all require either coordinated client work, a DB
migration, or threat-model/design discussion that doesn't fit a one-shot edit.

When picking one up, move the item to a feature branch and delete the entry
from this file in the same PR.

---

## #5 Delta encoding for location broadcasts

**Why:** Every `location.broadcast` ships full lat/lng as float64 (~16 B). After
the first frame, sending `Δlat, Δlng` as scaled int16 saves ~12 B per frame.
At 60 broadcasts/min × 3 peers, that's a real win on a 1 kbps link.

**Why deferred:** This is a wire-protocol change. Server can encode deltas, but
the mobile client must decode them — landing the server side alone yields zero
savings and forces an asymmetric upgrade. Treat as a coordinated bump to a new
subprotocol (`rideandconnect.cbor.v2`) so old clients keep working unchanged.

**Notes when picking up:**
- New `LocationDeltaPayload` type with `dlat_e5`, `dlng_e5` int16 fields.
- Server-side state per connection: last full position broadcast.
- Send a full `LocationPayload` every N frames (e.g. every 30) as a keyframe
  so reconnecting clients can resync without waiting.
- Negotiate via subprotocol; do not switch silently.

---

## #6 Server-side coalescing window for inbound location updates

**Why:** When a buggy or aggressive client sends two updates within 200 ms,
the server currently broadcasts both. Coalescing to last-write-wins inside a
200 ms window saves outbound bytes to all peers.

**Why deferred:** The tier-aware token bucket already drops 4 of every 5
updates at critical tier (`inboundLocationRateForTier`). Adding a coalescing
state machine on top doubles per-connection state for marginal incremental
savings. Revisit if telemetry shows real over-rate floods in production.

**Notes when picking up:**
- Per-connection `pendingLoc atomic.Pointer[Envelope]` + a 200 ms timer.
- On inbound: if `time.Since(lastBroadcastAt) < 200ms`, store as pending and
  arm the timer; otherwise broadcast immediately.
- Timer fires → broadcast pending, clear.
- Add a `ws_location_coalesced_total` counter to measure actual savings.

---

## #8 `ride.snapshot` recovery message on reconnect with seq gap

**Why:** When a client reconnects mid-ride and the server's `currentSeq` is
ahead of the client's last-seen seq, the client just resumes live. A snapshot
frame containing current presence + last-known position of every peer would
let the client catch up in one frame instead of waiting for the next
broadcast cycle (which can be 30 s on a slow connection).

**Why deferred:** New message type that needs client-side gap-detection logic
to be useful. Best packaged with #5 in the same protocol bump.

**Notes when picking up:**
- New `TypeRideSnapshot = "ride.snapshot"`, payload is
  `{peers: []{user_id, name, last_loc, paired_with}}`.
- `Connection.sendHello` already exposes `CurrentSeq`. Client sends a
  hypothetical `snapshot.request` (or server pushes proactively when
  `client_last_seq` cookie is on the upgrade URL).
- Sent on P0 so it preempts any backlog.

---

## #11 Refresh token reuse detection with family revocation cascade

**Why:** Standard hardening. If an already-consumed refresh token is presented
again, that's a stolen-token signal — the entire refresh-token family
(descended from the original login) should be revoked. Today we just reject
the reused token; the attacker's freshly-rotated token is still valid.

**Why deferred:** Needs a DB migration (token family table or `family_id`
column with parent chain) + a revocation cascade. Belongs in a focused
security PR with its own threat-model writeup, not bundled with feature work.

**Notes when picking up:**
- Add `family_id uuid` to `refresh_tokens`; new family on login, inherited on
  rotate.
- On rotate: if the presented token's `consumed_at IS NOT NULL`, mark every
  row with the same `family_id` as revoked.
- Surface as a `SECURITY_TOKEN_REUSE` audit event so we can alert on it.

---

## Test coverage gaps (low priority, do whenever)

These three were called out in the original lean-pass review as "claimed but
not measured." They're not bugs — the code paths work — but the tests assert
on math or in-process state rather than the actual wire / pipeline behavior.

**Deflate-effectiveness test.** Today we assume permessage-deflate cuts
~30 % off the wire because the negotiation succeeds. A real test would
capture raw socket bytes (via a `net.Listen` proxy in front of the
httptest server) and compare a 100-frame stream against an uncompressed
control. Confirms the dictionary stays warm across frames as documented
in `docs/WEBSOCKET_PROTOCOL.md`.

**RTT → tier-change end-to-end pipeline.** `maybeChangeTier` is
unit-tested in `lean_e2e_test.go` by calling it directly with a
duration. The full path — server emits ping, client pongs after delay,
`recordPongRTT` updates EWMA, tier flips, `emitAdvisory` queues on P0,
metric `ws_tier_transitions_total` increments — has no test. Use the
existing httptest harness with a controlled-delay pong loop.

**Rename `TestLean_TierClassification`.** It's currently a math-only
table test on `maybeChangeTier`'s switch. Either rename to
`TestMaybeChangeTier_RTTBuckets` to reflect what it actually tests, or
replace with the pipeline test above and delete this one.

---

## #15 GraphQL `auditEvents` query gated to ride members + retention policy

**Why:** Audit events are write-only today — the prune sweeper is the only
reader. If we ever need "show me last 7 days of activity for ride X" (incident
review, dispute resolution), there's no read path.

**Why deferred:** Needs an authorization design (only ride members can read
their ride's audit; never another ride's), a retention policy
(7 / 30 / 90 days?), and pagination. Not a one-shot change.

**Notes when picking up:**
- New `query auditEvents(rideId: UUID!, after: DateTime, limit: Int)` field.
- Resolver gate: `ensureMemberPool(ctx, rideID, callerID)`.
- Retention: extend `database/prune.go` with a configurable `AUDIT_RETENTION_DAYS`.
- Don't expose `metadata.ip` or other sensitive fields without a redaction step.
