-- linearcast schema v5.
-- See docs/database.md.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', '5');

-- ordering values are validated in Go (alphabetical, block). No CHECK here
-- so v3-introduced values can land without a table rebuild.
CREATE TABLE IF NOT EXISTS channels (
    id               TEXT PRIMARY KEY,
    display_name     TEXT NOT NULL,
    source_directory TEXT NOT NULL,
    ordering         TEXT NOT NULL,
    enabled          INTEGER NOT NULL,
    created_at_ms    INTEGER NOT NULL,
    description      TEXT,
    hidden_from_guide INTEGER NOT NULL DEFAULT 0,
    artwork_url      TEXT,
    playback_mode    TEXT NOT NULL DEFAULT 'packaged',
    required_package_profile TEXT,
    abr_ladder_json TEXT,
    package_prefill_ms INTEGER,
    encoder_policy TEXT,
    media_kind TEXT NOT NULL DEFAULT 'video',
    schedule_mode TEXT NOT NULL DEFAULT 'back_to_back',
    slot_duration_ms INTEGER,
    upstream_hls_url TEXT,
    prefill_mode TEXT NOT NULL DEFAULT 'eager',
    CHECK (enabled IN (0, 1)),
    CHECK (hidden_from_guide IN (0, 1)),
    CHECK (playback_mode IN ('generated', 'packaged', 'plex_relay')),
    CHECK (package_prefill_ms IS NULL OR package_prefill_ms > 0),
    CHECK (encoder_policy IS NULL OR encoder_policy IN ('any', 'remote_only', 'remote_preferred', 'local_only')),
    CHECK (media_kind IN ('video', 'music')),
    CHECK (schedule_mode IN ('back_to_back', 'slot_grid')),
    CHECK (slot_duration_ms IS NULL OR (slot_duration_ms > 0 AND slot_duration_ms % 6000 = 0)),
    CHECK (prefill_mode IN ('eager', 'on_demand'))
);

-- user_preference is reserved for future continuity-ordering work
-- (e.g. MCU/Star Wars marathon sequences); populated as NULL today.
CREATE TABLE IF NOT EXISTS media (
    id                  TEXT PRIMARY KEY,
    path                TEXT NOT NULL UNIQUE,
    directory           TEXT NOT NULL,
    title               TEXT,
    scheduling_group    TEXT,
    user_preference     INTEGER,
    duration_ms         INTEGER NOT NULL,
    container           TEXT NOT NULL,
    video_codec         TEXT NOT NULL,
    video_height        INTEGER NOT NULL,
    audio_codec         TEXT NOT NULL,
    codec_check_passed  INTEGER NOT NULL,
    codec_check_reason  TEXT,
    ingested_at_ms      INTEGER NOT NULL,
    media_kind          TEXT,
    source_ref          TEXT,
    video_width         INTEGER,
    color_transfer      TEXT,
    color_primaries     TEXT,
    CHECK (codec_check_passed IN (0, 1)),
    CHECK (duration_ms > 0),
    CHECK (media_kind IS NULL OR media_kind IN ('video', 'music'))
);

CREATE INDEX IF NOT EXISTS idx_media_directory ON media(directory);

CREATE TABLE IF NOT EXISTS schedule_entries (
    id            TEXT NOT NULL,
    channel_id    TEXT NOT NULL,
    start_ms      INTEGER NOT NULL,
    media_id      TEXT NOT NULL,
    offset_ms     INTEGER NOT NULL,
    duration_ms   INTEGER NOT NULL,
    created_at_ms INTEGER NOT NULL,
    PRIMARY KEY (id),
    UNIQUE (channel_id, start_ms),
    FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE RESTRICT,
    CHECK (start_ms % 6000 = 0),
    CHECK (duration_ms % 6000 = 0),
    CHECK (offset_ms >= 0),
    CHECK (duration_ms > 0)
);

-- channel_media: per-channel curated playlist. The scheduler reads from
-- this table (not from media.directory + channels.source_directory) when
-- building schedule_entries. anchor_media_id is the linked-list predecessor
-- pointer: each row's place in the channel order is determined by which other
-- row points at it. A NULL anchor marks the head of the chain.
CREATE TABLE IF NOT EXISTS channel_media (
    channel_id       TEXT NOT NULL,
    media_id         TEXT NOT NULL,
    anchor_media_id  TEXT,
    added_at_ms      INTEGER NOT NULL,
    PRIMARY KEY (channel_id, media_id),
    FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
);

-- Unique partial indexes on anchor_media_id (single head per channel, single
-- successor per anchor) are created by migrateV2toV3, not here. Keeping them
-- out of schema.sql avoids referencing anchor_media_id before migrateV1toV2
-- has added the column on a pre-v2 database.

-- filler_assets are global reusable packaged media clips such as bumpers,
-- station IDs, and padding/test-pattern clips. They are attached to channels
-- via channel_filler_assets; they are intentionally not part of channel_media
-- so the scheduler does not cycle them as normal programming.
CREATE TABLE IF NOT EXISTS filler_assets (
    id            TEXT PRIMARY KEY,
    media_id      TEXT NOT NULL UNIQUE,
    label         TEXT NOT NULL,
    kind          TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at_ms INTEGER NOT NULL,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    CHECK (kind IN ('filler', 'bumper', 'station_id')),
    CHECK (enabled IN (0, 1))
);

CREATE TABLE IF NOT EXISTS channel_filler_assets (
    channel_id TEXT NOT NULL,
    asset_id   TEXT NOT NULL,
    weight     INTEGER NOT NULL DEFAULT 1,
    enabled    INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (channel_id, asset_id),
    FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
    FOREIGN KEY (asset_id) REFERENCES filler_assets(id) ON DELETE CASCADE,
    CHECK (weight > 0),
    CHECK (enabled IN (0, 1))
);

CREATE INDEX IF NOT EXISTS idx_channel_filler_assets_channel ON channel_filler_assets(channel_id, enabled, weight);

-- media_packages: normalized/packaged renditions derived from source media.
CREATE TABLE IF NOT EXISTS media_packages (
    id                    TEXT PRIMARY KEY,
    media_id              TEXT NOT NULL,
    rendition_profile     TEXT NOT NULL,
    status                TEXT NOT NULL,
    package_root          TEXT,
    init_segment_path     TEXT,
    segment_base_path     TEXT,
    container             TEXT,
    video_codec           TEXT,
    video_profile         TEXT,
    video_width           INTEGER,
    video_height          INTEGER,
    audio_codec           TEXT,
    audio_profile         TEXT,
    timescale             INTEGER,
    packaged_duration_ms  INTEGER,
    error                 TEXT,
    last_attempt_error    TEXT,
    attempts              INTEGER NOT NULL DEFAULT 0,
    created_at_ms         INTEGER NOT NULL,
    updated_at_ms         INTEGER NOT NULL,
    UNIQUE (media_id, rendition_profile),
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    CHECK (status IN ('pending', 'processing', 'ready', 'failed')),
    CHECK (timescale IS NULL OR timescale > 0),
    CHECK (packaged_duration_ms IS NULL OR packaged_duration_ms > 0),
    CHECK (video_width IS NULL OR video_width > 0),
    CHECK (video_height IS NULL OR video_height > 0),
    CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_media_packages_media ON media_packages(media_id, rendition_profile);
CREATE INDEX IF NOT EXISTS idx_media_packages_status ON media_packages(status);

-- packaged_segments: exact packaged media segment boundaries. Segment identity
-- is package + segment_number, not channel + wall-clock index.
CREATE TABLE IF NOT EXISTS packaged_segments (
    package_id         TEXT NOT NULL,
    segment_number    INTEGER NOT NULL,
    media_start_ms    INTEGER NOT NULL,
    duration_ms       INTEGER NOT NULL,
    path              TEXT,
    byte_range_start  INTEGER,
    byte_range_length INTEGER,
    PRIMARY KEY (package_id, segment_number),
    FOREIGN KEY (package_id) REFERENCES media_packages(id) ON DELETE CASCADE,
    CHECK (segment_number >= 0),
    CHECK (media_start_ms >= 0),
    CHECK (duration_ms > 0),
    CHECK (path IS NOT NULL OR (byte_range_start IS NOT NULL AND byte_range_length IS NOT NULL)),
    CHECK (byte_range_start IS NULL OR byte_range_start >= 0),
    CHECK (byte_range_length IS NULL OR byte_range_length > 0)
);

CREATE INDEX IF NOT EXISTS idx_packaged_segments_position ON packaged_segments(package_id, media_start_ms);

-- play_history: one durable row per schedule entry observed by the playback
-- runtime. Future scheduling/guide features use this as the "has aired" log.
CREATE TABLE IF NOT EXISTS play_history (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id        TEXT NOT NULL,
    schedule_entry_id TEXT NOT NULL,
    media_id          TEXT NOT NULL,
    started_at        INTEGER NOT NULL,
    ended_at          INTEGER NOT NULL,
    duration_ms       INTEGER NOT NULL,
    UNIQUE (channel_id, schedule_entry_id),
    FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE RESTRICT,
    CHECK (ended_at >= started_at),
    CHECK (duration_ms >= 0)
);

CREATE INDEX IF NOT EXISTS idx_play_history_channel_started ON play_history(channel_id, started_at DESC);

-- package_profiles: stored profile definitions. Built-ins are seeded on first
-- migration; custom profiles are inserted via the admin API.
CREATE TABLE IF NOT EXISTS package_profiles (
    name             TEXT PRIMARY KEY,
    is_builtin       INTEGER NOT NULL DEFAULT 0,
    disabled         INTEGER NOT NULL DEFAULT 0,
    profile_json     TEXT NOT NULL,
    created_at_ms    INTEGER NOT NULL,
    updated_at_ms    INTEGER NOT NULL,
    CHECK (is_builtin IN (0, 1)),
    CHECK (disabled IN (0, 1))
);

CREATE INDEX IF NOT EXISTS idx_package_profiles_builtin ON package_profiles(is_builtin);

-- admin_write_log: control-plane observability for operator write actions.
-- Records what was asked, not whether downstream jobs completed.
-- Rows are append-only; no updates or deletes from application code.
CREATE TABLE IF NOT EXISTS admin_write_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at_ms INTEGER NOT NULL,
    method        TEXT NOT NULL,
    path          TEXT NOT NULL,
    action        TEXT,
    target_type   TEXT,
    target_id     TEXT,
    status        INTEGER NOT NULL,
    duration_ms   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_admin_write_log_created ON admin_write_log(created_at_ms DESC);


-- media_tracks: subtitle (and future audio) tracks extracted from source
-- media at package time. Subtitle tracks store a WebVTT sidecar at path.
-- source discriminates the provenance: embedded_text (text sub extracted from
-- the source file by ffmpeg into a VTT sidecar), embedded_bitmap (bitmap sub —
-- PGS/VOBSUB — known to exist in the source but not extractable to text, so
-- path IS NULL; an inventory row that makes the track visible for later burn-in
-- or external fetch), opensubtitles (downloaded from OS API), or manual.
-- A path=NULL opensubtitles row is a "no good match" sentinel that prevents
-- re-querying the API on every backfill run. forced marks a forced-display
-- subtitle (foreign-dialogue) track; load-bearing for the future forced
-- bitmap burn-in slice.
CREATE TABLE IF NOT EXISTS media_tracks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id     TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK(kind IN ('subtitle', 'audio')),
    stream_index INTEGER NOT NULL DEFAULT -1,
    language     TEXT,
    codec        TEXT,
    source       TEXT NOT NULL DEFAULT 'embedded_text'
                     CHECK(source IN ('embedded_text', 'embedded_bitmap', 'opensubtitles', 'manual')),
    default_flag INTEGER NOT NULL DEFAULT 0 CHECK(default_flag IN (0, 1)),
    forced       INTEGER NOT NULL DEFAULT 0 CHECK(forced IN (0, 1)),
    path         TEXT,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
);

-- Embedded tracks (text or bitmap) are unique per source stream: one row per
-- (media, kind, stream_index). Both embedded sources share this index so a
-- bitmap inventory row and a text sidecar cannot collide on the same stream.
CREATE UNIQUE INDEX IF NOT EXISTS idx_media_tracks_embedded
    ON media_tracks(media_id, kind, stream_index)
    WHERE source IN ('embedded_text', 'embedded_bitmap');

-- External tracks (opensubtitles/manual) are unique per (media, language,
-- source); they carry stream_index = -1. Embedded sources are excluded here so
-- two same-language bitmap streams stay distinct under the embedded index above.
CREATE UNIQUE INDEX IF NOT EXISTS idx_media_tracks_external
    ON media_tracks(media_id, language, source)
    WHERE source NOT IN ('embedded_text', 'embedded_bitmap') AND kind = 'subtitle';

CREATE INDEX IF NOT EXISTS idx_media_tracks_media ON media_tracks(media_id, kind);

-- settings: user/runtime configuration stored as JSON-encoded values.
-- Separate from meta (schema infrastructure) — different lifecycle.
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO settings (key, value) VALUES
    ('subtitle_language_preference', '["eng"]'),
    ('subtitle_auto_enable',         'false'),
    ('encoder_mode',                 '"local"'),
    ('local_worker_concurrency',     '1'),
    ('default_packaged_profile',     '"h264-main-1080p"'),
    ('scheduler_horizon_hours',      '24'),
    ('scheduler_low_water_hours',    '23'),
    ('scheduler_tick_seconds',       '300'),
    ('encoder_sweep_interval_seconds', '30'),
    ('encoder_max_attempts',           '5'),
    ('on_demand_grace_seconds',        '120'),
    ('on_demand_max_concurrent',       '4'),
    ('on_demand_evict_idle_seconds',   '10'),
    ('on_demand_stall_timeout_seconds','45'),
    ('on_demand_restart_budget',       '3'),
    ('on_demand_keepalive_ceiling_sec','900');

-- local media sources: persistent filesystem-backed media roots. These are
-- intentionally separate from Plex/Jellyfin singleton settings.
CREATE TABLE IF NOT EXISTS local_media_sources (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    media_kind    TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    CHECK (media_kind IN ('movies', 'shows', 'music', 'filler'))
);

CREATE TABLE IF NOT EXISTS local_media_source_paths (
    source_id TEXT NOT NULL,
    path      TEXT NOT NULL,
    sort_key  INTEGER NOT NULL,
    PRIMARY KEY (source_id, path),
    FOREIGN KEY (source_id) REFERENCES local_media_sources(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_local_media_source_paths_source
    ON local_media_source_paths(source_id, sort_key, path);

-- encoders: registered remote encoders. Each row owns an api key hash.
-- Revoke flips the row to offline so the key stops working but the row
-- stays. Delete removes the row outright and releases any active leases
-- back to pending; encoders are ephemeral, packages are tied to profiles
-- not encoders, so there's no audit trail to preserve here.
CREATE TABLE IF NOT EXISTS encoders (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    api_key_hash    TEXT NOT NULL,
    capabilities    TEXT NOT NULL,
    last_seen_ms    INTEGER NOT NULL,
    status          TEXT NOT NULL,
    created_at_ms   INTEGER NOT NULL,
    revoked_at_ms   INTEGER,
    concurrency     INTEGER NOT NULL DEFAULT 1,
    CHECK (status IN ('pending', 'online', 'draining', 'offline')),
    CHECK (concurrency >= 0)
);

CREATE INDEX IF NOT EXISTS idx_encoders_status_seen ON encoders(status, last_seen_ms);

-- encoder_jobs: per-job lease. 1:1 with media_packages while status='processing'.
-- Heartbeats touch this table (not media_packages) so the playback-read path
-- stays free of write contention from polling. Sweeper deletes rows whose
-- lease_expires_ms has passed and transitions the corresponding media_packages
-- row back to pending (transient attempt failure) or to failed (cap exceeded).
CREATE TABLE IF NOT EXISTS encoder_jobs (
    package_id        TEXT PRIMARY KEY,
    encoder_id        TEXT NOT NULL,
    claimed_at_ms     INTEGER NOT NULL,
    lease_expires_ms  INTEGER NOT NULL,
    last_heartbeat_ms INTEGER NOT NULL,
    progress_pct      INTEGER,
    FOREIGN KEY (package_id) REFERENCES media_packages(id) ON DELETE CASCADE,
    FOREIGN KEY (encoder_id) REFERENCES encoders(id) ON DELETE RESTRICT,
    CHECK (progress_pct IS NULL OR (progress_pct >= 0 AND progress_pct <= 100))
);

CREATE INDEX IF NOT EXISTS idx_encoder_jobs_lease ON encoder_jobs(lease_expires_ms);
CREATE INDEX IF NOT EXISTS idx_encoder_jobs_encoder ON encoder_jobs(encoder_id);

-- subtitle_scan_cache: persists the most recent library subtitle scan result.
-- A single row is kept; on_conflict replaces it with the latest scan.
CREATE TABLE IF NOT EXISTS subtitle_scan_cache (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    scanned_at_ms INTEGER NOT NULL,
    status      TEXT NOT NULL,
    shows_json  TEXT NOT NULL DEFAULT '[]'
);
