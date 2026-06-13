package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

// UpsertMediaPackage inserts or updates one normalized/package metadata row.
// Callers use this for every package-state transition, so the supplied struct
// must include the complete durable state for that transition rather than a
// partial patch.
func UpsertMediaPackage(ctx context.Context, conn *sql.DB, p MediaPackage) error {
	var pkgRoot, initSeg, videoWidth, videoHeight, timescale, pkgDurationMs, pkgErr, lastAttempt any
	if p.PackageRoot != nil {
		pkgRoot = *p.PackageRoot
	}
	if p.InitSegmentPath != nil {
		initSeg = *p.InitSegmentPath
	}
	if p.VideoWidth != nil {
		videoWidth = *p.VideoWidth
	}
	if p.VideoHeight != nil {
		videoHeight = *p.VideoHeight
	}
	if p.Timescale != nil {
		timescale = *p.Timescale
	}
	if p.PackagedDurationMs != nil {
		pkgDurationMs = *p.PackagedDurationMs
	}
	if p.Error != nil {
		pkgErr = *p.Error
	}
	if p.LastAttemptError != nil {
		lastAttempt = *p.LastAttemptError
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO media_packages (
			id, media_id, rendition_profile, status, package_root, init_segment_path,
			segment_base_path, container, video_codec, video_profile, video_width,
			video_height, audio_codec, audio_profile, timescale, packaged_duration_ms,
			error, last_attempt_error, created_at_ms, updated_at_ms
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			media_id = excluded.media_id,
			rendition_profile = excluded.rendition_profile,
			status = excluded.status,
			package_root = excluded.package_root,
			init_segment_path = excluded.init_segment_path,
			segment_base_path = excluded.segment_base_path,
			container = excluded.container,
			video_codec = excluded.video_codec,
			video_profile = excluded.video_profile,
			video_width = excluded.video_width,
			video_height = excluded.video_height,
			audio_codec = excluded.audio_codec,
			audio_profile = excluded.audio_profile,
			timescale = excluded.timescale,
			packaged_duration_ms = excluded.packaged_duration_ms,
			error = excluded.error,
			last_attempt_error = excluded.last_attempt_error,
			created_at_ms = excluded.created_at_ms,
			updated_at_ms = excluded.updated_at_ms`,
		p.ID, p.MediaID, p.RenditionProfile, string(p.Status), pkgRoot, initSeg,
		nullString(p.SegmentBasePath), nullString(p.Container), nullString(p.VideoCodec),
		nullString(p.VideoProfile), videoWidth, videoHeight,
		nullString(p.AudioCodec), nullString(p.AudioProfile), timescale, pkgDurationMs,
		pkgErr, lastAttempt, p.CreatedAtMs, p.UpdatedAtMs)
	return err
}

// MediaPackageByID returns one package row, or (nil, nil) if it does not exist.
func MediaPackageByID(ctx context.Context, conn *sql.DB, id string) (*MediaPackage, error) {
	row := conn.QueryRowContext(ctx, mediaPackageSelectSQL()+` WHERE id = ?`, id)
	return scanMediaPackage(row)
}

// MediaPackagesForMedia returns all package rows for a media item ordered by
// rendition_profile.
func MediaPackagesForMedia(ctx context.Context, conn *sql.DB, mediaID string) ([]MediaPackage, error) {
	return queryRows(ctx, conn, scanValue(scanMediaPackage), mediaPackageSelectSQL()+` WHERE media_id = ? ORDER BY rendition_profile`, mediaID)
}

// MediaPackageStatus returns the status of the package row for a media/profile
// pair, or "" when no row exists.
func MediaPackageStatus(ctx context.Context, conn Execer, mediaID, renditionProfile string) (PackageStatus, error) {
	var status string
	err := conn.QueryRowContext(ctx, `
		SELECT status FROM media_packages
		WHERE media_id = ? AND rendition_profile = ?`, mediaID, renditionProfile).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return PackageStatus(status), nil
}

// ReadyMediaPackage returns the ready package for a media/profile pair, or
// (nil, nil) if no ready package exists.
func ReadyMediaPackage(ctx context.Context, conn *sql.DB, mediaID, renditionProfile string) (*MediaPackage, error) {
	row := conn.QueryRowContext(ctx, mediaPackageSelectSQL()+`
		WHERE media_id = ? AND rendition_profile = ? AND status = ?
		ORDER BY updated_at_ms DESC
		LIMIT 1`, mediaID, renditionProfile, string(PackageStatusReady))
	return scanMediaPackage(row)
}

// ReadyMediaPackages returns all package rows currently marked ready.
// ReadyPackagedMediaIDs returns the set of media IDs that have at least one ready package.
func ReadyPackagedMediaIDs(ctx context.Context, conn *sql.DB) (map[string]bool, error) {
	ids, err := queryRows(ctx, conn, scanString, `SELECT DISTINCT media_id FROM media_packages WHERE status = ?`, string(PackageStatusReady))
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func ReadyMediaPackages(ctx context.Context, conn *sql.DB) ([]MediaPackage, error) {
	return queryRows(ctx, conn, scanValue(scanMediaPackage), mediaPackageSelectSQL()+`
		WHERE status = ?
		ORDER BY updated_at_ms, id`, string(PackageStatusReady))
}

// that row separately after segment metadata is safely committed.
func ReplacePackagedSegments(ctx context.Context, conn *sql.DB, packageID string, segments []PackagedSegment) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM packaged_segments WHERE package_id = ?`, packageID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO packaged_segments (
			package_id, segment_number, media_start_ms, duration_ms, path,
			byte_range_start, byte_range_length
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, s := range segments {
		if _, err := stmt.ExecContext(ctx, packageID, s.SegmentNumber, s.MediaStartMs, s.DurationMs, s.Path, s.ByteRangeStart, s.ByteRangeLength); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PackagedSegmentByNumber returns one segment by durable package identity and
// segment number, or (nil, nil) if it does not exist.
func PackagedSegmentByNumber(ctx context.Context, conn *sql.DB, packageID string, segmentNumber int64) (*PackagedSegment, error) {
	row := conn.QueryRowContext(ctx, packagedSegmentSelectSQL()+`
		WHERE package_id = ? AND segment_number = ?`, packageID, segmentNumber)
	return scanPackagedSegment(row)
}

// PackagedSegmentAt returns the packaged segment that covers mediaPositionMs,
// or (nil, nil) if the position is outside the package.
func PackagedSegmentAt(ctx context.Context, conn *sql.DB, packageID string, mediaPositionMs int64) (*PackagedSegment, error) {
	row := conn.QueryRowContext(ctx, packagedSegmentSelectSQL()+`
		WHERE package_id = ?
		  AND media_start_ms <= ?
		  AND media_start_ms + duration_ms > ?
		ORDER BY media_start_ms DESC
		LIMIT 1`, packageID, mediaPositionMs, mediaPositionMs)
	return scanPackagedSegment(row)
}

// PackagedSegmentsFrom returns the segment covering mediaPositionMs and the
// following package segments, ordered by segment_number. If mediaPositionMs is
// exactly between segments, it starts at the next segment.
func PackagedSegmentsFrom(ctx context.Context, conn *sql.DB, packageID string, mediaPositionMs int64, limit int) ([]PackagedSegment, error) {
	if limit <= 0 {
		return nil, nil
	}
	first, err := PackagedSegmentAt(ctx, conn, packageID, mediaPositionMs)
	if err != nil {
		return nil, err
	}
	if first == nil {
		row := conn.QueryRowContext(ctx, packagedSegmentSelectSQL()+`
			WHERE package_id = ? AND media_start_ms >= ?
			ORDER BY media_start_ms
			LIMIT 1`, packageID, mediaPositionMs)
		first, err = scanPackagedSegment(row)
		if err != nil {
			return nil, err
		}
		if first == nil {
			return nil, nil
		}
	}

	return queryRows(ctx, conn, scanValue(scanPackagedSegment), packagedSegmentSelectSQL()+`
		WHERE package_id = ? AND segment_number >= ?
		ORDER BY segment_number
		LIMIT ?`, packageID, first.SegmentNumber, limit)
}

// PackagedSegments returns exact segment metadata for a package ordered by
// segment_number.
func PackagedSegments(ctx context.Context, conn *sql.DB, packageID string) ([]PackagedSegment, error) {
	return queryRows(ctx, conn, scanValue(scanPackagedSegment), packagedSegmentSelectSQL()+`
		WHERE package_id = ?
		ORDER BY segment_number`, packageID)
}

func DeleteMediaPackage(ctx context.Context, conn *sql.DB, id string) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM media_packages WHERE id = ?`, id)
	return err
}

// DeleteMediaPackagesByMediaID deletes all package rows for a media item (or
// only the named profile if non-empty). It returns the deleted rows so callers
// can clean up on-disk package_root directories. packaged_segments rows are
// removed automatically via ON DELETE CASCADE.
func DeleteMediaPackagesByMediaID(ctx context.Context, conn *sql.DB, mediaID, profile string) ([]MediaPackage, error) {
	pkgs, err := MediaPackagesForMedia(ctx, conn, mediaID)
	if err != nil {
		return nil, err
	}
	var toDelete []MediaPackage
	for _, p := range pkgs {
		if profile == "" || p.RenditionProfile == profile {
			toDelete = append(toDelete, p)
		}
	}
	for _, p := range toDelete {
		if _, err := conn.ExecContext(ctx, `DELETE FROM media_packages WHERE id = ?`, p.ID); err != nil {
			return nil, err
		}
	}
	return toDelete, nil
}

// FutureScheduleEntriesForMedia returns the channel IDs and start times of any
// schedule entries that reference mediaID and start at or after atMs.
func FutureScheduleEntriesForMedia(ctx context.Context, conn *sql.DB, mediaID string, atMs int64) ([]ScheduleEntry, error) {
	return queryRows(ctx, conn, scanFutureScheduleEntry, `
		SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms
		FROM schedule_entries
		WHERE media_id = ? AND start_ms >= ?
		ORDER BY start_ms ASC`, mediaID, atMs)
}

// PeakSegmentBps returns the peak per-segment bitrate for a package by
// stat()-ing each segment file. Returns 0 if no segments are on disk.
func PeakSegmentBps(ctx context.Context, conn *sql.DB, packageID string) int64 {
	rows, err := conn.QueryContext(ctx, `SELECT path, duration_ms FROM packaged_segments
		 WHERE package_id = ? AND path IS NOT NULL AND duration_ms > 0`,
		packageID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var peak int64
	for rows.Next() {
		var path string
		var durMs int64
		if err := rows.Scan(&path, &durMs); err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if bps := info.Size() * 8 * 1000 / durMs; bps > peak {
			peak = bps
		}
	}
	return peak
}

// UnreferencedPackage is a package whose media is not referenced by any
// schedule entry. These packages are not needed for current or future playback.
type UnreferencedPackage struct {
	ID                 string
	MediaID            string
	RenditionProfile   string
	Status             string
	PackageRoot        *string
	PackagedDurationMs *int64
}

// UnreferencedPackages returns packages whose media_id does not appear in any
// schedule_entry. These are candidates for cache cleanup.
func UnreferencedPackages(ctx context.Context, conn *sql.DB) ([]UnreferencedPackage, error) {
	rows, err := queryRows(ctx, conn, scanUnreferencedPackage, `
		SELECT mp.id, mp.media_id, mp.rendition_profile, mp.status,
		       mp.package_root, mp.packaged_duration_ms
		FROM media_packages mp
		WHERE NOT EXISTS (
			SELECT 1 FROM schedule_entries se
			WHERE se.media_id = mp.media_id
		)
		ORDER BY mp.media_id, mp.rendition_profile`)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []UnreferencedPackage{}, nil
	}
	return rows, nil
}

func scanFutureScheduleEntry(row scanner) (ScheduleEntry, error) {
	var e ScheduleEntry
	err := row.Scan(&e.ID, &e.ChannelID, &e.StartMs, &e.MediaID, &e.OffsetMs, &e.DurationMs, &e.CreatedAtMs)
	return e, err
}

func scanUnreferencedPackage(row scanner) (UnreferencedPackage, error) {
	var p UnreferencedPackage
	var pkgRoot sql.NullString
	var durMs sql.NullInt64
	if err := row.Scan(&p.ID, &p.MediaID, &p.RenditionProfile, &p.Status, &pkgRoot, &durMs); err != nil {
		return UnreferencedPackage{}, err
	}
	if pkgRoot.Valid {
		v := pkgRoot.String
		p.PackageRoot = &v
	}
	if durMs.Valid {
		v := durMs.Int64
		p.PackagedDurationMs = &v
	}
	return p, nil
}
