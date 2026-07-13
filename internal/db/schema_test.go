package db

import (
	"context"
	"strings"
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
		"media_packages", "packaged_segments", "package_tracks",
		"package_profiles", "play_history", "admin_write_log",
		"settings", "local_media_sources", "filler_assets",
	} {
		if err := requireTable(context.Background(), rw, table); err != nil {
			t.Errorf("missing table: %v", err)
		}
	}
	if err := requireTable(context.Background(), rw, "media_tracks"); err == nil {
		t.Errorf("obsolete media_tracks table still exists")
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

func TestApplySchemaDropsObsoleteMediaTracks(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`CREATE TABLE media_tracks (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create obsolete table: %v", err)
	}
	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := requireTable(context.Background(), rw, "media_tracks"); err == nil {
		t.Fatal("obsolete media_tracks table still exists")
	}
	if err := requireTable(context.Background(), rw, "package_tracks"); err != nil {
		t.Fatalf("package_tracks missing: %v", err)
	}
}

func TestApplySchemaAddsMediaSeasonEpisodeColumns(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rows, err := rw.Query(`PRAGMA table_info(media)`)
	if err != nil {
		t.Fatalf("table_info(media): %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "season_number" || name == "episode_number" {
			seen[name] = true
			if strings.ToUpper(typ) != "INTEGER" {
				t.Fatalf("%s type=%q, want INTEGER", name, typ)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if !seen["season_number"] || !seen["episode_number"] {
		t.Fatalf("missing media columns: seen=%v", seen)
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
		{`SELECT COUNT(*) FROM package_profiles WHERE name = 'h264-1080p-8mbps' AND is_builtin = 1`, 1},
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

func TestApplySchemaDropsSubtitleIdentity(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	// newTestDB already applied the current schema. Replace media_packages +
	// packaged_segments with the pre-change shape: the
	// subtitle_identity column with the broadened (media, profile, identity)
	// unique key, plus an identity package whose id and child segments must be
	// swept by the down-migration.
	if _, err := rw.Exec(`
		INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0);
		DROP TABLE packaged_segments;
		DROP TABLE media_packages;
		CREATE TABLE media_packages (
			id                    TEXT PRIMARY KEY,
			media_id              TEXT NOT NULL,
			rendition_profile     TEXT NOT NULL,
			subtitle_identity     TEXT NOT NULL DEFAULT '',
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
			package_bytes         INTEGER,
			error                 TEXT,
			last_attempt_error    TEXT,
			attempts              INTEGER NOT NULL DEFAULT 0,
			created_at_ms         INTEGER NOT NULL,
			updated_at_ms         INTEGER NOT NULL,
			UNIQUE (media_id, rendition_profile, subtitle_identity)
		);
		CREATE TABLE packaged_segments (
			package_id        TEXT NOT NULL,
			segment_number    INTEGER NOT NULL,
			media_start_ms    INTEGER NOT NULL,
			duration_ms       INTEGER NOT NULL,
			path              TEXT,
			byte_range_start  INTEGER,
			byte_range_length INTEGER,
			PRIMARY KEY (package_id, segment_number),
			FOREIGN KEY (package_id) REFERENCES media_packages(id) ON DELETE CASCADE
		);
		INSERT INTO media_packages (id, media_id, rendition_profile, subtitle_identity, status, created_at_ms, updated_at_ms)
		VALUES ('m1-h264', 'm1', 'h264', '', 'ready', 0, 0),
		       ('m1-h264-burn', 'm1', 'h264', 'burn:forced_disposition:s2:eng', 'ready', 0, 0);
		INSERT INTO packaged_segments (package_id, segment_number, media_start_ms, duration_ms, path)
		VALUES ('m1-h264', 0, 0, 6000, '/cache/m1/h264/seg000000.m4s'),
		       ('m1-h264-burn', 0, 0, 6000, '/cache/m1/m1-h264-burn/seg000000.m4s')`); err != nil {
		t.Fatalf("seed legacy media_packages: %v", err)
	}

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	// The column is gone.
	if has, err := tableHasColumn(context.Background(), rw, "media_packages", "subtitle_identity"); err != nil {
		t.Fatalf("table_info: %v", err)
	} else if has {
		t.Fatal("subtitle_identity column should be dropped")
	}

	// The ordinary package survives; the identity package and its segments are swept.
	assertCount := func(query, label string, want int) {
		t.Helper()
		var got int
		if err := rw.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", label, got, want)
		}
	}
	assertCount(`SELECT COUNT(*) FROM media_packages WHERE id = 'm1-h264'`, "ordinary row kept", 1)
	assertCount(`SELECT COUNT(*) FROM media_packages WHERE id = 'm1-h264-burn'`, "identity row dropped", 0)
	assertCount(`SELECT COUNT(*) FROM packaged_segments WHERE package_id = 'm1-h264'`, "ordinary segments kept", 1)
	assertCount(`SELECT COUNT(*) FROM packaged_segments WHERE package_id = 'm1-h264-burn'`, "identity segments dropped", 0)

	// The unique key is back to (media_id, rendition_profile): a second row for
	// the same pair is now rejected.
	if _, err := rw.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('m1-h264-dup', 'm1', 'h264', 'ready', 0, 0)`); err == nil {
		t.Fatal("duplicate (media_id, rendition_profile) insert should violate the unique key")
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

func TestApplySchemaNormalizesLegacyHiddenFromGuideNull(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`
		DROP TABLE channels;
		CREATE TABLE channels (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			source_directory TEXT NOT NULL,
			ordering TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			created_at_ms INTEGER NOT NULL,
			description TEXT,
			hidden_from_guide INTEGER,
			artwork_url TEXT,
			playback_mode TEXT NOT NULL DEFAULT 'packaged',
			required_package_profile TEXT,
			abr_ladder_json TEXT,
			package_prefill_ms INTEGER,
			encoder_policy TEXT,
			media_kind TEXT NOT NULL DEFAULT 'video',
			schedule_mode TEXT NOT NULL DEFAULT 'back_to_back',
			slot_duration_ms INTEGER,
			upstream_hls_url TEXT,
			prefill_mode TEXT NOT NULL DEFAULT 'eager'
		);
		INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			hidden_from_guide
		)
		VALUES ('legacy', 'Legacy', '/tmp', 'alphabetical', 1, 0, NULL)`); err != nil {
		t.Fatalf("seed legacy channel: %v", err)
	}

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	ch, err := ChannelByID(context.Background(), rw, "legacy")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil {
		t.Fatal("channel not found")
	}
	if ch.HiddenFromGuide {
		t.Error("hidden_from_guide should normalize to false")
	}
}
