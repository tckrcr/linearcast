# Architecture

Snapshot of the system as it stands today: shipped capabilities, the scheduler's
guiding principles, and the invariants that must hold across changes. For
forward-looking work, see [ROADMAP.md](ROADMAP.md).

## Current state

- Local deployment runs backend services as native host binaries and keeps only
  the nginx-backed web UI in Docker.
- Local deploy preflights app-owned data/cache paths and external media roots
  before Docker Compose can fail on missing bind mounts.
- Repository-local agent instructions allow `npm run dev` for local web-ui
  iteration; `scripts/deploy-linearcast.sh` remains the deploy and smoke-test
  path.
- `linearcast-encoder run` handles both local and remote encoding. When
  `LINEARCAST_DB` is set it claims jobs directly from SQLite (zero
  download/upload overhead). When absent it uses HTTP claim + tar upload.
  On macOS the local mode can use the native `apple-h264-1080p` VideoToolbox profile.
- The packager supports multiple profiles, but each channel is configured for a
  single required profile.
- The admin UI has a Schedule Builder, a per-channel Schedule Editor, encoding
  status, profile-aware package queueing, all-profile package inspection, and
  profile management with reference-aware disable/delete behavior.
- The schedule editor supports exact editor saves (`extendTail=false`),
  non-rebuilding deletes, range select with shift/ctrl, draft edit with
  save/revert/undo, drag reorder, an EPG-style timeline view behind a toggle,
  and a read-only preview-rebuild endpoint with diff stats.
- Builder and Editor share a `MediaPickerRail` with Episodes (debounced server
  search) and Shows (lazy-loaded media-group grid with Queue all) tabs. The
  Builder's start screen and show-picker modal are gone.
- A multi-channel Guide panel renders a read-only EPG with a 6h / 12h / 24h
  window selector.
- The Schedule Builder and Schedule Editor are still separate UI surfaces with
  different mental models (order-only list vs. timestamped list), even though
  they manipulate the same underlying concept and share the picker rail.

## Scheduler principles

The scheduler should be explicit, inspectable, and reversible.

- Hard rules reject candidates outright.
- Soft weights rank acceptable candidates.
- Every generated entry should be explainable.
- Operators should be able to simulate before writing.
- Edits should be local and reversible instead of rebuilding a whole tail unless
  the operator asks for regeneration.
- Missing packages should produce clear recovery actions rather than mysterious
  schedule gaps.

## Core invariants

These must not break regardless of what changes:

1. Schedule entries stay on the 6000 ms grid.
2. Packaged segment durations are exact and may be non-6000 values such as
   6006 ms or 5964 ms.
3. The scheduler only places media with a ready package for the channel
   profile.
4. Deleted or replaced schedule entries do not immediately replay after a tail
   rebuild.
5. DB migrations preserve existing rows, defaults, and package state.
6. Admin write handlers validate channel IDs, media IDs, package readiness,
   and time boundaries before mutating state.
7. Package repair paths requeue missing or stale artifacts instead of silently
   serving broken playback.

## Packaged encoding contract

linearcast pre-converts each media file to fMP4 HLS before playback, then serves
ready artifacts at request time. There is no live transcoding and no per-client
seek/relaunch. Encode decisions are made once, at package time, for all future
viewers of a channel. This is intentional: wall-clock-driven linear channels
need every viewer to share the same position, and seamless splicing of
back-to-back programs needs predictable segment metadata.

Package profiles are both durable package identities and encoder presets. The
default video profile (`h264-main-1080p`) is the compatibility path: transcode
video to H.264 Main level 4.1, scale sources down to 1080p, cap VBV maxrate at
8 Mbps, and transcode audio to AAC stereo.
Copy profiles are opt-in: they preserve compatible source streams and trade
encode cost and quality loss for a weaker playback guarantee.

The transcode path must keep the HLS segment contract tight:

- GOP length, minimum keyframe interval, forced keyframe cadence, and
  `hls_time` stay aligned to the target segment duration.
- Scene-cut keyframes are disabled so segments start predictably.
- fMP4 HLS uses a separate `init.mp4`, one `stream.m3u8`, and complete segment
  listings (`hls_list_size 0`).
- Source metadata and data streams are not carried into packaged output.
- FFmpeg maps the selected video/audio stream indexes from probe data instead
  of relying on first-stream defaults; audio selection prefers default or
  English/unknown main tracks and avoids commentary/descriptive tracks when a
  better main track exists.
- Transcoded video emits `yuv420p` for broad client compatibility.
- When a profile sets `VideoMaxBitrate`, VBV `bufsize` is twice that maxrate.

Copy mode cannot force new keyframes. The HLS muxer cuts copied video only on
existing source keyframes, so segment durations may be irregular. Playback must
continue to trust parsed `EXTINF` durations rather than nominal segment length.
Copy profiles run ffmpeg with `-fflags +genpts` before input probing so remuxes
with missing presentation timestamps can still produce coherent package
timestamps.
Do not add MPEG-TS-specific H.264 Annex-B bitstream filters to the fMP4 path
without a focused compatibility test; CMAF/fMP4 expects MP4-style samples.

Use the native ffmpeg `aac` encoder for packaged AAC. Do not require Jellyfin's
custom `libfdk_aac` build.

## Subtitle packaging contract

Text subtitle streams are extracted to VTT sidecars. Bitmap subtitle streams
such as PGS/VOBSUB are inventoried in `media_tracks` with
`source = embedded_bitmap` and no playback path; they are visible to operators
but excluded from playback selection until a later workflow produces a usable
text sidecar or burns forced content into the packaged rendition.

Subtitle selection is per-viewer state. The packaged model can support shared
sidecars and forced burn-in, but per-viewer bitmap compositing belongs to a
future live/per-client channel mode rather than the normal package path.

## Configuration: env, compose, and DB

Three places hold configuration, with distinct responsibilities:

**`docker-compose.yml`** owns container shape: host bind paths, host port
mappings, UID/GID, restart policy. Host bind paths are interpolated from `.env`
(`${LINEARCAST_DATA_DIR}`, `${LINEARCAST_CACHE_DIR}`, `${LINEARCAST_MEDIA_ROOT}`)
with `/data/...` defaults. In-container paths are the same as host paths by
convention (identity-mapped), so the bind lines read `${VAR}:${VAR}`.

**`.env`** holds the runtime variables linearcast binaries read at
startup, both inside the container and in native mode:

- `LINEARCAST_DB` — chicken-and-egg; must resolve before any DB read.
- `CACHE_DIR` — read by packager binaries for the package cache location.
- `LINEARCAST_MEDIA_ROOT` — read by the admin binary for the local-source
  scanner. The matching read-only bind lives in compose.
- `LINEARCAST_ADDR`, `LINEARCAST_ADMIN_ADDR` — where each service binds inside
  the container. The matching host port mapping lives in compose; the two must
  agree.
- `LINEARCAST_ADMIN_PASSWORD` — would require a first-run setup flow, hashed
  storage, and a password-reset CLI before it could move to DB. Treat as a real
  auth feature when picked up, not a rename.
- `LINEARCAST_ADMIN_ALLOW_NO_AUTH` — intentional env-only recovery toggle.
- `TZ` — process runtime.
- `LINEARCAST_ENCODER_ADMIN_URL`, `LINEARCAST_ENCODER_API_KEY`,
  `LINEARCAST_ENCODER_WORK_DIR` — live on the remote Windows encoder box.
- `LINEARCAST_ENCODER_DIST_DIR` — host install path used by the deploy script
  in native mode; not a tunable.

**`settings` table** holds runtime tunables managed via the admin UI: default
packaged profile (Profiles panel), local encoder concurrency (concurrency=0
disables claiming), admin sweeper interval and max attempts
(`encoder_sweep_interval_seconds`, `encoder_max_attempts`), packager defaults
(poll interval, ffmpeg preset, stale-after, integrity interval, max attempts).
The target segment cadence is a code constant
(`scheduler.TargetSegmentMs = 6000`), not a runtime setting.

## Encoder transport boundary

Local mode speaks SQLite/filesystem; remote mode speaks HTTP/tar. The reuse
boundary is the shared state transitions (`db.ApplyFinalizedPackageTransition`,
`db.resolveAndApplyFailure`) and the packager core (`EncodePackageOutput`,
`FinalizePackage`). Do not add a generic `JobDriver` interface unless a third
transport appears.

## Package state machine

`media_packages.status ∈ {pending, processing, ready, missing, failed}`

```
pending ──claim──> processing ──success──────────> ready
                       │
                       ├──transient failure──> pending  [attempts++]
                       │
                       └──terminal failure───> failed   [no auto-retry]

failed  ──operator retry──> pending  [resets attempts to 0]
ready   ──missing artifact─> pending  [integrity check / repair]
```

Leases live in `encoder_jobs` (separate table from `media_packages` so
heartbeat writes don't churn the playback-read path). Local-mode claims
(`EncoderID=""`) use time-based stale recovery on `updated_at_ms` via
`recoverStale`; remote claims use an explicit `lease_expires_ms` TTL swept
by `Sweeper`.

## Remote encoder tar contract

`POST /api/encoder/jobs/{id}/complete` accepts `application/x-tar`. The
archive must contain root-level regular files only: `init.mp4`,
`stream.m3u8`, and at least one `seg*.m4s`. Anything nested, absolute,
dot-prefixed, non-regular, or outside that set is rejected before extraction
is published. Unpacks to `PackageRoot/<media_id>/<profile>/`.
