# Resilience Contract

Phase C tracks normal operational failures: interrupted jobs, missing artifacts,
and temporary local resource errors. The goal is deterministic degradation and
recovery, not random chaos experiments.

## Failure Matrix

| Surface | Failure trigger | Expected response | State effect | Recovery path |
|---|---|---|---|---|
| On-demand | ffmpeg spawn failure | Playback returns warming/error response with retry guidance, not a tight loop | Entry restart budget/cooldown may be updated | Dependency/config fixed; entry retries after cooldown |
| On-demand | ffmpeg exits or stalls mid-encoding | Channel encoding is stopped/restarted within budget; over-budget returns `503`/`Retry-After` | Encoding restart count and blocked-until state | Cooldown expiry allows a fresh encoding attempt |
| On-demand | Capacity exhausted | New tune-in returns `503`/`Retry-After`; existing encodings continue | No new encoding admitted | Idle encodings evict or operator raises capacity |
| Packaged playback | Ready init/segment artifact missing | Stale child request returns `404`; later eager manifest refreshes return `503` while repair is pending | Narrow playback repair changes `ready -> pending` and clears stale segment metadata | Packager rebuilds package; later requests serve ready artifacts |
| Packaged playback | Repair write fails because DB is locked/unavailable | Stale child request still returns `404`; package may remain `ready` until retry | No partial schedule writes | Later request retries repair when DB is writable |
| Encoder transport | Worker loses heartbeat or dies | Job becomes claimable after stale lease window | Package remains transient/in-progress until reclaimed | Same or another worker reclaims and completes |
| Encoder transport | Upload interrupted or tar invalid | Job fails transiently or terminally according to package error classification | Partial package artifacts are not promoted to ready; invalid tar uploads never replace an existing package root | Retry uses a clean package attempt unless error is terminal |
| Encoder transport | Remote complete upload is received, but finalization or DB completion fails | Complete route returns an error and leaves the package out of `ready` | Uploaded package directory and any unpromoted segment rows are removed best-effort; if the failure also left a processing row with no active lease, it is requeued as transient | Encoder reports failure or lease expiry requeues the job; retry starts from a clean upload |
| SQLite/disk | Temporary lock, read-only path, or full-like write failure | Caller returns actionable error; unrelated reads/writes should not corrupt state | Transaction rolls back or no state is promoted | Operator fixes local resource; next operation retries normally |

## Structured Logging

All output is JSON via `log/slog` with the standard `time`, `level`, `msg` fields.
Phase C failure paths use the key names documented below as additional JSON fields
for Loki `| json` parsing. Do not invent per-call-site variants (`channel` vs
`channel_id`).

| Field | Meaning | Emitted by |
|---|---|---|
| `entry_id` | Affected schedule entry | on-demand |
| `err` | Error string | all failure paths |
| `stage` | Failure stage within a multi-step operation | encoder completion cleanup |
| `package_id` | Affected package (repair requeue) | packaged repair |

Legacy unconverted `log.Printf` calls are captured by a bridge adapter that wraps
the full text in a single `msg` field. As each call site is migrated to
structured `slog.Info/Warn/Error`, its fields improve from one flat string to
proper JSON keys.

HTTP request-log middleware covers both the public/service and protected admin
route trees in the composed server, emitting one JSON line per request with
`method`, `path`, `status`, and `duration_ms`.

## Automation Strategy

Default automation should be deterministic and safe to run repeatedly:

1. Use focused Go tests for state-machine behavior such as restart budgets,
   stale encoder claims, and package repair writes.
2. Use smoke scripts for deployed-stack route behavior once unit tests define the
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
