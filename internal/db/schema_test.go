package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := Migrate(context.Background(), rw); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := VerifySchema(context.Background(), rw); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}

	for _, table := range requiredSchemaTables[SchemaVersion] {
		var count int
		if err := rw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("find table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s count=%d, want 1", table, count)
		}
	}
	var profiles int
	if err := rw.QueryRow(`SELECT COUNT(*) FROM package_profiles WHERE is_builtin = 1`).Scan(&profiles); err != nil {
		t.Fatalf("count builtin profiles: %v", err)
	}
	if profiles == 0 {
		t.Fatal("fresh migration did not seed builtin profiles")
	}
}

func TestMigrateCurrentDatabaseIsIdempotent(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO settings (key, value) VALUES ('migration_test', '"preserve"')`); err != nil {
		t.Fatalf("seed setting: %v", err)
	}

	for i := range 3 {
		if err := Migrate(context.Background(), rw); err != nil {
			t.Fatalf("Migrate call %d: %v", i+1, err)
		}
	}
	var value string
	if err := rw.QueryRow(`SELECT value FROM settings WHERE key = 'migration_test'`).Scan(&value); err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if value != `"preserve"` {
		t.Fatalf("setting=%q, want preserved value", value)
	}
}

func TestPlanMigrationsFreshAndCurrent(t *testing.T) {
	ctx := context.Background()
	freshPath := filepath.Join(t.TempDir(), "new.db")
	plan, err := PlanMigrations(ctx, freshPath)
	if err != nil {
		t.Fatalf("plan fresh: %v", err)
	}
	if !plan.Fresh || plan.CurrentVersion != 0 || plan.TargetVersion != SchemaVersion || plan.NeedsMigration() {
		t.Fatalf("fresh plan=%+v", plan)
	}

	currentPath := newTestDB(t)
	plan, err = PlanMigrations(ctx, currentPath)
	if err != nil {
		t.Fatalf("plan current: %v", err)
	}
	if plan.Fresh || plan.CurrentVersion != SchemaVersion || plan.TargetVersion != SchemaVersion || plan.NeedsMigration() {
		t.Fatalf("current plan=%+v", plan)
	}

	plan, err = planMigrations(ctx, currentPath, SchemaVersion+1)
	if err != nil {
		t.Fatalf("plan pending: %v", err)
	}
	if plan.Fresh || plan.CurrentVersion != SchemaVersion || plan.TargetVersion != SchemaVersion+1 || !plan.NeedsMigration() {
		t.Fatalf("pending plan=%+v", plan)
	}
}

func TestPlanMigrationsRejectsUnversionedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unversioned.db")
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := rw.Exec(`CREATE TABLE channels (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = PlanMigrations(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "no such table: meta") {
		t.Fatalf("plan error=%v, want unversioned database rejection", err)
	}
}

func TestMigrateV1ChannelsPreservesControlPlaneData(t *testing.T) {
	rw := newV1FixtureDB(t)
	defer rw.Close()
	if _, err := rw.Exec(`
		INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, hidden_from_guide, artwork_url, playback_mode,
			required_package_profile, package_prefill_ms, media_kind,
			schedule_mode, slot_duration_ms, prefill_mode
		) VALUES (
			'ch', 'Channel', '/media', 'block', 1, 123,
			'description', 1, 'https://example.test/art.png', 'packaged',
			'h264-1080p-8mbps', 86400000, 'video',
			'slot_grid', 1800000, 'buffered'
		);
		INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, media_kind
		) VALUES ('music', 'Music', '/music', 'block', 1, 124, 'packaged', '', 'music');
		INSERT INTO media (
			id, path, directory, duration_ms, container, video_codec,
			video_height, audio_codec, codec_check_passed, ingested_at_ms
		) VALUES ('m1', '/media/m1.mkv', '/media', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0);
		INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch', 'm1', NULL, 200);
		INSERT INTO schedule_entries (
			id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms
		) VALUES ('se1', 'ch', 0, 'm1', 0, 6000, 300);
		INSERT INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
		VALUES ('fa1', 'm1', 'Filler', 'filler', 1, 400);
		INSERT INTO channel_filler_assets (channel_id, asset_id, weight, enabled)
		VALUES ('ch', 'fa1', 2, 1)
	`); err != nil {
		t.Fatalf("seed v1 fixture: %v", err)
	}

	if err := Migrate(context.Background(), rw); err != nil {
		t.Fatalf("migrate v1: %v", err)
	}
	if err := VerifySchema(context.Background(), rw); err != nil {
		t.Fatalf("verify v2: %v", err)
	}
	for table, want := range map[string]int{
		"channels":              2,
		"channel_media":         1,
		"schedule_entries":      1,
		"channel_filler_assets": 1,
	} {
		var got int
		if err := rw.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count=%d, want %d", table, got, want)
		}
	}
	ch, err := ChannelByID(context.Background(), rw, "ch")
	if err != nil || ch == nil {
		t.Fatalf("read migrated channel: ch=%+v err=%v", ch, err)
	}
	if ch.DisplayName != "Channel" || ch.Description != "description" || !ch.HiddenFromGuide ||
		ch.RequiredPackageProfile != "h264-1080p-8mbps" || ch.PrefillMode != "eager" ||
		ch.SlotDurationMs == nil || *ch.SlotDurationMs != 1800000 {
		t.Fatalf("migrated channel=%+v", ch)
	}
	music, err := ChannelByID(context.Background(), rw, "music")
	if err != nil || music == nil {
		t.Fatalf("read migrated music channel: channel=%+v err=%v", music, err)
	}
	if music.RequiredPackageProfile != MusicPackageProfile {
		t.Fatalf("music profile=%q, want %q", music.RequiredPackageProfile, MusicPackageProfile)
	}
}

func TestMigrateV1ChannelsRefusesExternalRowsWithoutMutation(t *testing.T) {
	rw := newV1FixtureDB(t)
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO channels (
		id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, upstream_hls_url
	) VALUES ('spotify', 'Spotify', '', 'alphabetical', 1, 0, 'packaged', 'https://example.test/live.m3u8')`); err != nil {
		t.Fatalf("seed external channel: %v", err)
	}

	err := Migrate(context.Background(), rw)
	if err == nil || !strings.Contains(err.Error(), "remove external HLS channel(s) spotify") {
		t.Fatalf("migration error=%v", err)
	}
	var version string
	if err := rw.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != "1" {
		t.Fatalf("version=%q, want 1", version)
	}
	hasPlayback, err := tableHasColumn(context.Background(), rw, "channels", "playback_mode")
	if err != nil || !hasPlayback {
		t.Fatalf("v1 channel table changed: hasPlayback=%v err=%v", hasPlayback, err)
	}
	var count int
	if err := rw.QueryRow(`SELECT COUNT(*) FROM channels WHERE id = 'spotify'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("external row changed: count=%d err=%v", count, err)
	}
}

func newV1FixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open v1 fixture: %v", err)
	}
	if _, err := rw.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		rw.Close()
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	if _, err := rw.Exec(`DROP TABLE channels; ` + v1ChannelsFixtureSQL + `;
		ALTER TABLE media DROP COLUMN rating;
		UPDATE meta SET value = '1' WHERE key = 'schema_version'`); err != nil {
		rw.Close()
		t.Fatalf("build v1 fixture: %v", err)
	}
	if _, err := rw.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		rw.Close()
		t.Fatalf("restore fixture foreign keys: %v", err)
	}
	return rw
}

const v1ChannelsFixtureSQL = `CREATE TABLE channels (
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
	CHECK (playback_mode IN ('generated', 'packaged')),
	CHECK (package_prefill_ms IS NULL OR package_prefill_ms > 0),
	CHECK (encoder_policy IS NULL OR encoder_policy IN ('any', 'remote_only', 'remote_preferred', 'local_only')),
	CHECK (media_kind IN ('video', 'music')),
	CHECK (schedule_mode IN ('back_to_back', 'slot_grid')),
	CHECK (slot_duration_ms IS NULL OR (slot_duration_ms > 0 AND slot_duration_ms % 6000 = 0)),
	CHECK (prefill_mode IN ('eager', 'on_demand', 'buffered'))
)`

func TestRunSchemaMigrationsAdvancesTransactionally(t *testing.T) {
	rw := newSchemaVersionTestDB(t, 1)
	defer rw.Close()

	migrations := []schemaMigration{
		{
			version: 2,
			name:    "create probe",
			apply: func(ctx context.Context, tx Execer) error {
				_, err := tx.ExecContext(ctx, `CREATE TABLE migration_probe (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`)
				return err
			},
		},
		{
			version: 3,
			name:    "seed probe",
			apply: func(ctx context.Context, tx Execer) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO migration_probe (id, value) VALUES (1, 'kept')`)
				return err
			},
		},
	}
	if err := runSchemaMigrations(context.Background(), rw, 1, 3, migrations); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	var version, value string
	if err := rw.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if err := rw.QueryRow(`SELECT value FROM migration_probe WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("read probe: %v", err)
	}
	if version != "3" || value != "kept" {
		t.Fatalf("version=%q value=%q, want 3/kept", version, value)
	}
}

func TestRunSchemaMigrationsRollsBackBodyAndVersion(t *testing.T) {
	rw := newSchemaVersionTestDB(t, 1)
	defer rw.Close()

	wantErr := errors.New("stop migration")
	err := runSchemaMigrations(context.Background(), rw, 1, 2, []schemaMigration{{
		version: 2,
		name:    "broken",
		apply: func(ctx context.Context, tx Execer) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE should_rollback (id INTEGER PRIMARY KEY)`); err != nil {
				return err
			}
			return wantErr
		},
	}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("migration error=%v, want %v", err, wantErr)
	}
	var version string
	if err := rw.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != "1" {
		t.Fatalf("version=%q, want 1", version)
	}
	var tableCount int
	if err := rw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'should_rollback'`).Scan(&tableCount); err != nil {
		t.Fatalf("find rollback table: %v", err)
	}
	if tableCount != 0 {
		t.Fatal("failed migration left its table behind")
	}
}

func TestRunSchemaMigrationsRequiresContiguousSequence(t *testing.T) {
	rw := newSchemaVersionTestDB(t, 1)
	defer rw.Close()

	err := runSchemaMigrations(context.Background(), rw, 1, 2, nil)
	if err == nil || !strings.Contains(err.Error(), "missing schema migration from v1 to v2") {
		t.Fatalf("error=%v, want missing migration", err)
	}
}

func newSchemaVersionTestDB(t *testing.T, version int) *sql.DB {
	t.Helper()
	rw, err := OpenReadWrite(filepath.Join(t.TempDir(), "schema-version.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := rw.Exec(`
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO meta (key, value) VALUES ('schema_version', ?)
	`, version); err != nil {
		rw.Close()
		t.Fatalf("seed schema version: %v", err)
	}
	return rw
}

func TestVerifySchemaRejectsMissingBaselineTable(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`DROP TABLE subtitle_scan_cache`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	err = VerifySchema(context.Background(), rw)
	if err == nil || !strings.Contains(err.Error(), `missing required table "subtitle_scan_cache"`) {
		t.Fatalf("verify error=%v", err)
	}
}

func TestVerifySchemaRejectsIncompleteBaselineColumn(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`ALTER TABLE media DROP COLUMN content_rating`); err != nil {
		t.Fatalf("drop column: %v", err)
	}

	err = VerifySchema(context.Background(), rw)
	if err == nil || !strings.Contains(err.Error(), `missing required column "media.content_rating"`) {
		t.Fatalf("verify error=%v", err)
	}
}

func TestVerifySchemaRejectsObsoleteBaselineColumn(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`ALTER TABLE media_packages ADD COLUMN subtitle_identity TEXT`); err != nil {
		t.Fatalf("add column: %v", err)
	}

	err = VerifySchema(context.Background(), rw)
	if err == nil || !strings.Contains(err.Error(), `contains obsolete column "media_packages.subtitle_identity"`) {
		t.Fatalf("verify error=%v", err)
	}
}

func TestChannelHiddenFromGuideDefault(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	ch, err := ChannelByID(context.Background(), rw, "ch1")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil || ch.HiddenFromGuide {
		t.Fatalf("channel=%+v, want visible channel", ch)
	}
}
