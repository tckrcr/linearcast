package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// migrateV1toV2 adds channel_media.anchor_media_id and backfills the linked-list
// pointers from existing sort_key order. Idempotent: on a fresh v2 DB the SQL
// in schema.sql already created the column and indexes, the version is already
// '2', and the backfill loop is a no-op because there are no rows.
func migrateV1toV2(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 2 {
		return nil
	}

	// ALTER TABLE on an existing v1 DB. schema.sql's CREATE TABLE IF NOT EXISTS
	// is a no-op once the table exists, so the column has to be added here.
	if _, err := db.Exec(`ALTER TABLE channel_media ADD COLUMN anchor_media_id TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add anchor_media_id column: %w", err)
		}
	}

	if err := backfillAnchors(db); err != nil {
		return fmt.Errorf("backfill anchors: %w", err)
	}

	if _, err := db.Exec(
		`UPDATE meta SET value = '2' WHERE key = 'schema_version'`,
	); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV2toV3 adds the linked-list uniqueness constraints. Runs
// unconditionally: CREATE UNIQUE INDEX IF NOT EXISTS is idempotent on a v3 DB
// (indexes already exist) and the only way the create can fail meaningfully
// is if anchor data is inconsistent — which is exactly what we want to catch.
// Safe to run now because the v1->v2 backfill populated anchor_media_id for
// every existing row and the channel_media write paths populate it on insert.
func migrateV2toV3(db *sql.DB) error {
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_media_anchor
		ON channel_media(channel_id, anchor_media_id)
		WHERE anchor_media_id IS NOT NULL`); err != nil {
		return fmt.Errorf("create anchor index: %w", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_media_head
		ON channel_media(channel_id)
		WHERE anchor_media_id IS NULL`); err != nil {
		return fmt.Errorf("create head index: %w", err)
	}

	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion < 3 {
		if _, err := db.Exec(
			`UPDATE meta SET value = '3' WHERE key = 'schema_version'`,
		); err != nil {
			return fmt.Errorf("bump schema_version: %w", err)
		}
	}
	return nil
}

// migrateV3toV4 drops the legacy channel_media.sort_key column and its index.
// Anchor-based ordering replaced sort_key in v2/v3; this migration completes
// the rollout. Idempotent: gracefully handles missing column / missing index.
func migrateV3toV4(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 4 {
		return nil
	}

	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_channel_media_order`); err != nil {
		return fmt.Errorf("drop sort_key index: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE channel_media DROP COLUMN sort_key`); err != nil {
		if !strings.Contains(err.Error(), "no such column") {
			return fmt.Errorf("drop sort_key column: %w", err)
		}
	}

	if _, err := db.Exec(
		`UPDATE meta SET value = '4' WHERE key = 'schema_version'`,
	); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV4toV5 adds the remote-encoder tables, columns, and lifecycle bookkeeping:
//   - encoders + encoder_jobs tables (created by schema.sql for fresh DBs;
//     the CREATE TABLE IF NOT EXISTS statements there are no-ops here)
//   - media_packages.encoded_by, attempts, last_attempt_error (audit + lease bookkeeping)
//   - channels.encoder_policy (NULL = default any-encoder behavior)
//
// Per-column add is idempotent via the "duplicate column name" check, matching
// the migrateV1toV2 pattern. The new tables and indexes are idempotent on their
// own because schema.sql uses CREATE TABLE/INDEX IF NOT EXISTS.
func migrateV4toV5(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 5 {
		return nil
	}

	columns := []struct {
		table, column, ddl string
	}{
		{"media_packages", "encoded_by", `ALTER TABLE media_packages ADD COLUMN encoded_by TEXT`},
		{"media_packages", "attempts", `ALTER TABLE media_packages ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`},
		{"media_packages", "last_attempt_error", `ALTER TABLE media_packages ADD COLUMN last_attempt_error TEXT`},
		{"channels", "encoder_policy", `ALTER TABLE channels ADD COLUMN encoder_policy TEXT`},
	}
	for _, c := range columns {
		if _, err := db.Exec(c.ddl); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("add %s.%s: %w", c.table, c.column, err)
			}
		}
	}

	if _, err := db.Exec(
		`UPDATE meta SET value = '5' WHERE key = 'schema_version'`,
	); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV5toV6 widens the encoders.status CHECK constraint to include
// 'pending'. New encoders are inserted as pending and only flip to 'online'
// after the first successful /api/encoder/ping; the v5 CHECK rejected that
// status value. SQLite cannot alter a CHECK in place, so the table is rebuilt
// with the new constraint and rows are copied across. Foreign keys are
// disabled during the rebuild because encoder_jobs.encoder_id references
// encoders(id); the ids themselves are preserved unchanged.
func migrateV5toV6(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 6 {
		return nil
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE encoders_new (
			id              TEXT PRIMARY KEY,
			name            TEXT NOT NULL,
			api_key_hash    TEXT NOT NULL,
			capabilities    TEXT NOT NULL,
			last_seen_ms    INTEGER NOT NULL,
			status          TEXT NOT NULL,
			created_at_ms   INTEGER NOT NULL,
			revoked_at_ms   INTEGER,
			CHECK (status IN ('pending', 'online', 'draining', 'offline'))
		)`,
		`INSERT INTO encoders_new (id, name, api_key_hash, capabilities, last_seen_ms, status, created_at_ms, revoked_at_ms)
		 SELECT id, name, api_key_hash, capabilities, last_seen_ms, status, created_at_ms, revoked_at_ms FROM encoders`,
		`DROP TABLE encoders`,
		`ALTER TABLE encoders_new RENAME TO encoders`,
		`CREATE INDEX IF NOT EXISTS idx_encoders_status_seen ON encoders(status, last_seen_ms)`,
		`UPDATE meta SET value = '6' WHERE key = 'schema_version'`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("v5->v6 step: %w (sql: %s)", err, s)
		}
	}
	return tx.Commit()
}

// migrateV6toV7 drops media_packages.encoded_by. The column was added in v5 to
// act as a forensic audit trail ("which encoder encoded this package"), but
// encoders are ephemeral and packages are identified by their profile, not by
// the encoder that produced them. The column carried no operational use and
// blocked deleting any encoder that had ever touched a package.
func migrateV6toV7(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 7 {
		return nil
	}
	has, err := tableHasColumn(context.Background(), db, "media_packages", "encoded_by")
	if err != nil {
		return err
	}
	if has {
		if _, err := db.Exec(`ALTER TABLE media_packages DROP COLUMN encoded_by`); err != nil {
			return fmt.Errorf("drop media_packages.encoded_by: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '7' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV8toV9 seeds the scheduler-extender tunables (horizon, low-water,
// tick) so they can be managed from the admin UI instead of env. The defaults
// here match the constants in internal/db/settings.go.
func migrateV8toV9(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 9 {
		return nil
	}

	if _, err := db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES
		('scheduler_horizon_hours', '48'),
		('scheduler_low_water_hours', '24'),
		('scheduler_tick_seconds', '300')`); err != nil {
		return fmt.Errorf("seed scheduler tunables: %w", err)
	}

	if _, err := db.Exec(`UPDATE meta SET value = '9' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV9toV10 seeds the encoder sweeper tunables (interval, max attempts)
// so they can be managed from the admin UI instead of env. The defaults here
// match the constants in internal/db/settings.go.
func migrateV9toV10(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 10 {
		return nil
	}

	if _, err := db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES
		('encoder_sweep_interval_seconds', '30'),
		('encoder_max_attempts', '5')`); err != nil {
		return fmt.Errorf("seed encoder sweeper tunables: %w", err)
	}

	if _, err := db.Exec(`UPDATE meta SET value = '10' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV10toV11 drops channels.abr_ladder. ABR support was implemented but
// never actively used; removing the column simplifies the schema until
// concrete demand returns.
func migrateV10toV11(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 11 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE channels DROP COLUMN abr_ladder`); err != nil {
		if !strings.Contains(err.Error(), "no such column") {
			return fmt.Errorf("drop channels.abr_ladder: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '11' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV7toV8 adds encoders.concurrency and seeds the encoder_mode +
// local_worker_concurrency settings. Concurrency is a property of the machine
// doing the work (not the profile being encoded), and encoder mode graduates
// from an env var to a DB setting so it can be toggled from the admin UI
// without restarting the process.
func migrateV7toV8(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 8 {
		return nil
	}

	if _, err := db.Exec(`ALTER TABLE encoders ADD COLUMN concurrency INTEGER NOT NULL DEFAULT 1 CHECK (concurrency > 0)`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add encoders.concurrency: %w", err)
		}
	}

	if _, err := db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES
		('encoder_mode', '"local"'),
		('local_worker_concurrency', '1')`); err != nil {
		return fmt.Errorf("seed encoder_mode + local_worker_concurrency: %w", err)
	}

	if _, err := db.Exec(`UPDATE meta SET value = '8' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV11toV12 creates the subtitle_scan_cache table so scan results
// survive restarts. A single-row table (id = 1) stores the latest result as a
// JSON blob; ON CONFLICT REPLACE keeps it idempotent.
func migrateV11toV12(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 12 {
		return nil
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS subtitle_scan_cache (
		id            INTEGER PRIMARY KEY CHECK (id = 1),
		scanned_at_ms INTEGER NOT NULL,
		status        TEXT NOT NULL,
		shows_json    TEXT NOT NULL DEFAULT '[]'
	)`); err != nil {
		return fmt.Errorf("create subtitle_scan_cache: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '12' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV12toV13 adds anchor_schedule_entry_id to schedule_entries and
// backfills the linked-list predecessor pointers from the existing
// start_ms order. The new column is metadata for local schedule editing;
// reads still use start_ms until the schedule-order readers are promoted.
func migrateV12toV13(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 13 {
		return nil
	}

	if _, err := db.Exec(`ALTER TABLE schedule_entries ADD COLUMN anchor_schedule_entry_id TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add anchor_schedule_entry_id column: %w", err)
		}
	}
	if err := backfillScheduleEntryAnchors(db); err != nil {
		return fmt.Errorf("backfill schedule entry anchors: %w", err)
	}

	if _, err := db.Exec(`UPDATE meta SET value = '13' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV13toV14 hardens schedule_entries chain metadata with partial unique
// indexes. Before adding the constraints it rewrites every channel's anchors
// from start_ms order so existing v13 databases with drifted metadata can
// recover into a valid chain rather than failing the migration.
func migrateV13toV14(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 14 {
		return nil
	}

	rows, err := db.Query(`SELECT id FROM channels ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var channelIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		channelIDs = append(channelIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, channelID := range channelIDs {
		if err := RepairScheduleEntryAnchorsForChannel(db, channelID); err != nil {
			return fmt.Errorf("channel %s: %w", channelID, err)
		}
	}

	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_schedule_entries_head
		ON schedule_entries(channel_id)
		WHERE anchor_schedule_entry_id IS NULL`); err != nil {
		return fmt.Errorf("create schedule entry head index: %w", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_schedule_entries_anchor
		ON schedule_entries(channel_id, anchor_schedule_entry_id)
		WHERE anchor_schedule_entry_id IS NOT NULL`); err != nil {
		return fmt.Errorf("create schedule entry anchor index: %w", err)
	}

	if _, err := db.Exec(`UPDATE meta SET value = '14' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// backfillAnchors walks each channel's media in (sort_key, media_id) order and
// sets anchor_media_id to the predecessor's media_id. The head row gets NULL.
// Only writes to rows where anchor_media_id IS NULL so re-running is a no-op
// for channels that already have anchors set.
func backfillAnchors(db *sql.DB) error {
	rows, err := db.Query(`SELECT DISTINCT channel_id FROM channel_media`)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var channelIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		channelIDs = append(channelIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, channelID := range channelIDs {
		if err := backfillAnchorsForChannel(db, channelID); err != nil {
			return fmt.Errorf("channel %s: %w", channelID, err)
		}
	}
	return nil
}

func backfillAnchorsForChannel(db *sql.DB, channelID string) error {
	mediaRows, err := db.Query(
		`SELECT media_id FROM channel_media
		 WHERE channel_id = ?
		 ORDER BY sort_key, media_id`,
		channelID,
	)
	if err != nil {
		return err
	}
	defer mediaRows.Close()

	var ordered []string
	for mediaRows.Next() {
		var id string
		if err := mediaRows.Scan(&id); err != nil {
			return err
		}
		ordered = append(ordered, id)
	}
	if err := mediaRows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, mediaID := range ordered {
		var anchor sql.NullString
		if i > 0 {
			anchor = sql.NullString{String: ordered[i-1], Valid: true}
		}
		if _, err := tx.Exec(
			`UPDATE channel_media
			 SET anchor_media_id = ?
			 WHERE channel_id = ? AND media_id = ? AND anchor_media_id IS NULL`,
			anchor, channelID, mediaID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func backfillScheduleEntryAnchors(db *sql.DB) error {
	rows, err := db.Query(`SELECT DISTINCT channel_id FROM schedule_entries`)
	if err != nil {
		return fmt.Errorf("list schedule entry channels: %w", err)
	}
	defer rows.Close()

	var channelIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		channelIDs = append(channelIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, channelID := range channelIDs {
		if err := backfillScheduleEntryAnchorsForChannel(db, channelID); err != nil {
			return fmt.Errorf("channel %s: %w", channelID, err)
		}
	}
	return nil
}

func backfillScheduleEntryAnchorsForChannel(db *sql.DB, channelID string) error {
	rows, err := db.Query(
		`SELECT id FROM schedule_entries
		 WHERE channel_id = ?
		 ORDER BY start_ms, id`,
		channelID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ordered []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ordered = append(ordered, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, entryID := range ordered {
		var anchor sql.NullString
		if i > 0 {
			anchor = sql.NullString{String: ordered[i-1], Valid: true}
		}
		if _, err := tx.Exec(
			`UPDATE schedule_entries
			 SET anchor_schedule_entry_id = ?
			 WHERE channel_id = ? AND id = ? AND anchor_schedule_entry_id IS NULL`,
			anchor, channelID, entryID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BackfillScheduleEntryAnchors rebuilds anchor_schedule_entry_id for every
// channel from the current start_ms ordering. It is intended for fixture setup
// and one-off repair scripts, not as a normal runtime dependency.
func BackfillScheduleEntryAnchors(db *sql.DB) error {
	return backfillScheduleEntryAnchors(db)
}

// BackfillScheduleEntryAnchorsForChannel rebuilds anchor_schedule_entry_id for
// one channel from the current start_ms ordering. It is intended for fixture
// setup and one-off repair scripts, not as a normal runtime dependency.
func BackfillScheduleEntryAnchorsForChannel(db *sql.DB, channelID string) error {
	return backfillScheduleEntryAnchorsForChannel(db, channelID)
}

// RepairScheduleEntryAnchorsForChannel rewrites anchor_schedule_entry_id for
// every row in a channel from the current start_ms ordering. It is intended
// for fixture setup and one-off repair scripts that need to recover after raw
// SQL mutations.
func RepairScheduleEntryAnchorsForChannel(db *sql.DB, channelID string) error {
	rows, err := db.Query(
		`SELECT id FROM schedule_entries
		 WHERE channel_id = ?
		 ORDER BY start_ms, id`,
		channelID,
	)
	if err != nil {
		return fmt.Errorf("list schedule entry rows: %w", err)
	}
	defer rows.Close()

	var ordered []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ordered = append(ordered, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, entryID := range ordered {
		var anchor sql.NullString
		if i > 0 {
			anchor = sql.NullString{String: ordered[i-1], Valid: true}
		}
		if _, err := tx.Exec(
			`UPDATE schedule_entries
			 SET anchor_schedule_entry_id = ?
			 WHERE channel_id = ? AND id = ?`,
			anchor, channelID, entryID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// migrateV14toV15 adds media.media_kind. NULL means video (existing rows),
// 'music' marks audio-only files ingested via IngestMusic. The column lets the
// video packager exclude audio files and future pipelines route by media type.
func migrateV14toV15(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 15 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE media ADD COLUMN media_kind TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add media.media_kind: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '15' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV15toV16 adds channels.media_kind. Existing channels are video
// channels; music channels must be created or updated explicitly.
func migrateV15toV16(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 16 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN media_kind TEXT NOT NULL DEFAULT 'video' CHECK (media_kind IN ('video', 'music'))`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add channels.media_kind: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '16' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV16toV17 widens local_media_sources.media_kind to include music so
// local music roots can use the audio ingest path.
func migrateV16toV17(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 17 {
		return nil
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE local_media_sources_new (
			id            TEXT PRIMARY KEY,
			name          TEXT NOT NULL,
			media_kind    TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL,
			CHECK (media_kind IN ('movies', 'shows', 'music'))
		)`,
		`INSERT INTO local_media_sources_new (id, name, media_kind, created_at_ms, updated_at_ms)
		 SELECT id, name, media_kind, created_at_ms, updated_at_ms FROM local_media_sources`,
		`DROP TABLE local_media_sources`,
		`ALTER TABLE local_media_sources_new RENAME TO local_media_sources`,
		`UPDATE meta SET value = '17' WHERE key = 'schema_version'`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("v16->v17 step: %w (sql: %s)", err, stmt)
		}
	}
	return tx.Commit()
}

// migrateV17toV18 backfills channels.media_kind from the selected package
// profile. v16 introduced the column as video-by-default, so music channels
// created before the UI knew about channel kind could keep a music profile but
// still be filtered out by scheduler eligibility.
func migrateV17toV18(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 18 {
		return nil
	}

	profileKinds, err := profileMediaKindsForMigration(db)
	if err != nil {
		return err
	}

	rows, err := db.Query(`
		SELECT id, COALESCE(NULLIF(TRIM(required_package_profile), ''), ?), media_kind
		FROM channels`,
		DefaultPackageProfile)
	if err != nil {
		return fmt.Errorf("list channel kinds: %w", err)
	}
	type channelKindUpdate struct {
		id   string
		kind MediaKind
	}
	var updates []channelKindUpdate
	for rows.Next() {
		var id, profile, current string
		if err := rows.Scan(&id, &profile, &current); err != nil {
			rows.Close()
			return fmt.Errorf("scan channel kind: %w", err)
		}
		want := MediaKindVideo
		if kind, ok := profileKinds[profile]; ok {
			want = kind
		}
		if string(want) != current {
			updates = append(updates, channelKindUpdate{id: id, kind: want})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, update := range updates {
		if _, err := tx.Exec(`DELETE FROM schedule_entries WHERE channel_id = ?`, update.id); err != nil {
			return fmt.Errorf("clear schedule for channel %s: %w", update.id, err)
		}
		if _, err := tx.Exec(`UPDATE channels SET media_kind = ? WHERE id = ?`, string(update.kind), update.id); err != nil {
			return fmt.Errorf("update channel kind %s: %w", update.id, err)
		}
	}
	if _, err := tx.Exec(`UPDATE meta SET value = '18' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return tx.Commit()
}

func migrateV18toV19(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 19 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN upstream_hls_url TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add channels.upstream_hls_url: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '19' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV19toV20 removes the encoder_mode and local_worker_concurrency
// settings rows (superseded by encoders.concurrency) and relaxes the
// CHECK (concurrency > 0) constraint to CHECK (concurrency >= 0) so that
// setting concurrency=0 can disable the local encoder.
func migrateV19toV20(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 20 {
		return nil
	}
	if _, err := db.Exec(`DELETE FROM settings WHERE key IN ('encoder_mode', 'local_worker_concurrency')`); err != nil {
		return fmt.Errorf("delete deprecated encoder settings: %w", err)
	}
	// SQLite cannot DROP a CHECK constraint in place. Rebuild the table with
	// the relaxed constraint (>= 0 instead of > 0). PRAGMA foreign_keys is
	// disabled for the duration so the rename doesn't trip referencing tables.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable fk: %w", err)
	}
	rebuild := `
		CREATE TABLE encoders_v20 (
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
		INSERT INTO encoders_v20 SELECT id, name, api_key_hash, capabilities,
		    last_seen_ms, status, created_at_ms, revoked_at_ms, concurrency
		  FROM encoders;
		DROP TABLE encoders;
		ALTER TABLE encoders_v20 RENAME TO encoders;
		CREATE INDEX IF NOT EXISTS idx_encoders_status_seen ON encoders(status, last_seen_ms);
	`
	if _, err := db.Exec(rebuild); err != nil {
		_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
		return fmt.Errorf("rebuild encoders table: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable fk: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '20' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV20toV21 adds media_tracks.forced and widens the source CHECK to admit
// 'embedded_bitmap' — an inventory row recording a bitmap (PGS/VOBSUB) subtitle
// stream that cannot be extracted to text (path stays NULL). The two partial
// unique indexes are repointed: the embedded index now covers both embedded
// sources (per source stream), and the external index excludes both. SQLite
// cannot widen a CHECK or add a column with a new constraint in place, so the
// table is rebuilt. Idempotent: guarded by the version check, and on a fresh DB
// schema.sql already built the v21 shape so the rebuild copies an empty table.
func migrateV20toV21(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 21 {
		return nil
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable fk: %w", err)
	}
	// The column list in the INSERT ... SELECT deliberately omits `forced`: an
	// old v20 table has no such column, so it defaults to 0 for every copied
	// row. The rebuilt definition must match schema.sql's media_tracks exactly
	// so fresh and migrated databases converge on an identical schema.
	rebuild := `
		CREATE TABLE media_tracks_v21 (
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
		INSERT INTO media_tracks_v21
		    (id, media_id, kind, stream_index, language, codec, source, default_flag, path)
		  SELECT id, media_id, kind, stream_index, language, codec, source, default_flag, path
		  FROM media_tracks;
		DROP TABLE media_tracks;
		ALTER TABLE media_tracks_v21 RENAME TO media_tracks;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_media_tracks_embedded
		    ON media_tracks(media_id, kind, stream_index)
		    WHERE source IN ('embedded_text', 'embedded_bitmap');
		CREATE UNIQUE INDEX IF NOT EXISTS idx_media_tracks_external
		    ON media_tracks(media_id, language, source)
		    WHERE source NOT IN ('embedded_text', 'embedded_bitmap') AND kind = 'subtitle';
		CREATE INDEX IF NOT EXISTS idx_media_tracks_media ON media_tracks(media_id, kind);
	`
	if _, err := db.Exec(rebuild); err != nil {
		_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
		return fmt.Errorf("rebuild media_tracks table: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable fk: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '21' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV21toV22 adds per-channel schedule mode fields. Existing channels
// stay back-to-back; slot-grid channels opt in by setting schedule_mode and a
// 6-second-aligned slot_duration_ms.
func migrateV21toV22(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 22 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN schedule_mode TEXT NOT NULL DEFAULT 'back_to_back' CHECK (schedule_mode IN ('back_to_back', 'slot_grid'))`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add schedule_mode column: %w", err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN slot_duration_ms INTEGER CHECK (slot_duration_ms IS NULL OR (slot_duration_ms > 0 AND slot_duration_ms % 6000 = 0))`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add slot_duration_ms column: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '22' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV22toV23 widens local_media_sources.media_kind to include 'filler'
// so directories of filler video clips can be imported and auto-registered as
// filler_assets on each scan.
func migrateV22toV23(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 23 {
		return nil
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE local_media_sources_new (
			id            TEXT PRIMARY KEY,
			name          TEXT NOT NULL,
			media_kind    TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL,
			CHECK (media_kind IN ('movies', 'shows', 'music', 'filler'))
		)`,
		`INSERT INTO local_media_sources_new (id, name, media_kind, created_at_ms, updated_at_ms)
		 SELECT id, name, media_kind, created_at_ms, updated_at_ms FROM local_media_sources`,
		`DROP TABLE local_media_sources`,
		`ALTER TABLE local_media_sources_new RENAME TO local_media_sources`,
		`UPDATE meta SET value = '23' WHERE key = 'schema_version'`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("v22->v23 step: %w (sql: %s)", err, stmt)
		}
	}
	return tx.Commit()
}

// migrateV23toV24 adds the on-demand channel mode. The transient
// channel_demand table is kept here for databases that already observed v24;
// v25 drops it after live sessions replaced the demand-sweep shortcut.
func migrateV23toV24(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 24 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN prefill_mode TEXT NOT NULL DEFAULT 'eager' CHECK (prefill_mode IN ('eager', 'on_demand'))`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add prefill_mode column: %w", err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS channel_demand (
		channel_id   TEXT PRIMARY KEY,
		last_seen_ms INTEGER NOT NULL,
		FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("create channel_demand table: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '24' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

func migrateV24toV25(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 25 {
		return nil
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS channel_demand`); err != nil {
		return fmt.Errorf("drop channel_demand table: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '25' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV25toV26 adds schedule_entries.entry_kind ('primary' / 'filler'),
// making filler an authoritative per-entry property instead of inferring it
// from channel_media membership. Existing rows are backfilled to match the old
// inference exactly: an entry is 'filler' iff its media is NOT in the channel's
// channel_media chain (i.e. it is attached filler, not primary programming).
// Idempotent: the column add is guarded against re-runs, and the backfill only
// touches rows still at the 'primary' default.
func migrateV25toV26(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 26 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE schedule_entries ADD COLUMN entry_kind TEXT NOT NULL DEFAULT 'primary' CHECK (entry_kind IN ('primary', 'filler'))`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add schedule_entries.entry_kind: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE schedule_entries SET entry_kind = 'filler'
		WHERE entry_kind = 'primary'
		  AND NOT EXISTS (
			SELECT 1 FROM channel_media cm
			WHERE cm.channel_id = schedule_entries.channel_id
			  AND cm.media_id = schedule_entries.media_id
		)`); err != nil {
		return fmt.Errorf("backfill schedule_entries.entry_kind: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '26' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV26toV27 lowers the scheduler horizon default from 48h to 24h (and
// low-water from 24h to 23h) on deployments still sitting on the old default.
// Generating schedule past the EPG/guide visible window (24h) is wasted work.
// Only rows still at the old default are touched, so an operator's custom values
// are preserved; the low-water update additionally clamps any row that would
// otherwise land >= the new horizon, keeping the low_water < horizon invariant.
func migrateV26toV27(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 27 {
		return nil
	}
	if _, err := db.Exec(`UPDATE settings SET value = '24'
		WHERE key = 'scheduler_horizon_hours' AND value = '48'`); err != nil {
		return fmt.Errorf("lower scheduler_horizon_hours default: %w", err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = '23'
		WHERE key = 'scheduler_low_water_hours'
		  AND CAST(value AS INTEGER) >= (
			SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'scheduler_horizon_hours'
		)`); err != nil {
		return fmt.Errorf("clamp scheduler_low_water_hours below horizon: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '27' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV27toV28 adds media.source_ref for Plex rating keys and widens the
// channels.playback_mode CHECK constraint to accept 'plex_relay'.
//
// The channels table must be recreated because SQLite does not support ALTER
// TABLE to modify CHECK constraints. Foreign key enforcement is temporarily
// disabled during the swap. Multiple Exec calls are safe here because
// OpenReadWrite uses MaxOpenConns=1, so all PRAGMA/DDL statements land on the
// same connection.
func migrateV27toV28(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 28 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE media ADD COLUMN source_ref TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add media.source_ref: %w", err)
		}
	}
	// Drop channels_new from any previous partial run so the CREATE TABLE
	// below is idempotent across retries. The old channels table is untouched
	// until the data copy succeeds, so a crash between CREATE and RENAME will
	// leave channels intact for the next attempt.
	if _, err := db.Exec(`DROP TABLE IF EXISTS channels_new`); err != nil {
		return fmt.Errorf("drop stale channels_new: %w", err)
	}

	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE channels_new (
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
		)
	`); err != nil {
		return fmt.Errorf("create channels_new: %w", err)
	}
	// Use explicit column lists so the INSERT matches by name, not by ordinal
	// position. The old channels table has columns in a different order —
	// columns added by ALTER TABLE (upstream_hls_url, schedule_mode,
	// slot_duration_ms, prefill_mode) were appended at the end, while the
	// new table defines them in the schema.sql order.
	if _, err := db.Exec(`
		INSERT INTO channels_new (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, hidden_from_guide, artwork_url, playback_mode,
			required_package_profile, package_prefill_ms, encoder_policy, media_kind,
			schedule_mode, slot_duration_ms, upstream_hls_url, prefill_mode
		)
		SELECT
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, hidden_from_guide, artwork_url, playback_mode,
			required_package_profile, package_prefill_ms, encoder_policy, media_kind,
			schedule_mode, slot_duration_ms, upstream_hls_url, prefill_mode
		FROM channels
	`); err != nil {
		return fmt.Errorf("copy channels to channels_new: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE channels`); err != nil {
		return fmt.Errorf("drop channels: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE channels_new RENAME TO channels`); err != nil {
		return fmt.Errorf("rename channels_new to channels: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("re-enable foreign_keys: %w", err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '28' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV28toV29 adds media.video_width, media.color_transfer, and
// media.color_primaries so HDR sources can be detected reliably (smpte2084 /
// arib-std-b67 transfer characteristics). Columns are nullable: existing rows
// stay NULL until the next ingest re-probe backfills them.
func migrateV28toV29(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 29 {
		return nil
	}
	for _, col := range []string{
		`ALTER TABLE media ADD COLUMN video_width INTEGER`,
		`ALTER TABLE media ADD COLUMN color_transfer TEXT`,
		`ALTER TABLE media ADD COLUMN color_primaries TEXT`,
	} {
		if _, err := db.Exec(col); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("%s: %w", col, err)
			}
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '29' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// migrateV29toV30 adds the canonical per-channel ABR ladder JSON array. Existing
// channels keep single-profile behavior until an explicit ladder is written.
func migrateV29toV30(db *sql.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if currentVersion >= 30 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN abr_ladder_json TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("add channels.abr_ladder_json: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET value = '30' WHERE key = 'schema_version'`); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	return nil
}

// SetMediaSourceRef stores or clears the source_ref for the media row with the
// given path. Used by the Plex scan flow after ingest to record the rating key.
func SetMediaSourceRef(ctx context.Context, conn *sql.DB, path, sourceRef string) error {
	_, err := conn.ExecContext(ctx, `UPDATE media SET source_ref = ? WHERE path = ?`, sourceRef, path)
	if err != nil {
		return fmt.Errorf("set media source_ref path=%q: %w", path, err)
	}
	return nil
}

func profileMediaKindsForMigration(conn *sql.DB) (map[string]MediaKind, error) {
	out := map[string]MediaKind{
		DefaultPackageProfile: MediaKindVideo,
		MusicPackageProfile:   MediaKindMusic,
	}
	rows, err := conn.Query(`SELECT name, profile_json FROM package_profiles WHERE disabled = 0`)
	if err != nil {
		return nil, fmt.Errorf("list profile kinds: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, fmt.Errorf("scan profile kind: %w", err)
		}
		var p packageprofile.Profile
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			continue
		}
		if packageprofile.NormalizeMediaKind(p.MediaKind) == packageprofile.MediaKindMusic {
			out[name] = MediaKindMusic
		} else {
			out[name] = MediaKindVideo
		}
	}
	return out, rows.Err()
}

func readSchemaVersion(db *sql.DB) (int, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, fmt.Errorf("parse schema_version %q: %w", v, err)
	}
	return n, nil
}
