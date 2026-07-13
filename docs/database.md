# Database

The SQLite database is the single source of truth for all channel state. It coordinates three independent workflows: ingest (media management), scheduling (playback planning), and packaging (pre-transcoding).

---

## Role in the System

The database sits between three components:

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│  linearcast      │     │  linearcast-    │     │  linearcast-    │
│  (playback)     │     │  extender       │     │  packager      │
│  narrow write   │     │  read/write     │     │  read/write     │
└────────┬────────┘     └────────┬────────┘     └────────┬────────┘
         │                      │                      │
         └──────────────────────┼──────────────────────┘
                                │
                        ┌───────▼───────┐
                        │  SQLite DB    │
                        │  (single     │
                        │  source of   │
                        │  truth)      │
                        └──────────────┘
```

- **linearcast** opens the database read-write for two narrow runtime paths. It
  serves packaged media and may mark a `ready` package back to `pending` when a
  packaged init/segment artifact is missing on disk. It also publishes ephemeral
  `on_demand_encodings` rows while on-demand ffmpeg channel encodings are active.
- **linearcast-extender** and **linearcast-packager** open read-write (`OpenReadWrite`). They coordinate through SQLite's WAL mode and busy timeout (5s).
- **linearcast-admin** and maintenance tools are also read-write for channel/media CRUD, migrations, diagnostics, and repair workflows.

---

## Ownership Boundaries

| Component | Writes | Reads |
|-----------|--------|-------|
| linearcast | media_packages, packaged_segments (ready artifact repair only), on_demand_encodings | All tables; exports metrics on 60s refresh tick |
| linearcast-extender | schedule_entries | channels, media, channel_media, media_packages |
| linearcast-packager | media_packages, packaged_segments | media |
| linearcast-admin | channels, channel_media | All tables (read-write for Plex imports and channel management) |
| cmd/ingest | media | channels, media |

No component writes schedule entries except the scheduler. Package state writes
belong to the packager except for the narrow `linearcast` ready-artifact repair
path and explicit admin retry requests. `on_demand_encodings` is runtime
observability owned by playback, not durable package or schedule state. Metrics are exported from
`linearcast`'s process (which serves `/metrics`) rather than the extender, so
Prometheus scrapes always see current values.

---

## Tables

### channels

Channel configuration and policy.

| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | Channel identifier |
| display_name | TEXT | Human-readable name |
| source_directory | TEXT | Media source path |
| ordering | TEXT | `alphabetical` or `block` |
| enabled | INTEGER | 1 = active, 0 = paused |
| created_at_ms | INTEGER | Creation timestamp |
| description | TEXT | Optional description |
| hidden_from_guide | INTEGER | 1 = omit from public guide/source listings; direct stream URLs still work |
| playback_mode | TEXT | Always `packaged` |
| required_package_profile | TEXT | Package profile (e.g., `h264-1080p-8mbps`) |
| abr_ladder_json | TEXT | Optional ordered JSON array of package profile names for adaptive bitrate variants |
| package_prefill_ms | INTEGER | Package coverage horizon in ms |
| schedule_mode | TEXT | `back_to_back` or opt-in `slot_grid` |
| slot_duration_ms | INTEGER | Slot-grid interval in ms; 6s-aligned when set |
| prefill_mode | TEXT | `eager` (default - package the whole channel ahead) or `on_demand` (serve unpackaged entries through ephemeral channel encodings when a viewer tunes in) |

**Key invariant**: `playback_mode` is always `packaged`. The system does not support generated playback. `required_package_profile` is the single schedule gate; `abr_ladder_json` only adds package demand and HLS variants, and unready ladder rungs are omitted from the master playlist. The Schedule Builder create API accepts `adaptiveBitrate` for eager video channels; channel policy updates do not toggle ABR after creation. `required_package_profile` is mutable only for `on_demand` packaged channels, where the change leaves schedules and package rows untouched. `back_to_back` remains the default schedule mode. `slot_grid` keeps primary entries at real packaged duration but advances each next primary start to the next `slot_duration_ms` wall-clock boundary, leaving explicit gaps for future filler/dead-air materialization. `prefill_mode` is `eager` by default; `on_demand` schedules from codec-eligible media without requiring ready packages and encodes at tune-in.

### collections

Operator-facing media collections. Ingest and metadata edits attach media rows
to these records; scheduling still derives a block-rotation label from the
collection name.

| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | Deterministic collection identifier |
| name | TEXT | Display name |
| kind | TEXT | `show`, `movie`, `album`, `artist`, or `custom` |
| source | TEXT | `manual`, `filename`, `plex`, or `jellyfin` |
| genres_json | TEXT | Optional JSON array of source-provided genre labels |
| created_at_ms | INTEGER | Creation timestamp |
| updated_at_ms | INTEGER | Last update timestamp |

`kind, name` is unique.

### media

Ingested media items with codec validation metadata.

| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | Media identifier (derived from path) |
| path | TEXT | Full file path |
| directory | TEXT | Containing directory |
| title | TEXT | Optional title |
| scheduling_group | TEXT | Legacy grouping label retained for backfill/migration reads |
| collection_id | TEXT FK | Canonical collection reference; NULL means uncollected |
| description | TEXT | Optional source-provided summary/description |
| thumb_path | TEXT | Optional source-specific thumbnail path; Plex paths are proxied through `/api/art/media/{id}` |
| content_rating | TEXT | Optional source-provided content rating |
| user_preference | INTEGER | Per-media priority (higher = preferred) |
| duration_ms | INTEGER | Source duration in milliseconds |
| container | TEXT | File container (mkv, mp4, etc.) |
| video_codec | TEXT | Video codec (h264, hevc) |
| video_width | INTEGER | Video resolution width (NULL = not yet re-probed) |
| video_height | INTEGER | Video resolution height |
| color_transfer | TEXT | ffprobe color_transfer; smpte2084/arib-std-b67 mark HDR (NULL = unknown) |
| color_primaries | TEXT | ffprobe color_primaries (e.g. bt709, bt2020; NULL = unknown) |
| codec_tag_string | TEXT | ffprobe codec_tag_string for the video stream (e.g. hvc1, dvhe); used for Dolby Vision Profile 5 detection (NULL = unknown) |
| audio_codec | TEXT | Audio codec (aac, etc.) |
| codec_check_passed | INTEGER | 1 = passed, 0 = rejected |
| codec_check_reason | TEXT | Rejection reason if failed |
| ingested_at_ms | INTEGER | Ingest timestamp |

**Key invariant**: Only media with `codec_check_passed = 1` is eligible for scheduling.

#### Codec admission policy

`codec_check_passed` / `codec_check_reason` are written at ingest by the single
admission gate `codec.Admit` (`internal/codec/policy.go`) — the same function the
scheduler re-check and the admin probe flow call, so the accept/reject policy is
defined and verified in exactly one place. A source is admitted when:

- **Container** is `mkv` or `mp4`.
- **Video codec** is one of `h264`, `hevc`, `vc1`, `mpeg2video`, `mpeg4`, `vp9`.
  HEVC is admitted for both SDR and HDR — the H.264 transcode rungs decode any
  source, `hevc-1080p-16mbps-hdr` preserves HDR in an HEVC transcode, the
  `hevc-2160p-40mbps-hdr` rung provides a high-quality 2160p HDR archive, and
  the `hevc-copy-source` rung remuxes HEVC directly. AV1
  is not yet admitted (no AV1 copy rung).
- **Not Dolby Vision Profile 5.** A `dvhe`/`dvh1` video codec tag has no usable
  HDR10 base layer and cannot be copied or transcoded into a watchable stream, so
  it is rejected at scan time (reason `dolby_vision_p5=<tag>`). This mirrors the
  packager's terminal `ErrUnsupportedDolbyVision`, surfacing the failure early
  instead of late in packaging.
- **Audio codec** is on the allowlist (aac, ac3, eac3, dts and variants, truehd,
  flac, opus, mp3, pcm_s16le/s24le).

Resolution is not capped: 4K (SDR or HDR) is admitted. Rejection reasons are
structured `key=value` tokens joined by `; ` (e.g. `container=avi; audio_codec=wmav2`).

### channel_media

Many-to-many join table binding media to channels.

| Column | Type | Description |
|--------|------|-------------|
| channel_id | TEXT FK | References channels.id |
| media_id | TEXT FK | References media.id |
| anchor_media_id | TEXT NULL | Predecessor's media_id in the channel's linked list; NULL marks the head |
| added_at_ms | INTEGER | When added to channel |

**Key invariant**: per-channel playback order is a singly linked list built from
`anchor_media_id`. Each channel has exactly one head row (`anchor_media_id IS
NULL`) and at most one successor per anchor, enforced by partial unique
indexes. To walk the order, start from the head and follow the chain. A media
item can belong to multiple channels independently.

### schedule_entries

Planned playback windows. Each entry specifies which media plays at which time.

| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | Stable schedule entry ID |
| channel_id | TEXT FK | Channel |
| start_ms | INTEGER | Start time (wall-clock offset) |
| media_id | TEXT FK | Media to play |
| offset_ms | INTEGER | Start offset within source media |
| duration_ms | INTEGER | Playback duration |
| created_at_ms | INTEGER | When entry was created |

**Key invariant**: `start_ms` and `duration_ms` must be aligned to 6000ms (the schedule grid). The CHECK constraint enforces this in the schema.

### play_history

Runtime-observed playback history. Rows are inserted by `linearcast` when a
packaged manifest request resolves the current schedule entry and ready package.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER PK | Autoincrement history row ID |
| channel_id | TEXT FK | Channel |
| schedule_entry_id | TEXT | Stable schedule entry ID |
| media_id | TEXT FK | Media that played |
| started_at | INTEGER | Entry start time in unix-ms |
| ended_at | INTEGER | Entry end time in unix-ms |
| duration_ms | INTEGER | Played entry duration |

**Key invariant**: `(channel_id, schedule_entry_id)` is unique, so repeated
manifest requests for the same entry do not create duplicate history rows. The
schedule entry ID is intentionally not a foreign key; history remains durable
after schedule rebuilds or clears.

### on_demand_encodings

Current on-demand ffmpeg channel-encoding state for the authenticated admin UI.

| Column | Type | Description |
|--------|------|-------------|
| encoding_id | TEXT PK | Ephemeral encoding identifier |
| channel_id | TEXT | Channel being served |
| schedule_entry_id | TEXT | Schedule entry being encoded |
| media_id | TEXT | Source media ID |
| profile | TEXT | On-demand package profile |
| state | TEXT | `starting`, `serving`, `ended`, `failed`, or `stopping` |
| process_running | INTEGER | 1 while ffmpeg is expected to be running |
| spawned_at_ms | INTEGER | ffmpeg start timestamp |
| first_segment_at_ms | INTEGER | First segment observed timestamp; 0 until serving |
| last_progress_ms | INTEGER | Last manifest/segment progress timestamp |
| segment_count | INTEGER | Parsed encoding segment count |
| updated_at_ms | INTEGER | Last row update timestamp |
| last_error | TEXT | Last ffmpeg/encoding error, when retained in memory |

**Key invariant**: rows are ephemeral process state, not history. `linearcast`
clears stale rows on startup, upserts the active encoding row as ffmpeg starts and
segments progress, and deletes the row when the encoding tears down.

### media_packages

Package state machine tracking pre-transcoded media.

| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | Package identifier |
| media_id | TEXT FK | Source media |
| rendition_profile | TEXT | Package profile key (e.g., `h264-1080p-8mbps`) |
| status | TEXT | State (see below) |
| package_root | TEXT | Package directory |
| init_segment_path | TEXT | fMP4 init segment |
| segment_base_path | TEXT | Segment directory |
| container | TEXT | Output container (fmp4) |
| video_codec | TEXT | Output codec |
| video_profile | TEXT | Output profile |
| video_width | INTEGER | Output width |
| video_height | INTEGER | Output height |
| audio_codec | TEXT | Output audio codec |
| audio_profile | TEXT | Output audio profile |
| timescale | INTEGER | fMP4 timescale |
| packaged_duration_ms | INTEGER | Exact packaged duration |
| package_bytes | INTEGER | Finished package size in bytes, recorded at finalize from init + segment files |
| error | TEXT | Failure message if status = failed |
| created_at_ms | INTEGER | Creation timestamp |
| updated_at_ms | INTEGER | Last update timestamp |

`(media_id, rendition_profile)` is the package identity, matched exactly by
scheduler/playback eligibility. The profile names the encoder preset and fully
determines the output bytes — including any forced-subtitle burn the profile
resolves to from the media's intrinsic tracks — so a given media item under a
given profile always produces one package at `media/profile`. Historical fake
profiles such as `h264-maindfdfdf-1080p` can still exist as ready packages and
consume cache, but they are invalid for new encode work and will not satisfy a
channel requiring `h264-1080p-8mbps`.

`package_bytes` is populated for newly finalized packages. Existing ready
packages created before the column can be filled with
`linearcast-admin maint backfill-package-bytes`, which sums the DB-tracked init
segment and packaged segment paths without walking package directories.

### packaged_segments

Exact segment metadata for HLS manifest construction.

| Column | Type | Description |
|--------|------|-------------|
| package_id | TEXT FK | References media_packages.id |
| segment_number | INTEGER | Segment index |
| media_start_ms | INTEGER | Position in source media |
| duration_ms | INTEGER | Segment duration (exact) |
| path | TEXT | Segment file path |
| byte_range_start | INTEGER | Partial HTTP range start |
| byte_range_length | INTEGER | Partial HTTP range length |

**Key invariant**: `DurationMs` is the *exact* duration used in `#EXTINF`. This is critical for continuous playback without gaps or overlaps.

### package_tracks

Package-owned subtitle/audio track metadata. Text subtitle rows point at WebVTT
sidecars inside the package root, e.g.
`CACHE_DIR/packages/<media_id>/<profile>/subtitles/s3.vtt`. Rows reference
`media_packages.id` with `ON DELETE CASCADE`, so reclaiming a package removes
the subtitle metadata with the encode.

| Column | Type | Description |
|--------|------|-------------|
| package_id | TEXT FK | References media_packages.id |
| kind | TEXT | `subtitle` today; `audio` reserved |
| stream_index | INTEGER | Source stream index |
| language | TEXT | Source language tag |
| title | TEXT | Source track title |
| codec | TEXT | `webvtt` for extracted text sidecars; source codec for bitmap inventory |
| source | TEXT | `embedded_text`, `embedded_bitmap`, or `manual` |
| default_flag | INTEGER | Source default disposition |
| forced | INTEGER | Forced-display/foreign-dialogue disposition |
| hearing_impaired | INTEGER | SDH disposition |
| path | TEXT | Package-owned sidecar path; NULL for bitmap inventory |

### package_profiles

First-class profile definitions for packaging behavior. Built-in profiles are seeded at schema initialization.

| Column | Type | Description |
|--------|------|-------------|
| name | TEXT PK | Profile identifier (e.g., `h264-1080p-8mbps`) |
| is_builtin | INTEGER | 1 = built-in, 0 = custom |
| profile_json | TEXT | Encoder configuration as JSON, including optional `tags` such as `default` or `abr` and optional `subtitles` burn policy |
| created_at_ms | INTEGER | Creation timestamp |
| updated_at_ms | INTEGER | Last update timestamp |

The seeded compatibility video profile is `h264-1080p-8mbps` / `broad compatibility - 1080p h.264`
(H.264 Main level 4.1, CRF 23, 1080p scale-down, 8 Mbps VBV maxrate, AAC
stereo). The other seeded video
profiles are a high-bitrate 1080p H.264 archive (`h264-1080p-20mbps`),
a 1080p H.265 HDR utility (`hevc-1080p-16mbps-hdr`), a 2160p H.265 HDR
archive (`hevc-2160p-40mbps-hdr`), and HEVC source copy. Music
keeps its audio-only AAC profile. Lower-resolution and other quality-tier
variants can be added as custom profiles when needed. Profile names are the
durable package key and appear in `media_packages.rendition_profile`. All
encode work, channel policy writes, and manual queue endpoints validate
against the active registry allow-list. Video transcode built-ins default to a
`subtitles` policy of `forced_burn` for English, so matching forced tracks are
baked into the encoded video; profiles that do not burn a forced text track can
surface it as a soft HLS forced rendition.

### admin_write_log

Append-only operator action log for observability. Rows are never updated or deleted from application code.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER PK | Auto-increment row ID |
| created_at_ms | INTEGER | Action timestamp |
| method | TEXT | HTTP method (POST, PUT, DELETE) |
| path | TEXT | Request path |
| action | TEXT | Action name (e.g., `schedule_save`, `package_now`) |
| target_type | TEXT | Object type (e.g., `channel`, `media`) |
| target_id | TEXT | Object ID |
| status | INTEGER | HTTP response status |
| duration_ms | INTEGER | Request duration |

Used for operator timeline reconstruction and auditing. Does not capture request bodies, credentials, or response bodies.

---

## Key Invariants

1. **Eager channels schedule only ready packages.** For `prefill_mode = 'eager'` channels the scheduler's `EligibleReadyPackagedChannelMedia` query joins `channel_media` → `media` → `media_packages` with `status = ready`; unpackaged or in-progress media is not scheduled. Live-encoded channels (`prefill_mode = 'on_demand'`) instead schedule from `EligibleChannelMedia` (codec-eligible, package-agnostic) and serve unpackaged entries through ephemeral channel encodings.

2. **All schedule times are 6000ms-aligned.** The schema CHECK constraint rejects any `start_ms` or `duration_ms` not divisible by 6000.

3. **Playback mode is always packaged.** The `channels.playback_mode` column only accepts `packaged`. Generated playback was removed.

4. **Package state is durable but repairable.** Normal worker claims skip
   `ready` packages, but integrity repair may move `ready -> pending` when
   packaged artifacts are missing. Failed packages are not auto-retried; the
   admin queue endpoint explicitly resets failed rows to `pending` after the
   operator restores the cause.

5. **Segments are exact.** `packaged_segments.duration_ms` reflects the actual encoded duration, not a rounded or nominal value. This prevents the ~0.3% frame-skipping that occurred on generated playback.

6. **Single-writer coordination.** Writers use `OpenReadWrite` which sets `max_open_conns = 1` and configures a 5-second busy timeout. SQLite enforces serial writes; the timeout is the coordination policy. `OpenReadWrite` intentionally pins each read-write handle to one open SQLite connection. Some write workflows use `BEGIN IMMEDIATE` through `*sql.DB` rather than passing explicit `*sql.Tx` through every helper. Do not raise `MaxOpenConns`, introduce shared read-write handles across goroutines, or split a write workflow across DB handles without revisiting transaction boundaries and adding rollback/concurrency tests.

---

## Media Package State Machine

### States

| State | Meaning |
|-------|---------|
| pending | Package requested but not yet claimed |
| processing | Worker is actively encoding |
| ready | Package complete and playable |
| failed | Encoding failed (retryable) |

### Transitions

```
        ┌─────────────────┐
        │     pending     │◄──────────────┐
        └────────┬────────┘               │
                 │ claim                  │ artifact repair
                 ▼                        │
        ┌─────────────────┐
        │   processing    │◄────────────────────┐
        └────────┬────────┘                     │
                 │ complete                     │ stale / retry
                 ▼                             │
        ┌─────────────────┐                     │
        │     ready      │                      │
        └─────────────────┘─────────────────────┘
```

### Valid Transitions

| From | To | When |
|------|----|----|
| (insert) | pending | Package creation |
| (insert) | processing | Direct worker claim |
| (insert) | ready | One-shot CLI |
| (insert) | failed | One-shot CLI failure |
| pending | processing | Worker claims via `ClaimPackage` |
| processing | ready | Packager completes successfully |
| processing | failed | Packager encounters error |
| processing | processing | Stale detection (noop) |
| ready | pending | Artifact repair via playback 404 or worker integrity sweep |
| failed | pending | Explicit admin retry request |

### Components

| Transition | Performed By |
|------------|-------------|
| pending → processing | `ClaimPackage` (packager worker) |
| processing → ready | Packager after successful ffmpeg |
| processing → failed | Packager on ffmpeg failure |
| processing → (stale) | `FailStaleProcessingPackages` (scheduler or packager startup) |
| ready → pending | `MarkReadyPackagePendingForReencode` after missing artifact detection |
| failed → pending | Admin package queue retry |

### Failure and Retry

- **Stale processing detection**: Packages stuck in `processing` for longer
  than the configured cutoff are failed by `FailStaleProcessingPackages`. This
  runs when the packager worker starts so the normal claim path can retry them.
- **Retry from failed**: Workers do not auto-discover failed rows. Operators
  retry them through the admin queue path, which resets the row to `pending`.
- **Repair from ready**: Playback artifact 404s and worker integrity sweeps can
  mark a `ready` row back to `pending` with a reason and clear stale segment
  metadata.
- **No worker retry from ready**: `ClaimPackage` explicitly skips `ready`
  packages. A second encode runs only after a repair path changes state first.

---

## Coordination Through DB State

### Scheduling Flow

1. **Scheduler** queries `EligibleReadyPackagedChannelMedia(channelID, profile)` which returns only media with:
   - `codec_check_passed = 1`
   - `status = ready` for the required profile
   - `packaged_duration_ms NOT NULL`

2. **Scheduler** builds schedule entries from eligible media, respecting block ordering and existing group cursors.

3. **Scheduler** inserts entries with `InsertScheduleEntries` (transactional, aligned validation).

### Packaging Flow

1. **Packager** queries unready media for a given channel/profile using `EligibleReadyPackagedChannelMedia` (to find content gaps) or scans media without ready packages.

2. **Packager** claims work via `ClaimPackage`:
   - Atomically creates/claims a package row
   - Returns `true` only if the transition succeeded
   - Skips already-processing or ready packages

3. **Packager** runs ffmpeg, updates state via `MarkPackageProcessing` → `MarkPackageReady` (or `MarkPackageFailed`).

4. **Packager** writes segment metadata via `ReplacePackagedSegments`.

### Playback Flow

1. **linearcast** reads `schedule_entries` for the current time window via `ScheduleWindow`.

2. For each entry, **linearcast** looks up the ready package via `ReadyMediaPackage`.

3. **linearcast** records the current entry in `play_history` once the ready package is resolved.

4. **linearcast** constructs the HLS manifest using exact durations from `packaged_segments`.

5. **linearcast** serves segments from local cache (packaged artifacts). If a
   referenced artifact is missing, it marks the ready package back to `pending`
   so the packager worker can rebuild it.

---

## Common Pitfalls

### 1. Scheduling unready media

Do not query `EligibleChannelMedia` (the non-package-aware version) for a packaged channel. Use `EligibleReadyPackagedChannelMedia` to ensure scheduled entries have playable packages.

### 2. Misaligned schedule times

Always use 6000ms-aligned schedule-grid values. The schema CHECK catches this, but failing early in the insert is cleaner than debugging playback gaps.

### 3. Stale package state

If a worker crashes mid-encode, the package stays in `processing` until stale
detection fails it. `FailStaleProcessingPackages` runs at worker startup.

### 4. Missing package artifacts

Do not manually edit `media_packages` to recover deleted `init.mp4` or segment
files. Use the worker integrity sweep or let playback artifact 404 handling
move the ready row back to `pending`.

### 5. Segment duration mismatch

Never write rounded durations to `packaged_segments`. Use the exact encoded duration. Rounding causes manifest/segment mismatch and playback stalls.

### 6. Concurrency conflicts

Multiple packagers using `ClaimPackage` will safely coordinate (only one wins per media/profile), but each should open their own connection. Do not share a single `*sql.DB` across goroutines.

---

## How to Safely Modify DB Logic

> The schema is a single **end-state v1 baseline** with narrow additive startup
> migrations for live data that should be preserved. `collections` and
> `media.collection_id` are created/backfilled idempotently by `ApplySchema`.

### Adding a new table

1. Add the CREATE TABLE (and any indexes) to `schema.sql` directly.
2. Add Go types to `internal/db/types.go` and query functions to the
   appropriate domain file (`channels.go`, `media.go`, `schedule.go`,
   `packages.go`, etc.).
3. Add tests for CRUD operations.

### Changing an existing table

Edit the table definition in `schema.sql` to the desired fresh-database end
state. For existing databases, add an idempotent `ApplySchema` guard that
creates missing additive structures and backfills from existing data. Avoid
destructive table rebuilds unless the user has explicitly accepted a drop and
recreate.

### Modifying package state machine

1. Add new states to `PackageStatus` constants.
2. Update `validPackageTransition` to permit the new path.
3. Update worker code to handle the new state.
4. Add tests for the new transition path.
5. Ensure stale-detection still works for any new processing states.

### Adding a new query

1. Write the SQL in the appropriate domain file in `internal/db/` (e.g.,
   `channels.go` for channel reads/writes, `schedule.go` for schedule
   entries, `packages.go` for package rows). See the file-level split in
   `internal/db/` for existing groupings.
2. Return domain types (e.g., `Channel`, `Media`, `ScheduleEntry`).
3. Add tests for the query.
4. Document in this file.

### Monitoring and Observability Queries

These queries support the monitoring stack (Prometheus metrics + admin API).
They are defined in `internal/db/gaps.go`.

#### `ChannelPackageCoverageMs`

Returns the total `packaged_duration_ms` of all ready packages for a channel
that pass codec checks. Used to surface package coverage horizon per channel.

```sql
SELECT COALESCE(SUM(p.packaged_duration_ms), 0)
FROM channel_media cm
JOIN media m ON m.id = cm.media_id
JOIN media_packages p ON p.media_id = m.id
WHERE cm.channel_id = ?
  AND m.codec_check_passed = 1
  AND p.rendition_profile = ?
  AND p.status = 'ready'
  AND p.packaged_duration_ms IS NOT NULL;
```

#### `ScheduleGaps`

Returns all gaps (periods > 30s between consecutive schedule entries) for a
channel within a time window. Used by the `/api/schedule/gaps` admin endpoint
and the `schedule_gap_count` / `schedule_gap_active` Prometheus metrics.

```sql
WITH entries AS (
    SELECT start_ms, start_ms + duration_ms AS end_ms
    FROM schedule_entries
    WHERE channel_id = ?
      AND start_ms + duration_ms > ?
      AND start_ms < ?
    ORDER BY start_ms
),
with_prev AS (
    SELECT
        start_ms,
        end_ms,
        LAG(end_ms) OVER (ORDER BY start_ms) AS prev_end
    FROM entries
)
SELECT prev_end, start_ms, start_ms - prev_end AS gap_ms
FROM with_prev
WHERE prev_end IS NOT NULL
  AND start_ms - prev_end > 30000
ORDER BY prev_end;
```

The 30-second threshold filters out normal episode-to-episode transitions
(which typically have sub-second gaps) from actual schedule gaps caused by
missing packages or unscheduled time.

### Testing changes

- Use `newTestDB(t)` to create an in-memory test database with schema applied.
- The test DB starts at the current `SchemaVersion`.
- Use `OpenReadWrite` for writes, `OpenReadOnly` for reads.
- Domain tests in `internal/db/*_test.go` use this pattern.
