# Architecture

Snapshot of the system as it stands today: shipped capabilities, the scheduler's
guiding principles, and the invariants that must hold across changes. For
forward-looking work, see [ROADMAP.md](ROADMAP.md).

## Current state

- Channels now support three playback modes: `packaged` (pre-encoded HLS artifacts
  served from disk), `plex_relay` (per-viewer Plex transcode sessions proxied
  through linearcast), and `upstream_hls_url` (external HLS passthrough). The
  `playback_mode` column on the `channels` table discriminates.
- Plex relay channels use a random viewer token in the URL path for session
  affinity. A per-viewer Plex transcode session is created at tune-in, seeked to
  the current wall-clock offset of the schedule entry. Plex HLS manifests are
  rewritten inline to proxy segment URLs through linearcast, avoiding token
  exposure to viewers.
- Plex relay channels are excluded from durable package discovery. They may
  share media rows with packaged channels, but the relay channel itself never
  creates `media_packages` demand.
- The `media.source_ref` column stores source-specific identifiers
  (`"plex://{ratingKey}"`) so the schedule can resolve a media entry to the
  corresponding Plex media key for transcode session creation.

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
- The packager supports multiple profiles. Each channel still has one required
  profile that gates schedule eligibility, and may also define an ABR ladder of
  additional package profiles. Ladder rungs are encoded eagerly for pre-encoded
  video channels and emitted as ready variants in the HLS master playlist. The
  Schedule Builder presents this as a create-time adaptive-bitrate setting; ABR
  lives and dies with the channel rather than being a mutable policy toggle.
- The admin UI has a unified `/schedule` workspace for new-channel schedule
  building and existing-channel schedule edits, plus encoding status,
  profile-aware package queueing, all-profile package inspection, and profile
  management with reference-aware disable/delete behavior.
- The schedule workspace supports exact editor saves (`extendTail=false`), draft
  save/undo, EPG-style timeline drag reorder, single-episode drag insertion,
  and batch drag insertion for whole shows, half-season groups, albums, and
  artists. A secondary list preview provides precise move/remove controls.
- `/schedule?channel=<id>` opens the same workspace with channel name/profile
  locked, existing schedule populated into the draft, and newly-added ready
  media attached to the channel before saving.
- Creating a slot-grid channel keeps the operator in `/schedule?channel=<id>` so
  intentional grid gaps can be filled immediately; back-to-back channel creation
  returns to the watch/admin flow instead of forcing every new channel into the
  schedule editor.
- The schedule picker exposes Episodes (debounced server search), Shows
  (lazy-loaded media-group grid), and Music tabs. Empty schedules still render a
  drop target timeline so picker items can be dragged in before any entries
  exist.
- A multi-channel Guide panel renders a read-only EPG with a 6h / 12h / 24h
  window selector.
- New scheduled channels choose a playback mode in the Schedule Builder:
  `Pre-encode` packages the whole channel ahead, `On-demand` defers packaging
  until a viewer tunes in, and `Plex relay` follows the schedule but proxies
  per-viewer Plex transcode sessions instead of using linearcast packages.
  See "Demand-driven packaging" below for on-demand behavior.

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
3. Eager channels (`prefill_mode = 'eager'`, the default) only place media with
   a ready package for the channel profile. On-demand channels
   (`prefill_mode = 'on_demand'`) place codec-eligible media without requiring
   packages and trigger packaging on tune-in — their schedule may reference
   not-yet-packaged media, surfaced as "warming up" until the demand-triggered
   encode completes. See "Demand-driven packaging" below.
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
Built-in ABR rungs are tagged as `abr` in profile JSON so the admin UI can
present them as one adaptive-bitrate unit while the packager still addresses
each durable package profile by name.
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

## On-Demand Live Sessions

A channel's `prefill_mode` decides whether every program must be packaged ahead
of time. Eager channels require ready packages before scheduling. On-demand
channels schedule from codec-eligible media without ready packages and use the
same wall-clock schedule as every packaged channel.

Playback still prefers durable ready packages. If the current or near-future
entry has a ready package for the channel profile, the manifest serves that
package. If no ready package exists, `linearcast` starts an in-process,
ephemeral ffmpeg live session for that schedule entry. The session seeks to the
current media position, caps encoding at the entry boundary, writes the same
fMP4 HLS layout as packaged media, and the manifest stitches package-backed and
session-backed segments together in one playlist.

On-demand sessions are deliberately not durable package state:

- `DiscoverCandidates` excludes on-demand channels from eager worker discovery,
  so idle on-demand channels encode nothing.
- Session files live under `LINEARCAST_SESSION_DIR` (default
  `/tmp/linearcast-sessions`), which `linearcast` wipes on startup and shutdown.
- Sessions are touched by manifest requests, torn down after idle grace or entry
  end, and prune already-played segment files behind the playhead.
- Admission is bounded by the session manager's max-concurrent setting. Under
  pressure it evicts the least-recently-touched idle channel; if all sessions
  are fresh, manifests return `503` with `Retry-After`.
- Copy-mode video profiles are rejected for sessions because they cannot
  re-keyframe accurately after a seek. Use a transcode profile for on-demand
  channels.

The package worker remains responsible for durable packages only. Operator
retry/package requests can still create normal `media_packages` rows for
on-demand media, and once those packages are ready playback uses them instead
of a live session.

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
