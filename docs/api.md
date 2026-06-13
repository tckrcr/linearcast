# API & Endpoints

HTTP surface for playback and the admin sidecar, plus the Prometheus metrics
exposed for operating a deployment.

## Playback (`:8888`)

```
GET /channel/<id>/stream.m3u8      HLS playlist
GET /channel/<id>/now              What's on (JSON)
GET /healthz                       Liveness
GET /readyz                        Readiness (503 until packages are ready)
GET /status                        Per-channel packaged playback state
GET /metrics                       Prometheus metrics
```

## Admin (`:8890`)

The admin API is broad; the routes below are the commonly used read and
control endpoints. The full set is registered in `internal/admin/routes.go`.

```
GET    /api/now                          Verbose grid for the UI
GET    /api/playing                      Per-channel current item
GET    /api/queue-depth                  Schedule coverage + cache lookahead
GET    /api/channels                     Channel list
POST   /api/channels/{id}/disable        Disable a channel
POST   /api/channels/{id}/enable         Enable a channel
DELETE /api/channels/{id}                Delete a disabled channel; add
                                         ?reclaim-encodes=true to delete
                                         unshared packaged encodes for its media

GET    /api/admin/plex/status            Current admin Plex connection status
PUT    /api/admin/plex/config            Set/test the admin Plex connection
DELETE /api/admin/plex/config            Clear the admin Plex connection
GET    /api/admin/plex/libraries         List Plex libraries
POST   /api/admin/plex/scan              Scan a Plex library into the DB

GET    /api/schedule-builder/shows       Media-group browse for the builder
GET    /api/schedule-builder/package-candidates
POST   /api/schedule-builder/channels    Create a channel from the builder

GET    /api/admin/maintenance/package-integrity
DELETE /api/admin/maintenance/packages

POST   /api/channels/{id}/schedule/gaps/fill
                                         Fill an existing schedule gap with an
                                         attached ready filler asset slice
```

`POST /api/channels` and `POST /api/schedule-builder/channels` accept optional
`scheduleMode` (`back_to_back` or `slot_grid`) and `slotDurationMs` for opt-in
slot-grid scheduling. They also accept `playbackMode: "plex_relay"` for
scheduled Plex relay channels; relay channels require Plex-imported media and
do not queue linearcast packages. `GET /api/channels` returns those fields for
each channel.

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
scoped with `?media=<id>`. `DELETE /api/admin/maintenance/packages?media=<id>`
reclaims package rows and artifacts for one media item; it defaults to dry-run,
and `?dry-run=false&force=true` commits deletion including referenced media.

Jellyfin mirrors the Plex `status`/`config`/`libraries`/`scan` shape under
`/api/admin/jellyfin/*`.

## Observability

`GET /metrics` (on `:8888`) exposes Prometheus metrics for the packaged-only
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

Labels are intentionally bounded to profiles, package statuses, result classes,
artifact types, and channel IDs for channel runway/coverage.
