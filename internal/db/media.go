package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DistinctSchedulingGroups returns all non-empty scheduling_group values, sorted.
func DistinctSchedulingGroups(ctx context.Context, conn *sql.DB) ([]string, error) {
	return queryRows(ctx, conn, scanString, `
		SELECT DISTINCT scheduling_group FROM media
		WHERE scheduling_group IS NOT NULL AND scheduling_group != ''
		ORDER BY scheduling_group`)
}

// SchedulingGroupStats is a per-group count + total duration roll-up.
type SchedulingGroupStats struct {
	Group        string
	EpisodeCount int
	DurationMs   int64
}

// SchedulingGroupRollup returns per-group episode counts and total duration
// for every non-empty scheduling_group of video media, sorted by group name.
// Music scheduling groups are excluded; they are served by a separate endpoint.
func SchedulingGroupRollup(ctx context.Context, conn *sql.DB) ([]SchedulingGroupStats, error) {
	return queryRows(ctx, conn, scanSchedulingGroupStats, `
		SELECT scheduling_group, COUNT(*), COALESCE(SUM(duration_ms), 0)
		FROM media
		WHERE scheduling_group IS NOT NULL AND scheduling_group != ''
		  AND COALESCE(media_kind, 'video') = 'video'
		  AND id NOT IN (SELECT media_id FROM filler_assets)
		GROUP BY scheduling_group
		ORDER BY scheduling_group`)
}

// MediaIDPathTitle is a lightweight projection used for filesystem scans.
type MediaIDPathTitle struct {
	ID    string
	Path  string
	Title string
}

// AllMediaIDPathTitle returns every media row's id, path, and title ordered by
// path. Used by the maintenance scan to stat each path on disk without pulling
// the full Media struct.
func AllMediaIDPathTitle(ctx context.Context, conn *sql.DB) ([]MediaIDPathTitle, error) {
	return queryRows(ctx, conn, scanMediaIDPathTitle, `SELECT id, path, title FROM media ORDER BY path`)
}

// DeleteMediaByIDs removes media rows and their dependents in a single
// transaction. The media table has ON DELETE RESTRICT foreign keys from
// schedule_entries and play_history, so those are cleared first. Cascade
// foreign keys handle channel_media, media_packages, packaged_segments,
// media_tracks, and filler_assets.
//
// Callers are responsible for removing any on-disk package roots before
// invoking this; this function only touches the DB.
func DeleteMediaByIDs(ctx context.Context, conn *sql.DB, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	const chunkSize = 500
	var deleted int64
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		var b strings.Builder
		b.WriteByte('(')
		for i := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
		}
		b.WriteByte(')')
		inClause := b.String()

		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_entries WHERE media_id IN `+inClause, args...); err != nil {
			return 0, fmt.Errorf("delete schedule_entries: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM play_history WHERE media_id IN `+inClause, args...); err != nil {
			return 0, fmt.Errorf("delete play_history: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM media WHERE id IN `+inClause, args...)
		if err != nil {
			return 0, fmt.Errorf("delete media: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += n
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// AllMedia returns every media row ordered by path.
func AllMedia(ctx context.Context, conn *sql.DB) ([]Media, error) {
	return queryRows(ctx, conn, scanValue(scanMedia), mediaSelectSQL()+` ORDER BY path`)
}

// MusicAlbumRollup returns per-group track counts and total duration for
// music scheduling groups, sorted by group name.
func MusicAlbumRollup(ctx context.Context, conn *sql.DB) ([]SchedulingGroupStats, error) {
	return queryRows(ctx, conn, scanSchedulingGroupStats, `
		SELECT scheduling_group, COUNT(*), COALESCE(SUM(duration_ms), 0)
		FROM media
		WHERE scheduling_group IS NOT NULL AND scheduling_group != ''
		  AND COALESCE(media_kind, 'video') = 'music'
		GROUP BY scheduling_group
		ORDER BY scheduling_group`)
}

// MediaByGroup returns all media rows with an exact scheduling_group match, ordered by title then path.
func MediaByGroup(ctx context.Context, conn *sql.DB, group string) ([]Media, error) {
	return queryRows(ctx, conn, scanValue(scanMedia), mediaSelectSQL()+
		` WHERE scheduling_group = ? ORDER BY title, path`, group)
}

func SetMediaSchedulingGroup(ctx context.Context, conn *sql.DB, mediaID string, group sql.NullString) error {
	var value any
	if group.Valid {
		value = group.String
	}
	_, err := conn.ExecContext(ctx, `UPDATE media SET scheduling_group = ? WHERE id = ?`, value, mediaID)
	return err
}

// MediaByPath returns the media row matched by exact path, or (nil, nil).
func MediaByPath(ctx context.Context, conn *sql.DB, path string) (*Media, error) {
	return scanMedia(conn.QueryRowContext(ctx, mediaSelectSQL()+` WHERE path = ?`, path))
}

// MediaByID returns the media row, or (nil, nil) if missing.
func MediaByID(ctx context.Context, conn *sql.DB, id string) (*Media, error) {
	return scanMedia(conn.QueryRowContext(ctx, mediaSelectSQL()+` WHERE id = ?`, id))
}

// MediaByIDs returns the media rows for the supplied IDs keyed by ID.
// Missing IDs are omitted from the map.
func MediaByIDs(ctx context.Context, conn *sql.DB, ids []string) (map[string]Media, error) {
	out := make(map[string]Media, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	unique := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	const chunkSize = 900
	for start := 0; start < len(unique); start += chunkSize {
		end := start + chunkSize
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]

		var b strings.Builder
		b.WriteString(mediaSelectSQL())
		b.WriteString(" WHERE id IN (")
		for i := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
		}
		b.WriteByte(')')

		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}

		rows, err := queryRows(ctx, conn, scanValue(scanMedia), b.String(), args...)
		if err != nil {
			return nil, err
		}
		for _, m := range rows {
			out[m.ID] = m
		}
	}

	return out, nil
}

// SearchMedia returns up to limit media rows whose title, path, or
// scheduling_group contain q (case-insensitive LIKE). If channelID is
// non-empty, rows already in that channel's membership are excluded. limit is
// capped at 50; 0 defaults to 20.
func SearchMedia(ctx context.Context, conn *sql.DB, q string, limit int, channelID string) ([]Media, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	pattern := "%" + q + "%"
	if channelID != "" {
		return queryRows(ctx, conn, scanValue(scanMedia), mediaSelectSQL()+`
			WHERE (title LIKE ? OR path LIKE ? OR scheduling_group LIKE ?)
			  AND id NOT IN (SELECT media_id FROM channel_media WHERE channel_id = ?)
			ORDER BY title, path
			LIMIT ?`, pattern, pattern, pattern, channelID, limit)
	}
	return queryRows(ctx, conn, scanValue(scanMedia), mediaSelectSQL()+`
		WHERE title LIKE ? OR path LIKE ? OR scheduling_group LIKE ?
		ORDER BY title, path
		LIMIT ?`, pattern, pattern, pattern, limit)
}

func scanSchedulingGroupStats(row scanner) (SchedulingGroupStats, error) {
	var s SchedulingGroupStats
	err := row.Scan(&s.Group, &s.EpisodeCount, &s.DurationMs)
	return s, err
}

func scanMediaIDPathTitle(row scanner) (MediaIDPathTitle, error) {
	var r MediaIDPathTitle
	var title sql.NullString
	if err := row.Scan(&r.ID, &r.Path, &title); err != nil {
		return MediaIDPathTitle{}, err
	}
	r.Title = title.String
	return r, nil
}

// UpsertMediaPackage inserts or updates one normalized/package metadata row.
// Callers use this for every package-state transition, so the supplied struct
// must include the complete durable state for that transition rather than a
