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

- **linearcast** opens the database read-write for a narrow repair path. It
  serves packaged media and may mark a `ready` package back to `pending` when a
  packaged init/segment artifact is missing on disk.
- **linearcast-extender** and **linearcast-packager** open read-write (`OpenReadWrite`). They coordinate through SQLite's WAL mode and busy timeout (5s).
- **linearcast-admin** and maintenance tools are also read-write for channel/media CRUD, migrations, diagnostics, and repair workflows.

---

## Ownership Boundaries

| Component | Writes | Reads |
|-----------|--------|-------|
| linearcast | media_packages, packaged_segments (ready artifact repair only) | All tables; exports metrics on 60s refresh tick |
| linearcast-extender | schedule_entries | channels, media, channel_media, media_packages |
| linearcast-packager | media_packages, packaged_segments | media |
| linearcast-admin | channels, channel_media | All tables (read-write for Plex imports and channel management) |
| cmd/ingest | media | channels, media |

No component writes schedule entries except the scheduler. Package state writes
belong to the packager except for the narrow `linearcast` ready-artifact repair
path and explicit admin retry requests. Metrics are exported from
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
| required_package_profile | TEXT | Package profile (e.g., `h264-main-1080p`) |
| package_prefill_ms | INTEGER | Package coverage horizon in ms |

**Key invariant**: `playback_mode` is always `packaged`. The system does not support generated playback.

### media

Ingested media items with codec validation metadata.

| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | Media identifier (derived from path) |
| path | TEXT | Full file path |
| directory | TEXT | Containing directory |
| title | TEXT | Optional title |
| scheduling_group | TEXT | Series/group identifier for block ordering |
| user_preference | INTEGER | Per-media priority (higher = preferred) |
| duration_ms | INTEGER | Source duration in milliseconds |
| container | TEXT | File container (mkv, mp4, etc.) |
| video_codec | TEXT | Video codec (h264, hevc) |
| video_height | INTEGER | Video resolution height |
| audio_codec | TEXT | Audio codec (aac, etc.) |
| codec_check_passed | INTEGER | 1 = passed, 0 = rejected |
| codec_check_reason | TEXT | Rejection reason if failed |
| ingested_at_ms | INTEGER | Ingest timestamp |

**Key invariant**: Only media with `codec_check_passed = 1` is eligible for scheduling.

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

**Key invariant**: `start_ms` and `duration_ms` must be aligned to 6000ms (6-second segments). The CHECK constraint enforces this in the schema.

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

### media_packages

Package state machine tracking pre-transcoded media.

| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | Package identifier |
| media_id | TEXT FK | Source media |
| rendition_profile | TEXT | Package profile key (e.g., `h264-main-1080p`) |
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
| error | TEXT | Failure message if status = failed |
| created_at_ms | INTEGER | Creation timestamp |
| updated_at_ms | INTEGER | Last update timestamp |

`rendition_profile` is matched exactly by scheduler/playback eligibility. It is
the durable package key used in package IDs and filesystem paths, and executable
packager behavior is resolved through the package profile registry. Historical
fake profiles such as `h264-maindfdfdf-1080p` can still exist as ready packages
and consume cache, but they are invalid for new encode work and will not satisfy
a channel requiring `h264-main-1080p`.

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

### package_profiles

First-class profile definitions for packaging behavior. Built-in profiles are seeded at schema initialization.

| Column | Type | Description |
|--------|------|-------------|
| name | TEXT PK | Profile identifier (e.g., `h264-main-1080p`) |
| is_builtin | INTEGER | 1 = built-in, 0 = custom |
| profile_json | TEXT | Encoder configuration as JSON |
| created_at_ms | INTEGER | Creation timestamp |
| updated_at_ms | INTEGER | Last update timestamp |

The seeded video profile is `h264-main-1080p` (H.264 Main level 4.1, 1080p
scale-down, 8 Mbps maxrate, AAC stereo). Profile
names are the durable package key and appear in
`media_packages.rendition_profile`. All encode work, channel policy writes, and
manual queue endpoints validate against the active registry allow-list.

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

1. **Schedule only references ready packages.** The scheduler's `EligibleReadyPackagedChannelMedia` query joins `channel_media` → `media` → `media_packages` with `status = ready`. Unpackaged or in-progress media is not scheduled.

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

Always use 6000ms-aligned values. The schema CHECK catches this, but failing early in the insert is cleaner than debugging playback gaps.

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

### Adding a new table

1. Add the CREATE TABLE to `schema.sql` (additive only).
2. Add a version-gated migration in `migrations.go` if existing databases need
   version metadata or backfill work.
3. Add Go types to `internal/db/types.go` and query functions to the
   appropriate domain file (`channels.go`, `media.go`, `schedule.go`,
   `packages.go`, etc.).
4. Add tests for CRUD operations.

### Changing an existing table

1. **If adding columns**: Use `ALTER TABLE ... ADD COLUMN`. The migration pattern handles idempotency (`IF NOT EXISTS` or catching "duplicate column name").
2. **If changing constraints**: Consider migration safety. CHECK constraints are enforced at write time; foreign keys are enforced at write time (configured `_pragma=foreign_keys(on)`).
3. **If removing columns**: Requires a migration and is not additive. Consider backward compatibility or a schema version bump.

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
