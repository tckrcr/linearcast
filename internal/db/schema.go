package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

//go:embed schema.sql
var SchemaSQL string

const (
	BaselineSchemaVersion = 1
	SchemaVersion         = 4
)

// schemaMigration is one immutable transition from version-1 to version. New
// entries must be appended in version order and must perform all durable data
// changes through the supplied transaction.
type schemaMigration struct {
	version       int
	name          string
	rebuildTables bool
	apply         func(context.Context, Execer) error
}

var schemaMigrations = []schemaMigration{
	{
		version:       2,
		name:          "remove external HLS channel fields",
		rebuildTables: true,
		apply:         migrateChannelsV2,
	},
	{
		version: 3,
		name:    "add media rating column",
		apply:   migrateMediaRatingV3,
	},
	{
		version: 4,
		name:    "drop play_history",
		apply:   migrateDropPlayHistoryV4,
	},
}

var requiredSchemaTables = map[int][]string{
	1: {
		"meta",
		"channels",
		"collections",
		"media",
		"schedule_entries",
		"channel_media",
		"filler_assets",
		"channel_filler_assets",
		"media_packages",
		"packaged_segments",
		"package_tracks",
		"play_history",
		"package_profiles",
		"admin_write_log",
		"settings",
		"local_media_sources",
		"local_media_source_paths",
		"encoders",
		"encoder_jobs",
		"on_demand_encodings",
		"subtitle_scan_cache",
	},
	2: {
		"meta",
		"channels",
		"collections",
		"media",
		"schedule_entries",
		"channel_media",
		"filler_assets",
		"channel_filler_assets",
		"media_packages",
		"packaged_segments",
		"package_tracks",
		"play_history",
		"package_profiles",
		"admin_write_log",
		"settings",
		"local_media_sources",
		"local_media_source_paths",
		"encoders",
		"encoder_jobs",
		"on_demand_encodings",
		"subtitle_scan_cache",
	},
	3: {
		"meta",
		"channels",
		"collections",
		"media",
		"schedule_entries",
		"channel_media",
		"filler_assets",
		"channel_filler_assets",
		"media_packages",
		"packaged_segments",
		"package_tracks",
		"play_history",
		"package_profiles",
		"admin_write_log",
		"settings",
		"local_media_sources",
		"local_media_source_paths",
		"encoders",
		"encoder_jobs",
		"on_demand_encodings",
		"subtitle_scan_cache",
	},
	4: {
		"meta",
		"channels",
		"collections",
		"media",
		"schedule_entries",
		"channel_media",
		"filler_assets",
		"channel_filler_assets",
		"media_packages",
		"packaged_segments",
		"package_tracks",
		"package_profiles",
		"admin_write_log",
		"settings",
		"local_media_sources",
		"local_media_source_paths",
		"encoders",
		"encoder_jobs",
		"on_demand_encodings",
		"subtitle_scan_cache",
	},
}

// These columns define the supported shape at each schema version. Registering
// historically repaired and migration-critical columns prevents a same-version
// but structurally older database from being accepted as a safe migration
// source.
var requiredSchemaColumns = map[int]map[string][]string{
	1: {
		"channels": {
			"playback_mode",
			"upstream_hls_url",
			"required_package_profile",
			"media_kind",
			"schedule_mode",
			"prefill_mode",
		},
		"collections": {"genres_json"},
		"media": {
			"collection_id",
			"season_number",
			"episode_number",
			"video_bitrate_bps",
			"description",
			"thumb_path",
			"content_rating",
		},
		"media_packages": {"package_bytes"},
	},
	2: {
		"channels": {
			"required_package_profile",
			"media_kind",
			"schedule_mode",
			"prefill_mode",
		},
		"collections": {"genres_json"},
		"media": {
			"collection_id",
			"season_number",
			"episode_number",
			"video_bitrate_bps",
			"description",
			"thumb_path",
			"content_rating",
		},
		"media_packages": {"package_bytes"},
	},
	3: {
		"channels": {
			"required_package_profile",
			"media_kind",
			"schedule_mode",
			"prefill_mode",
		},
		"collections": {"genres_json"},
		"media": {
			"collection_id",
			"season_number",
			"episode_number",
			"video_bitrate_bps",
			"description",
			"thumb_path",
			"content_rating",
			"rating",
		},
		"media_packages": {"package_bytes"},
	},
	4: {
		"channels": {
			"required_package_profile",
			"media_kind",
			"schedule_mode",
			"prefill_mode",
		},
		"collections": {"genres_json"},
		"media": {
			"collection_id",
			"season_number",
			"episode_number",
			"video_bitrate_bps",
			"description",
			"thumb_path",
			"content_rating",
			"rating",
		},
		"media_packages": {"package_bytes"},
	},
}

var forbiddenSchemaColumns = map[int]map[string][]string{
	1: {
		"media_packages": {"subtitle_identity"},
	},
	2: {
		"channels":       {"playback_mode", "upstream_hls_url"},
		"media_packages": {"subtitle_identity"},
	},
	3: {
		"channels":       {"playback_mode", "upstream_hls_url"},
		"media_packages": {"subtitle_identity"},
	},
	4: {
		"channels":       {"playback_mode", "upstream_hls_url"},
		"media_packages": {"subtitle_identity"},
	},
}

func migrateChannelsV2(ctx context.Context, tx Execer) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM channels
		WHERE upstream_hls_url IS NOT NULL
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("inspect external HLS channels: %w", err)
	}
	var externalIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("inspect external HLS channels: %w", err)
		}
		externalIDs = append(externalIDs, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect external HLS channels: %w", err)
	}
	if len(externalIDs) != 0 {
		return fmt.Errorf("remove external HLS channel(s) %s with the v1 application before retrying; v2 does not support upstream_hls_url", strings.Join(externalIDs, ", "))
	}

	if _, err := tx.ExecContext(ctx, `ALTER TABLE channels RENAME TO channels_v1`); err != nil {
		return fmt.Errorf("rename v1 channels: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE channels (
			id               TEXT PRIMARY KEY,
			display_name     TEXT NOT NULL,
			source_directory TEXT NOT NULL,
			ordering         TEXT NOT NULL,
			enabled          INTEGER NOT NULL,
			created_at_ms    INTEGER NOT NULL,
			description      TEXT,
			hidden_from_guide INTEGER NOT NULL DEFAULT 0,
			artwork_url      TEXT,
			required_package_profile TEXT,
			abr_ladder_json TEXT,
			package_prefill_ms INTEGER,
			encoder_policy TEXT,
			media_kind TEXT NOT NULL DEFAULT 'video',
			schedule_mode TEXT NOT NULL DEFAULT 'back_to_back',
			slot_duration_ms INTEGER,
			prefill_mode TEXT NOT NULL DEFAULT 'eager',
			CHECK (enabled IN (0, 1)),
			CHECK (hidden_from_guide IN (0, 1)),
			CHECK (package_prefill_ms IS NULL OR package_prefill_ms > 0),
			CHECK (encoder_policy IS NULL OR encoder_policy IN ('any', 'remote_only', 'remote_preferred', 'local_only')),
			CHECK (media_kind IN ('video', 'music')),
			CHECK (schedule_mode IN ('back_to_back', 'slot_grid')),
			CHECK (slot_duration_ms IS NULL OR (slot_duration_ms > 0 AND slot_duration_ms % 6000 = 0)),
			CHECK (prefill_mode IN ('eager', 'on_demand'))
		)`); err != nil {
		return fmt.Errorf("create v2 channels: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, hidden_from_guide, artwork_url, required_package_profile,
			abr_ladder_json, package_prefill_ms, encoder_policy, media_kind,
			schedule_mode, slot_duration_ms, prefill_mode
		)
		SELECT id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, COALESCE(hidden_from_guide, 0), artwork_url,
			COALESCE(NULLIF(TRIM(required_package_profile), ''),
				CASE WHEN media_kind = 'music' THEN ? ELSE ? END),
			abr_ladder_json, package_prefill_ms, encoder_policy, media_kind,
			schedule_mode, slot_duration_ms,
			CASE WHEN prefill_mode = 'on_demand' THEN 'on_demand' ELSE 'eager' END
		FROM channels_v1`, MusicPackageProfile, DefaultPackageProfile); err != nil {
		return fmt.Errorf("copy v1 channels: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE channels_v1`); err != nil {
		return fmt.Errorf("drop v1 channels: %w", err)
	}
	return nil
}

func migrateMediaRatingV3(ctx context.Context, tx Execer) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE media ADD COLUMN rating REAL`); err != nil {
		return fmt.Errorf("add media.rating column: %w", err)
	}
	return nil
}

// migrateDropPlayHistoryV4 removes the play_history table. Past schedule_entries
// rows are the aired log; play_history duplicated their columns and added only
// "a manifest was served for this entry", which nothing read.
func migrateDropPlayHistoryV4(ctx context.Context, tx Execer) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS play_history`); err != nil {
		return fmt.Errorf("drop play_history: %w", err)
	}
	return nil
}

// MigrationPlan describes whether opening path with the current binary would
// create a fresh database or advance an existing one. Existing databases need
// a verified snapshot exactly when CurrentVersion is behind TargetVersion.
type MigrationPlan struct {
	Fresh          bool
	CurrentVersion int
	TargetVersion  int
}

func (p MigrationPlan) NeedsMigration() bool {
	return !p.Fresh && p.CurrentVersion < p.TargetVersion
}

// PlanMigrations inspects a database without opening it read-write. A missing
// or zero-length file is fresh. Unversioned, pre-baseline, and future databases
// are rejected rather than repaired heuristically.
func PlanMigrations(ctx context.Context, path string) (MigrationPlan, error) {
	return planMigrations(ctx, path, SchemaVersion)
}

func planMigrations(ctx context.Context, path string, targetVersion int) (MigrationPlan, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return MigrationPlan{Fresh: true, TargetVersion: targetVersion}, nil
	}
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("stat database: %w", err)
	}
	if info.Size() == 0 {
		return MigrationPlan{Fresh: true, TargetVersion: targetVersion}, nil
	}

	conn, err := openReadOnlyNoWAL(path)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("inspect database: %w", err)
	}
	defer conn.Close()
	version, err := readSchemaVersion(ctx, conn)
	if err != nil {
		return MigrationPlan{}, err
	}
	if err := validateSchemaVersion(version, targetVersion); err != nil {
		return MigrationPlan{}, err
	}
	return MigrationPlan{CurrentVersion: version, TargetVersion: targetVersion}, nil
}

// Migrate creates the current schema for a fresh database or applies every
// numbered transition after the database's recorded version. Existing schema
// changes are transactional and advance meta.schema_version in the same commit.
// Callers are responsible for taking a verified snapshot before invoking this
// on an existing database for which PlanMigrations reports NeedsMigration.
func Migrate(ctx context.Context, conn *sql.DB) error {
	fresh, err := databaseIsEmpty(ctx, conn)
	if err != nil {
		return err
	}
	if fresh {
		if _, err := conn.ExecContext(ctx, SchemaSQL); err != nil {
			return fmt.Errorf("initialize schema: %w", err)
		}
		if err := seedBuiltinProfiles(ctx, conn); err != nil {
			return fmt.Errorf("seed builtin profiles: %w", err)
		}
		return VerifySchema(ctx, conn)
	}

	current, err := readSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	if err := runSchemaMigrations(ctx, conn, current, SchemaVersion, schemaMigrations); err != nil {
		return err
	}
	return VerifySchema(ctx, conn)
}

func databaseIsEmpty(ctx context.Context, conn *sql.DB) (bool, error) {
	var count int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect database tables: %w", err)
	}
	return count == 0, nil
}

func runSchemaMigrations(ctx context.Context, conn *sql.DB, current, target int, migrations []schemaMigration) error {
	if err := validateSchemaVersion(current, target); err != nil {
		return err
	}
	byVersion := make(map[int]schemaMigration, len(migrations))
	for _, migration := range migrations {
		if migration.version <= BaselineSchemaVersion || migration.version > target {
			return fmt.Errorf("invalid schema migration version %d (%s)", migration.version, migration.name)
		}
		if _, exists := byVersion[migration.version]; exists {
			return fmt.Errorf("duplicate schema migration version %d", migration.version)
		}
		byVersion[migration.version] = migration
	}

	for version := current + 1; version <= target; version++ {
		migration, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("missing schema migration from v%d to v%d", version-1, version)
		}
		if migration.apply == nil {
			return fmt.Errorf("schema migration v%d (%s) has no implementation", version, migration.name)
		}
		if err := runSchemaMigrationTx(ctx, conn, migration, func(tx Execer) error {
			if err := migration.apply(ctx, tx); err != nil {
				return fmt.Errorf("apply schema migration v%d (%s): %w", version, migration.name, err)
			}
			res, err := tx.ExecContext(ctx, `
				UPDATE meta SET value = ?
				WHERE key = 'schema_version' AND value = ?`, strconv.Itoa(version), strconv.Itoa(version-1))
			if err != nil {
				return fmt.Errorf("advance schema version to %d: %w", version, err)
			}
			rows, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("read schema version update: %w", err)
			}
			if rows != 1 {
				return fmt.Errorf("advance schema version to %d: expected one v%d row, updated %d", version, version-1, rows)
			}
			if err := verifyForeignKeys(ctx, tx); err != nil {
				return fmt.Errorf("schema migration v%d foreign keys: %w", version, err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// runSchemaMigrationTx keeps every migration atomic. SQLite table rebuilds
// additionally need foreign-key enforcement disabled before BEGIN so renaming
// and replacing a referenced parent table does not rewrite or cascade-delete
// its dependents. foreign_key_check still validates the rebuilt graph before
// commit, and enforcement is restored before the connection is released.
func runSchemaMigrationTx(ctx context.Context, conn *sql.DB, migration schemaMigration, fn func(Execer) error) error {
	if !migration.rebuildTables {
		return WithImmediateTx(ctx, conn, fn)
	}

	sqlConn, err := conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer sqlConn.Close()
	if _, err := sqlConn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for table rebuild: %w", err)
	}
	if _, err := sqlConn.ExecContext(ctx, `PRAGMA legacy_alter_table = ON`); err != nil {
		_, _ = sqlConn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
		return fmt.Errorf("enable legacy alter-table behavior: %w", err)
	}
	restorePragmas := func() error {
		if _, err := sqlConn.ExecContext(context.Background(), `PRAGMA legacy_alter_table = OFF`); err != nil {
			return err
		}
		_, err := sqlConn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
		return err
	}
	restored := false
	defer func() {
		if !restored {
			_ = restorePragmas()
		}
	}()

	if _, err := sqlConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = sqlConn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if err := fn(sqlConn); err != nil {
		return err
	}
	if _, err := sqlConn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	if err := restorePragmas(); err != nil {
		return fmt.Errorf("restore foreign-key enforcement after migration: %w", err)
	}
	restored = true
	return nil
}

func verifyForeignKeys(ctx context.Context, conn Execer) error {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("foreign_key_check reported %d violation(s)", count)
	}
	return nil
}

func verifySchemaShape(ctx context.Context, conn Execer, version int) error {
	tables, ok := requiredSchemaTables[version]
	if !ok {
		return fmt.Errorf("schema v%d has no registered shape verifier", version)
	}
	for _, table := range tables {
		var found string
		if err := conn.QueryRowContext(ctx, `
			SELECT name FROM sqlite_master
			WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("schema v%d missing required table %q", version, table)
			}
			return fmt.Errorf("verify schema table %q: %w", table, err)
		}
	}
	for table, columns := range requiredSchemaColumns[version] {
		for _, column := range columns {
			has, err := tableHasColumn(ctx, conn, table, column)
			if err != nil {
				return fmt.Errorf("verify schema column %s.%s: %w", table, column, err)
			}
			if !has {
				return fmt.Errorf("schema v%d missing required column %q", version, table+"."+column)
			}
		}
	}
	for table, columns := range forbiddenSchemaColumns[version] {
		for _, column := range columns {
			has, err := tableHasColumn(ctx, conn, table, column)
			if err != nil {
				return fmt.Errorf("verify schema column %s.%s: %w", table, column, err)
			}
			if has {
				return fmt.Errorf("schema v%d contains obsolete column %q", version, table+"."+column)
			}
		}
	}
	return nil
}

func tableHasColumn(ctx context.Context, conn Execer, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func validateSchemaVersion(current, target int) error {
	if current < BaselineSchemaVersion {
		return fmt.Errorf("unsupported schema version %d: minimum supported version is %d", current, BaselineSchemaVersion)
	}
	if current > target {
		return fmt.Errorf("schema version %d is newer than this binary supports (max %d)", current, target)
	}
	return nil
}

func readSchemaVersion(ctx context.Context, conn Execer) (int, error) {
	var raw string
	if err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("schema_version row missing: initialize from the supported v1 baseline")
		}
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	version, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid schema_version %q", raw)
	}
	return version, nil
}

func seedBuiltinProfiles(ctx context.Context, conn *sql.DB) error {
	nowMs := time.Now().UTC().UnixMilli()
	for _, p := range packageprofile.BuiltIns() {
		jsonBytes, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("marshal profile %s: %w", p.Name, err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO package_profiles (name, is_builtin, disabled, profile_json, created_at_ms, updated_at_ms)
			 VALUES (?, 1, 0, ?, ?, ?)
			 ON CONFLICT(name) DO UPDATE SET
				is_builtin = 1,
				profile_json = excluded.profile_json,
				updated_at_ms = excluded.updated_at_ms`,
			p.Name, string(jsonBytes), nowMs, nowMs,
		); err != nil {
			return fmt.Errorf("seed profile %s: %w", p.Name, err)
		}
	}
	return nil
}

// VerifySchema confirms the database is at the exact schema version supported
// by this binary. It never mutates the database.
func VerifySchema(ctx context.Context, conn *sql.DB) error {
	version, err := readSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	if version != SchemaVersion {
		return fmt.Errorf("schema version mismatch: db=%d expected=%d", version, SchemaVersion)
	}
	if err := verifySchemaShape(ctx, conn, version); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, conn); err != nil {
		return fmt.Errorf("verify schema foreign keys: %w", err)
	}
	return nil
}
