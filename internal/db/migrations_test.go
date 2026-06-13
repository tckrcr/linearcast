package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestApplySchemaFreshDB(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := VerifySchema(context.Background(), rw); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}

	for _, table := range []string{
		"channels", "media", "channel_media", "schedule_entries",
		"media_packages", "packaged_segments", "media_tracks",
		"package_profiles", "play_history", "admin_write_log",
		"settings", "local_media_sources", "filler_assets",
	} {
		if err := requireTable(context.Background(), rw, table); err != nil {
			t.Errorf("missing table: %v", err)
		}
	}
}

func TestApplySchemaIdempotent(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	for i := range 3 {
		if err := ApplySchema(context.Background(), rw); err != nil {
			t.Fatalf("ApplySchema call %d: %v", i+1, err)
		}
	}
	if err := VerifySchema(context.Background(), rw); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
}

func TestApplySchemaPrunesRemovedBuiltinProfiles(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO package_profiles (name, is_builtin, disabled, profile_json, created_at_ms, updated_at_ms)
		VALUES ('removed-builtin', 1, 0, '{}', 1, 1)`); err != nil {
		t.Fatalf("insert removed builtin: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO package_profiles (name, is_builtin, disabled, profile_json, created_at_ms, updated_at_ms)
		VALUES ('custom-profile', 0, 0, '{}', 1, 1)`); err != nil {
		t.Fatalf("insert custom profile: %v", err)
	}

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema rerun: %v", err)
	}

	for _, tc := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM package_profiles WHERE name = 'removed-builtin'`, 0},
		{`SELECT COUNT(*) FROM package_profiles WHERE name = 'custom-profile'`, 1},
		{`SELECT COUNT(*) FROM package_profiles WHERE name = 'h264-main-1080p' AND is_builtin = 1`, 1},
	} {
		var got int
		if err := rw.QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatalf("count query: %v", err)
		}
		if got != tc.want {
			t.Fatalf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}
}

func TestChannelHiddenFromGuideDefault(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	ch, err := ChannelByID(context.Background(), rw, "ch1")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil {
		t.Fatal("channel not found")
	}
	if ch.HiddenFromGuide {
		t.Error("hidden_from_guide should default to false")
	}
}

// TestMigrateV1toV2Backfill simulates an existing v1 DB by dropping the
// anchor_media_id column after schema.sql runs, resetting schema_version to 1,
// inserting channel_media rows in a known sort_key order, then re-running
// ApplySchema and verifying the linked list was reconstructed correctly.
func TestMigrateV1toV2Backfill(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema (initial): %v", err)
	}

	// Simulate a v1 DB: drop the v3 uniqueness indexes, drop the
	// anchor_media_id column added by v2, and re-add sort_key (which v4
	// removed). Then rewind schema_version so the full migration chain re-runs.
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_channel_media_anchor`,
		`DROP INDEX IF EXISTS idx_channel_media_head`,
		`ALTER TABLE channel_media DROP COLUMN anchor_media_id`,
		`ALTER TABLE channel_media ADD COLUMN sort_key TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := rw.Exec(stmt); err != nil {
			t.Fatalf("simulate v1 (%s): %v", stmt, err)
		}
	}

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	for _, m := range []struct {
		id, sortKey string
	}{
		{"m_a", "0001"},
		{"m_b", "0002"},
		{"m_c", "0003"},
	} {
		if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', 1000, 'mp4', 'h264', 1080, 'aac', 1, 0)`, m.id, "/tmp/"+m.id); err != nil {
			t.Fatalf("insert media %s: %v", m.id, err)
		}
		if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, sort_key, added_at_ms)
			VALUES ('ch1', ?, ?, 0)`, m.id, m.sortKey); err != nil {
			t.Fatalf("insert channel_media %s: %v", m.id, err)
		}
	}

	if _, err := rw.Exec(`UPDATE meta SET value = '1' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("reset schema_version: %v", err)
	}

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema (migration): %v", err)
	}
	if err := VerifySchema(context.Background(), rw); err != nil {
		t.Fatalf("VerifySchema after migration: %v", err)
	}

	anchors := readAnchors(t, rw, "ch1")
	want := map[string]sql.NullString{
		"m_a": {Valid: false},
		"m_b": {String: "m_a", Valid: true},
		"m_c": {String: "m_b", Valid: true},
	}
	for mediaID, expected := range want {
		got, ok := anchors[mediaID]
		if !ok {
			t.Errorf("media %s missing from channel_media", mediaID)
			continue
		}
		if got != expected {
			t.Errorf("media %s anchor = %+v, want %+v", mediaID, got, expected)
		}
	}
}

// TestMigrateV1toV2Idempotent: running ApplySchema again on an already-migrated
// DB must not alter existing anchor_media_id values.
func TestMigrateV1toV2Idempotent(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema (initial): %v", err)
	}

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m_a', '/tmp/a', '/tmp', 1000, 'mp4', 'h264', 1080, 'aac', 1, 0),
		       ('m_b', '/tmp/b', '/tmp', 1000, 'mp4', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	// Hand-set anchors so b is the head and a points at b. A rerun of
	// ApplySchema (already at v4) must not clobber these.
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm_a', 'm_b', 0),
		       ('ch1', 'm_b', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema (rerun): %v", err)
	}

	anchors := readAnchors(t, rw, "ch1")
	if anchors["m_a"] != (sql.NullString{String: "m_b", Valid: true}) {
		t.Errorf("m_a anchor mutated: %+v", anchors["m_a"])
	}
	if anchors["m_b"] != (sql.NullString{Valid: false}) {
		t.Errorf("m_b anchor mutated: %+v", anchors["m_b"])
	}
}

func TestMigrateV12toV13BackfillScheduleEntryAnchors(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema (initial): %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m2', '/tmp/m2.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m3', '/tmp/m3.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	entries := []ScheduleEntry{
		{ID: "se1", ChannelID: "ch1", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ID: "se2", ChannelID: "ch1", StartMs: 6000, MediaID: "m2", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ID: "se3", ChannelID: "ch1", StartMs: 12000, MediaID: "m3", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
	}
	if _, err := InsertScheduleEntries(context.Background(), rw, entries); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	if _, err := rw.Exec(`DROP INDEX IF EXISTS idx_schedule_entries_head`); err != nil {
		t.Fatalf("drop head index: %v", err)
	}
	if _, err := rw.Exec(`DROP INDEX IF EXISTS idx_schedule_entries_anchor`); err != nil {
		t.Fatalf("drop anchor index: %v", err)
	}
	if _, err := rw.Exec(`ALTER TABLE schedule_entries DROP COLUMN anchor_schedule_entry_id`); err != nil {
		t.Fatalf("drop anchor column: %v", err)
	}
	if _, err := rw.Exec(`UPDATE meta SET value = '12' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("rewind schema_version: %v", err)
	}

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema (migration): %v", err)
	}
	if err := VerifySchema(context.Background(), rw); err != nil {
		t.Fatalf("VerifySchema after migration: %v", err)
	}

	want := map[string]string{
		"se1": "",
		"se2": "se1",
		"se3": "se2",
	}
	for entryID, expected := range want {
		got, err := ScheduleEntryByID(context.Background(), rw, entryID)
		if err != nil {
			t.Fatalf("lookup %s: %v", entryID, err)
		}
		if got == nil {
			t.Fatalf("schedule entry %s missing after migration", entryID)
		}
		gotAnchor := ""
		if got.AnchorScheduleEntryID != nil {
			gotAnchor = *got.AnchorScheduleEntryID
		}
		if gotAnchor != expected {
			t.Fatalf("entry %s anchor=%q, want %q", entryID, gotAnchor, expected)
		}
	}
	issues, err := ValidateScheduleEntryChains(context.Background(), rw)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("validate chains returned issues: %+v", issues)
	}
}

// TestMigrateV25toV26BackfillEntryKind simulates a pre-v26 database (every
// schedule entry at the 'primary' default) and verifies the migration flips
// only entries whose media is not in the channel's channel_media chain to
// 'filler', matching the old membership-based inference.
func TestMigrateV25toV26BackfillEntryKind(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	ctx := context.Background()

	if err := ApplySchema(ctx, rw); err != nil {
		t.Fatalf("ApplySchema (initial): %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('mf', '/tmp/mf.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	// m1 is primary programming (in channel_media); mf is attached filler (not in channel_media).
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}
	entries := []ScheduleEntry{
		{ID: "se1", ChannelID: "ch1", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ID: "se2", ChannelID: "ch1", StartMs: 6000, MediaID: "mf", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
	}
	if _, err := InsertScheduleEntries(ctx, rw, entries); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	// Rewind to a pre-v26 state: every row at the 'primary' default.
	if _, err := rw.Exec(`UPDATE schedule_entries SET entry_kind = 'primary'`); err != nil {
		t.Fatalf("reset entry_kind: %v", err)
	}
	if _, err := rw.Exec(`UPDATE meta SET value = '25' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("rewind schema_version: %v", err)
	}

	if err := ApplySchema(ctx, rw); err != nil {
		t.Fatalf("ApplySchema (migration): %v", err)
	}
	if err := VerifySchema(ctx, rw); err != nil {
		t.Fatalf("VerifySchema after migration: %v", err)
	}

	want := map[string]string{"se1": "primary", "se2": "filler"}
	for id, expected := range want {
		var kind string
		if err := rw.QueryRow(`SELECT entry_kind FROM schedule_entries WHERE id = ?`, id).Scan(&kind); err != nil {
			t.Fatalf("read entry_kind %s: %v", id, err)
		}
		if kind != expected {
			t.Fatalf("entry %s entry_kind=%q, want %q", id, kind, expected)
		}
	}
}

// TestMigrateV26toV27LowersSchedulerHorizonDefault verifies a deployment on the
// old 48h/24h default is rolled to 24h/23h, while an operator's custom horizon is
// left untouched (with low-water clamped to stay below it).
func TestMigrateV26toV27LowersSchedulerHorizonDefault(t *testing.T) {
	ctx := context.Background()

	t.Run("rolls old default to 24/23", func(t *testing.T) {
		rw, err := OpenReadWrite(newTestDB(t))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer rw.Close()
		if err := ApplySchema(ctx, rw); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		// Simulate a pre-v27 deployment still on the old default.
		if _, err := rw.Exec(`UPDATE settings SET value='48' WHERE key='scheduler_horizon_hours';
			UPDATE settings SET value='24' WHERE key='scheduler_low_water_hours';
			UPDATE meta SET value='26' WHERE key='schema_version';`); err != nil {
			t.Fatalf("rewind: %v", err)
		}
		if err := ApplySchema(ctx, rw); err != nil {
			t.Fatalf("ApplySchema (migration): %v", err)
		}
		got, err := GetSchedulerTunables(ctx, rw)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.HorizonHours != 24 || got.LowWaterHours != 23 {
			t.Fatalf("got horizon=%d low_water=%d, want 24/23", got.HorizonHours, got.LowWaterHours)
		}
	})

	t.Run("preserves a custom horizon, clamps low-water below it", func(t *testing.T) {
		rw, err := OpenReadWrite(newTestDB(t))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer rw.Close()
		if err := ApplySchema(ctx, rw); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		// Operator override: horizon 72 (not the old default), low-water 36.
		if _, err := rw.Exec(`UPDATE settings SET value='72' WHERE key='scheduler_horizon_hours';
			UPDATE settings SET value='36' WHERE key='scheduler_low_water_hours';
			UPDATE meta SET value='26' WHERE key='schema_version';`); err != nil {
			t.Fatalf("rewind: %v", err)
		}
		if err := ApplySchema(ctx, rw); err != nil {
			t.Fatalf("ApplySchema (migration): %v", err)
		}
		got, err := GetSchedulerTunables(ctx, rw)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		// Custom horizon untouched; low-water (36 < 72) stays valid and untouched.
		if got.HorizonHours != 72 || got.LowWaterHours != 36 {
			t.Fatalf("got horizon=%d low_water=%d, want 72/36 preserved", got.HorizonHours, got.LowWaterHours)
		}
	})
}

func TestMigrateV27toV28PreservesChannelsWithOldColumnOrder(t *testing.T) {
	ctx := context.Background()
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(ctx, rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	for _, stmt := range []string{
		`DROP TABLE channels`,
		`CREATE TABLE channels (
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
			package_prefill_ms INTEGER,
			encoder_policy TEXT,
			media_kind TEXT NOT NULL DEFAULT 'video',
			upstream_hls_url TEXT,
			schedule_mode TEXT NOT NULL DEFAULT 'back_to_back',
			slot_duration_ms INTEGER,
			prefill_mode TEXT NOT NULL DEFAULT 'eager',
			CHECK (enabled IN (0, 1)),
			CHECK (hidden_from_guide IN (0, 1)),
			CHECK (playback_mode IN ('generated', 'packaged')),
			CHECK (package_prefill_ms IS NULL OR package_prefill_ms > 0),
			CHECK (encoder_policy IS NULL OR encoder_policy IN ('any', 'remote_only', 'remote_preferred', 'local_only')),
			CHECK (media_kind IN ('video', 'music')),
			CHECK (schedule_mode IN ('back_to_back', 'slot_grid')),
			CHECK (slot_duration_ms IS NULL OR (slot_duration_ms > 0 AND slot_duration_ms % 6000 = 0)),
			CHECK (prefill_mode IN ('eager', 'on_demand'))
		)`,
		`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, hidden_from_guide, artwork_url, playback_mode,
			required_package_profile, package_prefill_ms, encoder_policy, media_kind,
			upstream_hls_url, schedule_mode, slot_duration_ms, prefill_mode
		) VALUES (
			'ch1', 'Slot Grid', '/media', 'alphabetical', 1, 123,
			'desc', 1, 'https://example.test/art.jpg', 'packaged',
			'h264-main-1080p', 60000, 'remote_preferred', 'video',
			'https://upstream.test/live.m3u8', 'slot_grid', 12000, 'on_demand'
		)`,
		`UPDATE meta SET value = '27' WHERE key = 'schema_version'`,
	} {
		if _, err := rw.Exec(stmt); err != nil {
			t.Fatalf("simulate v27 (%s): %v", stmt, err)
		}
	}

	if err := ApplySchema(ctx, rw); err != nil {
		t.Fatalf("ApplySchema (migration): %v", err)
	}
	if err := VerifySchema(ctx, rw); err != nil {
		t.Fatalf("VerifySchema after migration: %v", err)
	}

	var playbackMode, upstreamURL, scheduleMode, prefillMode string
	var slotDuration int64
	if err := rw.QueryRow(`SELECT playback_mode, upstream_hls_url, schedule_mode, slot_duration_ms, prefill_mode FROM channels WHERE id = 'ch1'`).
		Scan(&playbackMode, &upstreamURL, &scheduleMode, &slotDuration, &prefillMode); err != nil {
		t.Fatalf("read migrated channel: %v", err)
	}
	if playbackMode != "packaged" || upstreamURL != "https://upstream.test/live.m3u8" ||
		scheduleMode != "slot_grid" || slotDuration != 12000 || prefillMode != "on_demand" {
		t.Fatalf("migrated channel got playback=%q upstream=%q schedule=%q slot=%d prefill=%q",
			playbackMode, upstreamURL, scheduleMode, slotDuration, prefillMode)
	}

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms, playback_mode)
		VALUES ('plex1', 'Plex Relay', '', 'alphabetical', 1, 456, 'plex_relay')`); err != nil {
		t.Fatalf("insert plex_relay channel after migration: %v", err)
	}
}

func TestMigrateV28toV29AddsColorMetadataColumns(t *testing.T) {
	ctx := context.Background()
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(ctx, rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	for _, stmt := range []string{
		`ALTER TABLE media DROP COLUMN video_width`,
		`ALTER TABLE media DROP COLUMN color_transfer`,
		`ALTER TABLE media DROP COLUMN color_primaries`,
		`INSERT INTO media (id, path, directory, duration_ms, container, video_codec,
			video_height, audio_codec, codec_check_passed, ingested_at_ms)
		 VALUES ('m1', '/media/old.mkv', '/media', 60000, 'mkv', 'h264', 1080, 'aac', 1, 123)`,
		`UPDATE meta SET value = '28' WHERE key = 'schema_version'`,
	} {
		if _, err := rw.Exec(stmt); err != nil {
			t.Fatalf("simulate v28 (%s): %v", stmt, err)
		}
	}

	if err := ApplySchema(ctx, rw); err != nil {
		t.Fatalf("ApplySchema (migration): %v", err)
	}
	if err := VerifySchema(ctx, rw); err != nil {
		t.Fatalf("VerifySchema after migration: %v", err)
	}

	// Pre-migration rows must scan with zero values for the new columns.
	m, err := MediaByID(ctx, rw, "m1")
	if err != nil {
		t.Fatalf("MediaByID: %v", err)
	}
	if m == nil || m.VideoWidth != 0 || m.ColorTransfer != "" || m.ColorPrimaries != "" {
		t.Fatalf("migrated row=%+v, want zero-valued color metadata", m)
	}

	if _, err := rw.Exec(`UPDATE media SET video_width = 3840, color_transfer = 'smpte2084',
		color_primaries = 'bt2020' WHERE id = 'm1'`); err != nil {
		t.Fatalf("update color metadata: %v", err)
	}
	m, err = MediaByID(ctx, rw, "m1")
	if err != nil {
		t.Fatalf("MediaByID (after update): %v", err)
	}
	if m.VideoWidth != 3840 || m.ColorTransfer != "smpte2084" || m.ColorPrimaries != "bt2020" {
		t.Fatalf("updated row=%+v, want width=3840 transfer=smpte2084 primaries=bt2020", m)
	}
}

func readAnchors(t *testing.T, db *sql.DB, channelID string) map[string]sql.NullString {
	t.Helper()
	rows, err := db.Query(`SELECT media_id, anchor_media_id FROM channel_media WHERE channel_id = ?`, channelID)
	if err != nil {
		t.Fatalf("query anchors: %v", err)
	}
	defer rows.Close()
	out := map[string]sql.NullString{}
	for rows.Next() {
		var id string
		var anchor sql.NullString
		if err := rows.Scan(&id, &anchor); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = anchor
	}
	return out
}
