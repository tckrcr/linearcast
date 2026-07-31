# API & Endpoints

HTTP surface for the composed backend listener, plus the Prometheus metrics
exposed for operating a deployment. nginx publishes every route on the same
public origin; the backend listens on `LINEARCAST_ADDR` inside the container.

## Playback and service routes

```
GET /channels/<id>/stream.m3u8     HLS playlist
GET /channels/<id>/now              What's on (JSON)
GET /healthz                       Liveness (HTTP reachability — a response
                                   proves the process is alive; no deeper
                                   check, by design for a single-host appliance)
GET /readyz                        Readiness (503 when an eager channel's active
                                   packaged manifest is unavailable; on-demand
                                   channels do not require durable packages).
                                   Binary 200/503 for traffic routing — does
                                   not reflect degraded state.
GET /status                        Per-channel packaged playback state
GET /metrics                       Prometheus metrics
```

### Degraded state

`linearcast_degraded{signal="..."}` on `/metrics` is 1 when a signal crosses
its operator-attention threshold, 0 otherwise. It does not affect `/readyz`:
degraded means "still serving, but something needs attention," not unhealthy.
A container restart on a degraded signal would be exactly wrong for a
single-host appliance with no replica.

`GET /api/degraded` (admin session) returns the same signals as JSON
(`{signals: [...], degraded: bool}`) for the operator recovery view in the
web UI. The Tools panel renders the signals with recommended actions that
link to the existing maintenance surface.

Current signals and thresholds:

- `disk_pressure` — `linearcast_disk_free_gb` < 5 GB on the cache filesystem.
- `at_capacity` — on-demand active encodings / max concurrent >= 1.0.

Thresholds are shared constants in `internal/metrics/metrics.go`; the
`cmd/linearcast` scrape collector and `internal/playback` degraded-status
provider evaluate the same values.
Queue depth, runway, and lease count are exposed as raw gauges for direct
alerting; add degraded flags for them when operational thresholds are clear.

## Admin and encoder routes

The admin API is broad; the routes below are the commonly used read and
control endpoints. The full set is registered in `internal/admin/routes.go`.

The browser-facing subset is defined by `api/openapi.yaml`. That contract is
the source for generated TypeScript DTOs in
`web-ui/src/api/generated/schema.ts`, and representative Go handler responses
are validated against it in the backend test suite. Regenerate DTOs with
`cd web-ui && npm run api:generate`; `scripts/verify.sh` rejects stale output.
Maintenance and encoder APIs are intentionally not part of this initial
checked subset.

Contracted API errors use this shape:

```json
{"error":"invalid_request","message":"request is invalid","hint":"optional operator guidance"}
```

The UI transport exposes `error` as `ApiError.code` for ergonomic branching.

```
GET    /api/now                          Verbose grid for the UI
GET    /api/playing                      Per-channel current item
GET    /api/queue-depth                  Schedule coverage + cache lookahead
GET    /api/channels                     Channel list
PATCH  /api/channels/{id}                 Update channel flags (enabled,
                                       hiddenFromGuide)
PUT    /api/channels/{id}/on-demand-profile
                                         Change the package profile for an
                                         on-demand channel
DELETE /api/channels/{id}                Delete a disabled channel; add
                                         ?reclaim-encodes=true to delete
                                         unshared packaged encodes for its media

GET    /api/admin/plex/status            Current admin Plex connection status
PUT    /api/admin/plex/config            Set/test the admin Plex connection
DELETE /api/admin/plex/config            Clear the admin Plex connection
GET    /api/admin/plex/libraries         List Plex libraries
POST   /api/admin/plex/scan              Scan a Plex library into the DB

GET    /api/public-server-url            Read the configured public origin used
                                         for copyable IPTV/DVR URLs
PUT    /api/public-server-url            Set the public origin; empty means
                                         use the current browser origin
GET    /api/art/media/{id}               Public proxied media artwork for
                                         XMLTV/IPTV clients

GET    /api/admin/media-sources/status   Media-source readiness for the admin UI
GET    /api/media/inventory              Paginated media inventory for the
                                         Inventory panel
PATCH  /api/media/{id}                   Update media title, show
                                         (`collectionName`), seasonNumber, or
                                         episodeNumber
POST   /api/media/collections/bulk       Preview or apply bulk collection
                                         set/clear/rename operations
GET    /api/media/shows                  Show collections with season summaries
GET    /api/media/movies                 Movie scheduling-group summaries
GET    /api/media/albums                 Music artist/album summaries
GET    /api/media/by-group               Media rows in one scheduling group
GET    /api/media/package-profiles       Available package profiles
GET    /api/media/package-candidates
GET    /api/filler-assets/candidates
POST   /api/channels                     Create a channel

GET    /api/admin/maintenance/package-integrity
DELETE /api/admin/maintenance/packages

POST   /api/channels/{id}/schedule/gaps/fill
                                         Fill an existing schedule gap with an
                                         attached ready filler asset slice
```

Package status fields use the four durable `media_packages.status` values:
`pending`, `processing`, `ready`, and `failed`. The profile-scoped package
candidate, channel-media, and filler-candidate projections additionally emit
`packageStatus: "missing"` only when no package row exists for that
media/profile pair; it is not a stored state. The inventory filter
`packageStatus=missing` is broader and selects media with no package row under
any profile. A missing package artifact is never reported as a `missing` row
status: artifact detection requeues a ready row to `pending`.

`POST /api/channels` accepts optional `scheduleMode` (`back_to_back` or
`slot_grid`) and `slotDurationMs` for opt-in slot-grid scheduling. It accepts
`prefillMode: "on_demand"` for channels that should encode at tune-in instead
of pre-encoding the whole channel; on-demand channel creation does not queue
durable packages automatically. `GET /api/channels` returns those fields for
each channel.

`PUT /api/channels/{id}/on-demand-profile` accepts `{"profile":"<name>"}` and
only works for channels whose `prefillMode` is `on_demand`. It changes
the channel's required package profile without clearing schedule rows or queuing
durable package work; playback starts using the new profile after the next
runtime refresh/viewer request. Pre-encoded channels should be
recreated when their profile strategy changes.

`POST /api/channels/{id}/schedule/gaps/fill` accepts `mediaId`, `startMs`, an
optional `offsetMs`, and an optional `offsetMode`. `offsetMode` defaults to
`zero` (start the filler at `offsetMs`); `sequential` continues the filler
rotation from where the previous placement of the same asset on the channel
ended, wrapping back to the asset start when continuing would overrun its
packaged duration. The response echoes the resolved `offsetMs`.

`DELETE /api/channels/{id}` requires the channel to be disabled. By default it
removes the channel, playlist membership, and schedule entries while keeping
packaged media. With `?reclaim-encodes=true`, it also deletes package rows and
on-disk artifacts for every media item from the deleted channel that is not
still referenced by another channel; `?force=true` includes shared media.

`GET /api/admin/maintenance/package-integrity` checks package files, optionally
scoped with `?media=<id>`. `POST /api/admin/maintenance/import-packages`
reattaches finalized package directories already on disk to existing media rows
without re-encoding video; finalize also rebuilds package-owned subtitle track
metadata from the source. `DELETE /api/admin/maintenance/packages?media=<id>`
reclaims package rows and artifacts for one media item; it defaults to dry-run,
and `?dry-run=false&force=true` commits deletion including referenced media.

Jellyfin mirrors the Plex `status`/`config`/`libraries`/`scan` shape under
`/api/admin/jellyfin/*`.

Plex scans hydrate media titles, descriptions, ratings, thumbnail paths, and
collection genres when Plex provides them. Public artwork URLs in XMLTV point
at `/api/art/media/{id}`; that route fetches the configured Plex thumbnail with
the stored token server-side, so clients never receive the Plex token.

`POST /api/media/collections/bulk` accepts `action` (`set`, `clear`, or
`rename`), either `mediaIds` or an inventory-style `filter`, and `dryRun`.
`set` and `rename` require `collection`; `rename` also requires
`fromCollection`. A dry run returns the matched row count without writing.
Collection writes attach media rows to canonical `collections` records.

`PATCH /api/media/{id}` accepts any subset of `title`, `collectionName`,
`seasonNumber`, and `episodeNumber`. Empty strings clear text fields; `null`
clears season or episode ordering. Non-null ordering values must be positive
integers.

`GET /api/media/shows` returns codec-compatible, non-filler video collections
as shows with season summaries derived from stored `seasonNumber` metadata.
Members without a season are returned in an unsorted (`seasonNumber: null`)
summary. New filename ingest writes TV collections at the show level rather
than exposing half-season `H1`/`H2` buckets.

## Observability

`GET /metrics` on the public origin exposes Prometheus metrics for the playback
runtime. The important questions are:

- Is package work backing up? Use `linearcast_package_queue_depth` by
  `rendition_profile` and `status`.
- Are package jobs slow or failing? Use
  `linearcast_package_job_duration_seconds` and
  `linearcast_package_state_transitions_total`.
- Is playback runway healthy? Use `linearcast_schedule_runway_seconds`,
  `linearcast_schedule_runway_by_channel_seconds`, and
  `linearcast_schedule_gap_active`.
- Are manifests fast and populated? Use
  `linearcast_manifest_generation_duration_seconds` and
  `linearcast_manifest_segments_listed`.
- Are packaged files missing at request time? Use
  `linearcast_packaged_artifact_not_found_total` and
  `linearcast_package_repair_requeues_total`.

The following signals are derived at scrape time by the scrape owner
(`cmd/linearcast/scrape.go`), not pushed by write events, so they always reflect
current state:

- Are encoder leases held or drifting? Use `linearcast_encoder_active_leases`
  (encoder_jobs rows with `lease_expires_ms >= now`); it falls as leases expire
  and the sweeper reclaims them.
- Is on-demand encoding at capacity? Use
  `linearcast_on_demand_active_encodings` against
  `linearcast_on_demand_max_concurrent`; the ratio is capacity utilization.
- Is the artifact disk filling? Use `linearcast_disk_free_gb`, the free space on
  the filesystem holding the package cache (`CACHE_DIR`).
- Queue depth, schedule runway, schedule gaps, and per-channel ready-package
  duration are also derived at scrape time by the same owner, so they reflect
  the current DB state on every poll rather than the value at the last
  scheduler tick or worker loop iteration.

Labels are intentionally bounded to profiles, package statuses, result classes,
artifact types, and channel IDs for channel runway/coverage.
