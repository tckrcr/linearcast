# Resilience Contract

Phase C tracks normal operational failures: unavailable dependencies, slow
upstreams, interrupted jobs, missing artifacts, and temporary local resource
errors. The goal is deterministic degradation and recovery, not random chaos
experiments.

## Failure Matrix

| Surface | Failure trigger | Expected response | State effect | Recovery path |
|---|---|---|---|---|
| External HLS | Upstream timeout or connection failure | Playback route returns `502`, then `503` with `Retry-After` while cooling down | Live-proxy failure keyed by channel/upstream | Next request after cooldown retries upstream and clears failure on success |
| External HLS | Upstream `4xx`/`5xx` or missing segment | Original upstream status is returned on first failure, then cooldown returns `503` | Live-proxy failure keyed by channel/upstream | Upstream serving a `2xx` response clears cooldown |
| External HLS | Malformed playlist body | Recognized URIs are rewritten to proxy paths; unparseable lines pass through unchanged, so the client (not linearcast) rejects a truly broken playlist. A non-2xx body is surfaced as upstream status; a 2xx non-manifest body is served as-is | No package or schedule writes | Operator fixes upstream; status failures cool down and clear on a healthy 2xx |
| External HLS | Upstream URL (or redirect target) resolves to a blocked address | Dial-time SSRF guard refuses the connection; route fails visibly and is not retried as transient | No package or schedule writes | Operator points the channel at an allowed upstream |
| On-demand | ffmpeg spawn failure | Playback returns warming/error response with retry guidance, not a tight loop | Entry restart budget/cooldown may be updated | Dependency/config fixed; entry retries after cooldown |
| On-demand | ffmpeg exits or stalls mid-encoding | Channel encoding is stopped/restarted within budget; over-budget returns `503`/`Retry-After` | Encoding restart count and blocked-until state | Cooldown expiry allows a fresh encoding attempt |
| On-demand | Capacity exhausted | New tune-in returns `503`/`Retry-After`; existing encodings continue | No new encoding admitted | Idle encodings evict or operator raises capacity |
| Packaged playback | Ready init/segment artifact missing | Request fails visibly and package is marked `pending` for repair | Narrow playback repair path writes package state only | Packager rebuilds package; later requests serve ready artifacts |
| Packaged playback | Repair write fails because DB is locked/unavailable | Request fails visibly; package may remain `ready` until retry | No partial schedule writes | Later request retries repair when DB is writable |
| Encoder transport | Worker loses heartbeat or dies | Job becomes claimable after stale lease window | Package remains transient/in-progress until reclaimed | Same or another worker reclaims and completes |
| Encoder transport | Upload interrupted or tar invalid | Job fails transiently or terminally according to package error classification | Partial package artifacts are not promoted to ready; invalid tar uploads never replace an existing package root | Retry uses a clean package attempt unless error is terminal |
| Encoder transport | Remote complete upload is received, but finalization or DB completion fails | Complete route returns an error and leaves the package out of `ready` | Uploaded package directory and any unpromoted segment rows are removed best-effort; if the failure also left a processing row with no active lease, it is requeued as transient | Encoder reports failure or lease expiry requeues the job; retry starts from a clean upload |
| SQLite/disk | Temporary lock, read-only path, or full-like write failure | Caller returns actionable error; unrelated reads/writes should not corrupt state | Transaction rolls back or no state is promoted | Operator fixes local resource; next operation retries normally |

## Upstream-URL Trust (SSRF)

The shared live-proxy fetch client enforces an adapter-scoped IP policy at dial
time (`internal/liveproxy/dialer.go`):

- **External HLS** (`a.externalHLSClient`) uses `DenyPrivateNetworks`: a
  scheduler-supplied upstream URL must not reach loopback, link-local (including
  the `169.254.169.254` cloud-metadata endpoint), private, unique-local,
  multicast, unspecified, or other non-global-unicast addresses.

Enforcement is on the **resolved IP** via the dialer's `Control` hook, so it
also covers each redirect hop (`http.Client.Do` dials through the same
transport) and defeats DNS rebinding: the policy sees the address actually
being connected to, not whatever the configured URL parsed to. A blocked address
is **terminal**: the route returns `502` and logs `live_proxy upstream blocked`,
without arming the transient cooldown (a forbidden address never "recovers", so
a `503` cooldown would only hide the cause). The policy is deliberately *not* a
single global dialer; keeping it adapter-scoped is what lets each adapter apply
the IP policy appropriate to its upstream.

## Structured Logging

All output is JSON via `log/slog` with the standard `time`, `level`, `msg` fields.
Phase C failure paths use the key names documented below as additional JSON fields
for Loki `| json` parsing. Do not invent per-call-site variants (`channel` vs
`channel_id`).

| Field | Meaning | Emitted by |
|---|---|---|
| `entry_id` | Affected schedule entry | on-demand |
| `upstream` | Sanitized upstream URL/host (no query/token) | live-proxy |
| `err` | Error string | all failure paths |
| `cooldown_until` | RFC3339 time the transient cooldown lifts | live-proxy |
| `stage` | Failure stage within a multi-step operation | encoder completion cleanup |
| `package_id` | Affected package (repair requeue) | packaged repair |
| `fields` | Extra comma-separated key=value pairs from caller (legacy) | live-proxy |

Legacy unconverted `log.Printf` calls are captured by a bridge adapter that wraps
the full text in a single `msg` field. As each call site is migrated to
structured `slog.Info/Warn/Error`, its fields improve from one flat string to
proper JSON keys.

The shared live-proxy failure line is rate-limited (`LogSampleEach`, default 60s)
with a `suppressed` counter so a hard-down upstream cannot flood the log; the
SSRF `blocked` line is not rate-limited because each occurrence is a distinct
terminal (mis)configuration worth seeing.

An HTTP request-log middleware runs on both servers, emitting one JSON line per
request with `method`, `path`, `status`, and `duration_ms`.

## Automation Strategy

Default automation should be deterministic and safe to run repeatedly:

1. Use fake upstream servers for CI-safe failures such as timeout, `500`, missing
   segment, malformed manifest, and recovery after cooldown.
2. Use focused Go tests for state-machine behavior such as cooldown clearing,
   restart budgets, stale encoder claims, and package repair writes.
3. Use smoke scripts for deployed-stack route behavior once unit tests define the
   expected contract.

Host-level fault injection remains in scope for manual or host-only smoke runs:

1. Firewall or Docker network rules can block linearcast from reaching a real Plex
   host to verify deployed behavior.
2. Toxiproxy can model latency, hangs, resets, and bandwidth limits without
   editing firewall state.
3. Any host-level test must clean up its network rule/proxy state on exit and
   must not be required for normal CI.

## Minimum Assertions

Each automated failure scenario should assert:

1. The first failure is visible to the client.
2. Repeated failures do not hot-loop the dependency.
3. `Retry-After` is present when the route is intentionally cooling down.
4. A healthy dependency recovers without manual DB edits or process restart.
5. The failure does not mutate schedule state or package state outside the
   component's allowed write boundary.
