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
