package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

//go:embed schema.sql
var SchemaSQL string

const SchemaVersion = 1

// ApplySchema executes the embedded end-state schema and seeds reference data.
// Idempotent — writers may call it on every startup. The linearcast read-only
// service must not (it would attempt to write).
//
// The schema is a single v1 baseline: the historical migration chain was
// collapsed into schema.sql. Databases predating the collapse are not migrated
// across it — they are dropped and recreated from this schema (see plan.md /
// docs/database.md).
func ApplySchema(ctx context.Context, conn *sql.DB) error {
	if _, err := conn.ExecContext(ctx, SchemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := ensureCollectionsTable(ctx, conn); err != nil {
		return fmt.Errorf("ensure collections table: %w", err)
	}
	if err := dropMediaTracksTable(ctx, conn); err != nil {
		return fmt.Errorf("drop media_tracks table: %w", err)
	}
	if err := ensureMediaSeasonEpisodeColumns(ctx, conn); err != nil {
		return fmt.Errorf("ensure media season/episode columns: %w", err)
	}
	if err := ensureMediaVideoBitrateColumn(ctx, conn); err != nil {
		return fmt.Errorf("ensure media video_bitrate_bps column: %w", err)
	}
	if err := ensureMediaMetadataColumns(ctx, conn); err != nil {
		return fmt.Errorf("ensure media metadata columns: %w", err)
	}
	if err := ensureMediaPackagesPackageBytesColumn(ctx, conn); err != nil {
		return fmt.Errorf("ensure media_packages package_bytes column: %w", err)
	}
	if err := ensureMediaPackagesNoSubtitleIdentity(ctx, conn); err != nil {
		return fmt.Errorf("drop media_packages subtitle_identity column: %w", err)
	}
	if err := normalizeChannelDefaults(ctx, conn); err != nil {
		return fmt.Errorf("normalize channel defaults: %w", err)
	}
	if err := pruneRemovedBuiltinProfiles(ctx, conn); err != nil {
		return fmt.Errorf("prune removed builtin profiles: %w", err)
	}
	if err := seedBuiltinProfiles(ctx, conn); err != nil {
		return fmt.Errorf("seed builtin profiles: %w", err)
	}
	return nil
}

func ensureMediaMetadataColumns(ctx context.Context, conn *sql.DB) error {
	if err := ensureColumn(ctx, conn, "media", "description", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "media", "thumb_path", "TEXT"); err != nil {
		return err
	}
	return ensureColumn(ctx, conn, "media", "content_rating", "TEXT")
}

func normalizeChannelDefaults(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(ctx, `UPDATE channels SET hidden_from_guide = 0 WHERE hidden_from_guide IS NULL`)
	return err
}

// ensureMediaPackagesNoSubtitleIdentity reverses the short-lived subtitle_identity
// experiment. A package's bytes are fully determined by (media, rendition
// profile) — including any forced-subtitle burn the profile resolves to — so the
// extra identity dimension and its secondary <mediaID>/<packageID> output path
// were redundant. This rebuilds the table without the column and restores the
// (media_id, rendition_profile) unique key. Rows that carried a non-empty
// identity used a package id that no longer matches the (media, profile) scheme,
// so they are unreachable under the collapsed key: drop them (and their cascade
// children) and let demand re-encode into the canonical <mediaID>/<profile>
// path. The stranded on-disk dirs become orphans the cache reclaim sweeps.
func ensureMediaPackagesNoSubtitleIdentity(ctx context.Context, conn *sql.DB) error {
	has, err := tableHasColumn(ctx, conn, "media_packages", "subtitle_identity")
	if err != nil || !has {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Foreign keys are off for the rebuild, so the parent drop won't cascade;
	// clear the identity packages' child rows explicitly first.
	if _, err := tx.ExecContext(ctx, `DELETE FROM packaged_segments WHERE package_id IN (SELECT id FROM media_packages WHERE subtitle_identity != '')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encoder_jobs WHERE package_id IN (SELECT id FROM media_packages WHERE subtitle_identity != '')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE media_packages_new (
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
			package_bytes         INTEGER,
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
			CHECK (package_bytes IS NULL OR package_bytes >= 0),
			CHECK (video_width IS NULL OR video_width > 0),
			CHECK (video_height IS NULL OR video_height > 0),
			CHECK (attempts >= 0)
		)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO media_packages_new (
			id, media_id, rendition_profile, status, package_root,
			init_segment_path, segment_base_path, container, video_codec,
			video_profile, video_width, video_height, audio_codec, audio_profile,
			timescale, packaged_duration_ms, package_bytes, error, last_attempt_error,
			attempts, created_at_ms, updated_at_ms
		)
		SELECT id, media_id, rendition_profile, status, package_root,
			init_segment_path, segment_base_path, container, video_codec,
			video_profile, video_width, video_height, audio_codec, audio_profile,
			timescale, packaged_duration_ms, package_bytes, error, last_attempt_error,
			attempts, created_at_ms, updated_at_ms
		FROM media_packages
		WHERE subtitle_identity = ''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE media_packages`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE media_packages_new RENAME TO media_packages`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_media_packages_media ON media_packages(media_id, rendition_profile)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_media_packages_status ON media_packages(status)`); err != nil {
		return err
	}
	return tx.Commit()
}

// tableHasColumn reports whether table has a column named col.
func tableHasColumn(ctx context.Context, conn *sql.DB, table, col string) (bool, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func dropMediaTracksTable(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS media_tracks`)
	return err
}

func ensureMediaSeasonEpisodeColumns(ctx context.Context, conn *sql.DB) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(media)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	hasSeason := false
	hasEpisode := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "season_number" {
			hasSeason = true
		}
		if name == "episode_number" {
			hasEpisode = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasSeason {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE media ADD COLUMN season_number INTEGER`); err != nil {
			return err
		}
	}
	if !hasEpisode {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE media ADD COLUMN episode_number INTEGER`); err != nil {
			return err
		}
	}
	return nil
}

// ensureMediaVideoBitrateColumn adds media.video_bitrate_bps to databases
// created before the column existed. New databases get it from schema.sql; this
// backfills the column (default 0 = unknown until the next ingest) so the
// shared media SELECT list scans without error.
func ensureMediaVideoBitrateColumn(ctx context.Context, conn *sql.DB) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(media)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "video_bitrate_bps" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `ALTER TABLE media ADD COLUMN video_bitrate_bps INTEGER NOT NULL DEFAULT 0`)
	return err
}

// ensureMediaPackagesPackageBytesColumn adds media_packages.package_bytes to
// databases created before the column existed. It is NULL ("size unknown")
// until set at finalize or filled by the backfill maint command.
func ensureMediaPackagesPackageBytesColumn(ctx context.Context, conn *sql.DB) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(media_packages)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "package_bytes" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `ALTER TABLE media_packages ADD COLUMN package_bytes INTEGER`)
	return err
}

func requireTable(ctx context.Context, conn *sql.DB, name string) error {
	var got string
	err := conn.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if err != nil {
		return fmt.Errorf("table %q not found: %w", name, err)
	}
	return nil
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

func pruneRemovedBuiltinProfiles(ctx context.Context, conn *sql.DB) error {
	names, err := queryRows(ctx, conn, scanString, `SELECT name FROM package_profiles WHERE is_builtin = 1`)
	if err != nil {
		return err
	}

	var removed []string
	for _, name := range names {
		if !packageprofile.Known(name) {
			removed = append(removed, name)
		}
	}
	for _, name := range removed {
		if _, err := conn.ExecContext(ctx, `DELETE FROM package_profiles WHERE name = ? AND is_builtin = 1`, name); err != nil {
			return err
		}
	}
	return nil
}

// VerifySchema confirms the embedded schema version matches the meta row.
func VerifySchema(ctx context.Context, conn *sql.DB) error {
	var v string
	row := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`)
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("schema_version row missing — initialize the database first")
		}
		return fmt.Errorf("read schema version: %w", err)
	}
	if v != fmt.Sprintf("%d", SchemaVersion) {
		return fmt.Errorf("schema version mismatch: db=%s expected=%d", v, SchemaVersion)
	}
	return nil
}
