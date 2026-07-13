package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type MediaInventoryFilter struct {
	Search        string
	Title         string
	Episode       string
	PathRoot      string
	ReleaseGroup  string
	Media         string
	Source        string
	MediaKind     string
	Collection    string
	PackageStatus string
	CodecStatus   string
	SortBy        string
	SortDir       string
	Limit         int
	Offset        int
}

type MediaInventoryRow struct {
	Media
	ReadyPackages      int64
	PendingPackages    int64
	ProcessingPackages int64
	FailedPackages     int64
	PackageProfiles    string
}

type MediaCollectionBulkScope struct {
	MediaIDs []string
	Filter   *MediaInventoryFilter
}

type MediaCollectionBulkMutation struct {
	Action         string
	Collection     string
	FromCollection string
	Scope          MediaCollectionBulkScope
}

type MediaDeleteBlocker struct {
	ChannelID   string
	DisplayName string
	Kind        string
}

type MediaSourceMetadata struct {
	Path           string
	Source         string
	SourceRef      string
	Title          string
	CollectionName string
	CollectionKind string
	Description    string
	ThumbPath      string
	ContentRating  string
	Genres         []string
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

// UpdateMediaSourceMetadata applies metadata learned from an upstream media
// source after path-based ingest has created or refreshed the media row.
func UpdateMediaSourceMetadata(ctx context.Context, conn *sql.DB, md MediaSourceMetadata) error {
	md.Path = strings.TrimSpace(md.Path)
	if md.Path == "" {
		return fmt.Errorf("media path is required")
	}
	md.Source = strings.TrimSpace(md.Source)
	if md.Source == "" {
		return fmt.Errorf("media source is required")
	}

	return WithTx(ctx, conn, func(tx Execer) error {
		var collectionID any
		if strings.TrimSpace(md.CollectionName) != "" {
			kind := strings.TrimSpace(md.CollectionKind)
			if kind == "" {
				kind = "show"
			}
			id, err := UpsertCollection(ctx, tx, md.CollectionName, kind, md.Source)
			if err != nil {
				return err
			}
			if err := UpdateCollectionGenres(ctx, tx, id, md.Genres); err != nil {
				return err
			}
			collectionID = id
		}

		_, err := tx.ExecContext(ctx, `UPDATE media
			SET source_ref = ?,
			    title = ?,
			    collection_id = COALESCE(?, collection_id),
			    description = ?,
			    thumb_path = ?,
			    content_rating = ?
			WHERE path = ?`,
			metadataNullString(md.SourceRef),
			metadataNullString(md.Title),
			collectionID,
			metadataNullString(md.Description),
			metadataNullString(md.ThumbPath),
			metadataNullString(md.ContentRating),
			md.Path)
		return err
	})
}

func metadataNullString(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

// DistinctSchedulingGroups returns all non-empty collection labels, sorted.
func DistinctSchedulingGroups(ctx context.Context, conn *sql.DB) ([]string, error) {
	return queryRows(ctx, conn, scanString, `
		SELECT DISTINCT CASE WHEN c.kind = 'movie' THEN 'movie:' || c.name ELSE c.name END
		FROM media m
		JOIN collections c ON c.id = m.collection_id
		ORDER BY c.name`)
}

// SchedulingGroupStats is a per-group count + total duration roll-up.
type SchedulingGroupStats struct {
	Group        string
	EpisodeCount int
	DurationMs   int64
}

// MovieGroupRollup returns per-group item counts and total duration for movie
// groups (scheduling_group prefixed with "movie:"), sorted by group name.
func MovieGroupRollup(ctx context.Context, conn *sql.DB) ([]SchedulingGroupStats, error) {
	return queryRows(ctx, conn, scanSchedulingGroupStats, `
		SELECT COALESCE('movie:' || c.name, m.scheduling_group), COUNT(*), COALESCE(SUM(m.duration_ms), 0)
		FROM media m
		LEFT JOIN collections c ON c.id = m.collection_id
		WHERE (c.kind = 'movie' OR m.scheduling_group LIKE 'movie:%')
		  AND COALESCE(m.media_kind, 'video') = 'video'
		  AND m.codec_check_passed = 1
		  AND m.id NOT IN (SELECT media_id FROM filler_assets)
		GROUP BY COALESCE(c.id, m.scheduling_group)
		ORDER BY COALESCE(c.name, m.scheduling_group)`)
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
// package_tracks, and filler_assets.
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

// DeleteMediaMetadataByID removes one media row and dependent metadata without
// pruning schedule_entries or channel membership first. play_history is cleared
// explicitly because it has ON DELETE RESTRICT; other dependent rows fall away
// through foreign-key cascades. If any remaining references still point at the
// media row, the delete fails.
func DeleteMediaMetadataByID(ctx context.Context, conn *sql.DB, id string) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM play_history WHERE media_id = ?`, id); err != nil {
		return false, fmt.Errorf("delete play_history: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete media: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}

// AllMedia returns every media row ordered by path.
func AllMedia(ctx context.Context, conn *sql.DB) ([]Media, error) {
	return queryRows(ctx, conn, scanValue(scanMedia), mediaSelectWithCollectionSQL()+` ORDER BY m.path`)
}

func MediaInventory(ctx context.Context, conn *sql.DB, f MediaInventoryFilter) ([]MediaInventoryRow, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where, args := mediaInventoryWhere(f)
	countArgs := append([]any(nil), args...)
	var total int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM media m LEFT JOIN collections c ON c.id = m.collection_id`+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := queryRows(ctx, conn, scanMediaInventoryRow, `
		SELECT `+mediaColumnsWithCollection("m.", "c.")+`,
		       COALESCE(SUM(CASE WHEN p.status = 'ready' THEN 1 ELSE 0 END), 0) AS ready_packages,
		       COALESCE(SUM(CASE WHEN p.status = 'pending' THEN 1 ELSE 0 END), 0) AS pending_packages,
		       COALESCE(SUM(CASE WHEN p.status = 'processing' THEN 1 ELSE 0 END), 0) AS processing_packages,
		       COALESCE(SUM(CASE WHEN p.status = 'failed' THEN 1 ELSE 0 END), 0) AS failed_packages,
		       COALESCE(GROUP_CONCAT(p.rendition_profile, ', '), '') AS package_profiles
		FROM media m
		LEFT JOIN collections c ON c.id = m.collection_id
		LEFT JOIN media_packages p ON p.media_id = m.id`+where+`
		GROUP BY m.id
		ORDER BY `+mediaInventoryOrderBy(f)+`
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func mediaInventoryWhere(f MediaInventoryFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	if q := strings.TrimSpace(f.Search); q != "" {
		like := "%" + q + "%"
		clauses = append(clauses, `(COALESCE(m.title, '') LIKE ? OR m.path LIKE ? OR COALESCE(c.name, '') LIKE ? OR COALESCE(m.source_ref, '') LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if title := strings.TrimSpace(f.Title); title != "" {
		clauses = append(clauses, `COALESCE(m.title, '') LIKE ?`)
		args = append(args, "%"+title+"%")
	}
	if episode := strings.TrimSpace(f.Episode); episode != "" {
		clauses = append(clauses, `LOWER(m.path) LIKE ?`)
		args = append(args, "%"+strings.ToLower(episode)+"%")
	}
	if root := strings.TrimSpace(f.PathRoot); root != "" {
		clauses = append(clauses, `m.path LIKE ?`)
		args = append(args, root+"%")
	}
	if group := strings.TrimSpace(f.ReleaseGroup); group != "" {
		like := "%-" + group + ".%"
		clauses = append(clauses, `LOWER(m.path) LIKE ?`)
		args = append(args, strings.ToLower(like))
	}
	if media := strings.TrimSpace(f.Media); media != "" {
		like := "%" + strings.ToLower(media) + "%"
		clauses = append(clauses, `(LOWER(COALESCE(m.video_codec, '')) LIKE ? OR LOWER(COALESCE(m.audio_codec, '')) LIKE ? OR CAST(COALESCE(m.video_height, 0) AS TEXT) LIKE ? OR LOWER(COALESCE(m.container, '')) LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	switch strings.TrimSpace(f.Source) {
	case "local":
		clauses = append(clauses, `COALESCE(m.source_ref, '') = ''`)
	case "plex":
		clauses = append(clauses, `m.source_ref LIKE 'plex://%'`)
	case "jellyfin":
		clauses = append(clauses, `m.source_ref LIKE 'jellyfin://%'`)
	case "external":
		clauses = append(clauses, `COALESCE(m.source_ref, '') != '' AND m.source_ref NOT LIKE 'plex://%' AND m.source_ref NOT LIKE 'jellyfin://%'`)
	}
	switch strings.TrimSpace(f.MediaKind) {
	case "video":
		clauses = append(clauses, `COALESCE(m.media_kind, 'video') = 'video'`)
	case "movies":
		clauses = append(clauses, `COALESCE(m.media_kind, 'video') = 'video'`)
		clauses = append(clauses, `(c.kind = 'movie' OR m.scheduling_group LIKE 'movie:%')`)
	case "shows", "tv":
		clauses = append(clauses, `COALESCE(m.media_kind, 'video') = 'video'`)
		clauses = append(clauses, `(COALESCE(c.kind, '') != 'movie' AND COALESCE(m.scheduling_group, '') NOT LIKE 'movie:%')`)
		clauses = append(clauses, `m.id NOT IN (SELECT media_id FROM filler_assets)`)
	case "music":
		clauses = append(clauses, `COALESCE(m.media_kind, 'video') = 'music'`)
	case "filler":
		clauses = append(clauses, `m.id IN (SELECT media_id FROM filler_assets)`)
	}
	if c := strings.TrimSpace(f.Collection); c != "" {
		if c == "__none__" {
			clauses = append(clauses, `m.collection_id IS NULL AND (m.scheduling_group IS NULL OR m.scheduling_group = '')`)
		} else {
			clauses = append(clauses, `COALESCE(c.name, CASE WHEN m.scheduling_group LIKE 'movie:%' THEN substr(m.scheduling_group, 7) ELSE m.scheduling_group END) = ?`)
			args = append(args, normalizeCollectionInput(c))
		}
	}
	switch strings.TrimSpace(f.CodecStatus) {
	case "passed":
		clauses = append(clauses, `m.codec_check_passed = 1`)
	case "failed":
		clauses = append(clauses, `m.codec_check_passed = 0`)
	}
	switch strings.TrimSpace(f.PackageStatus) {
	case "ready", "pending", "processing", "failed":
		clauses = append(clauses, `EXISTS (SELECT 1 FROM media_packages p2 WHERE p2.media_id = m.id AND p2.status = ?)`)
		args = append(args, strings.TrimSpace(f.PackageStatus))
	case "missing":
		clauses = append(clauses, `NOT EXISTS (SELECT 1 FROM media_packages p2 WHERE p2.media_id = m.id)`)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func mediaInventoryOrderBy(f MediaInventoryFilter) string {
	dir := "ASC"
	if strings.EqualFold(strings.TrimSpace(f.SortDir), "desc") {
		dir = "DESC"
	}
	switch strings.TrimSpace(f.SortBy) {
	case "collection":
		return `COALESCE(c.name, CASE WHEN m.scheduling_group LIKE 'movie:%' THEN substr(m.scheduling_group, 7) ELSE m.scheduling_group END) ` + dir + `, COALESCE(m.title, ''), m.path`
	case "pathRoot", "path":
		return `m.path ` + dir + `, COALESCE(m.title, ''), m.id`
	case "releaseGroup":
		return `m.path ` + dir + `, COALESCE(m.title, ''), m.id`
	case "episode":
		return `m.path ` + dir + `, COALESCE(m.title, ''), m.id`
	case "duration":
		return `m.duration_ms ` + dir + `, COALESCE(m.title, ''), m.path`
	case "height":
		return `m.video_height ` + dir + `, COALESCE(m.title, ''), m.path`
	case "source":
		return `COALESCE(m.source_ref, '') ` + dir + `, COALESCE(m.title, ''), m.path`
	case "packages":
		return `COALESCE(SUM(CASE WHEN p.status = 'ready' THEN 1 ELSE 0 END), 0) ` + dir + `,
		        COALESCE(SUM(CASE WHEN p.status = 'processing' THEN 1 ELSE 0 END), 0) ` + dir + `,
		        COALESCE(SUM(CASE WHEN p.status = 'pending' THEN 1 ELSE 0 END), 0) ` + dir + `,
		        COALESCE(SUM(CASE WHEN p.status = 'failed' THEN 1 ELSE 0 END), 0) ` + dir + `,
		        COALESCE(m.title, ''), m.path`
	default:
		return `COALESCE(m.title, '') ` + dir + `, m.path`
	}
}

func ActiveMediaDeleteBlockers(ctx context.Context, conn *sql.DB, mediaID string) ([]MediaDeleteBlocker, error) {
	rows, err := queryRows(ctx, conn, scanMediaDeleteBlocker, `
		SELECT DISTINCT c.id, c.display_name, 'scheduled'
		FROM schedule_entries se
		JOIN channels c ON c.id = se.channel_id
		WHERE se.media_id = ?
		UNION
		SELECT DISTINCT c.id, c.display_name, 'pooled'
		FROM channel_media cm
		JOIN channels c ON c.id = cm.channel_id
		WHERE cm.media_id = ?
		ORDER BY 2, 1, 3`, mediaID, mediaID)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []MediaDeleteBlocker{}, nil
	}
	return rows, nil
}

func scanMediaDeleteBlocker(row scanner) (MediaDeleteBlocker, error) {
	var b MediaDeleteBlocker
	err := row.Scan(&b.ChannelID, &b.DisplayName, &b.Kind)
	return b, err
}

func CountMediaCollectionBulkMutation(ctx context.Context, conn *sql.DB, m MediaCollectionBulkMutation) (int64, error) {
	target, args, err := mediaCollectionBulkTarget(m)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM media m LEFT JOIN collections c ON c.id = m.collection_id`+target, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func ApplyMediaCollectionBulkMutation(ctx context.Context, conn *sql.DB, m MediaCollectionBulkMutation) (int64, error) {
	target, args, err := mediaCollectionBulkTarget(m)
	if err != nil {
		return 0, err
	}

	var q string
	updateArgs := []any{}
	collection := normalizeCollectionInput(m.Collection)
	switch strings.TrimSpace(m.Action) {
	case "set":
		return applySetCollection(ctx, conn, target, args, collection)
	case "clear":
		q = `UPDATE media SET collection_id = NULL, scheduling_group = NULL WHERE id IN (SELECT m.id FROM media m LEFT JOIN collections c ON c.id = m.collection_id` + target + `)`
	case "rename":
		return applySetCollection(ctx, conn, target, args, collection)
	default:
		return 0, fmt.Errorf("unsupported collection action %q", m.Action)
	}
	updateArgs = append(updateArgs, args...)
	res, err := conn.ExecContext(ctx, q, updateArgs...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func applySetCollection(ctx context.Context, conn *sql.DB, target string, args []any, collection string) (int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT m.id, COALESCE(c.kind, ''), COALESCE(m.media_kind, 'video'), COALESCE(m.scheduling_group, '')
		FROM media m
		LEFT JOIN collections c ON c.id = m.collection_id`+target, args...)
	if err != nil {
		return 0, err
	}
	type targetRow struct {
		id          string
		kind        string
		mediaKind   MediaKind
		legacyGroup string
	}
	var targets []targetRow
	for rows.Next() {
		var row targetRow
		if err := rows.Scan(&row.id, &row.kind, &row.mediaKind, &row.legacyGroup); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, row)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(targets) == 0 {
		return 0, nil
	}

	var updated int64
	err = WithTx(ctx, conn, func(tx Execer) error {
		for _, row := range targets {
			kind := row.kind
			if kind == "" {
				kind = collectionKindForGroup(row.legacyGroup, row.mediaKind)
			}
			id, err := UpsertCollection(ctx, tx, collection, kind, "manual")
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE media SET collection_id = ? WHERE id = ?`, id, row.id)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			updated += n
		}
		return nil
	})
	return updated, err
}

func mediaCollectionBulkTarget(m MediaCollectionBulkMutation) (string, []any, error) {
	if len(m.Scope.MediaIDs) > 0 && m.Scope.Filter != nil {
		return "", nil, fmt.Errorf("mediaIds and filter are mutually exclusive")
	}

	var clauses []string
	var args []any
	if len(m.Scope.MediaIDs) > 0 {
		placeholders := make([]string, 0, len(m.Scope.MediaIDs))
		for _, id := range m.Scope.MediaIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) == 0 {
			return "", nil, fmt.Errorf("mediaIds must include at least one non-empty id")
		}
		clauses = append(clauses, `m.id IN (`+strings.Join(placeholders, ",")+`)`)
	} else if m.Scope.Filter != nil {
		where, filterArgs := mediaInventoryWhere(*m.Scope.Filter)
		if strings.TrimSpace(where) == "" {
			return "", nil, fmt.Errorf("filter must restrict the target set")
		}
		clauses = append(clauses, strings.TrimPrefix(where, " WHERE "))
		args = append(args, filterArgs...)
	} else {
		return "", nil, fmt.Errorf("mediaIds or filter is required")
	}

	switch strings.TrimSpace(m.Action) {
	case "set":
		if normalizeCollectionInput(m.Collection) == "" {
			return "", nil, fmt.Errorf("collection is required for set")
		}
	case "clear":
	case "rename":
		from := normalizeCollectionInput(m.FromCollection)
		to := normalizeCollectionInput(m.Collection)
		if from == "" {
			return "", nil, fmt.Errorf("fromCollection is required for rename")
		}
		if to == "" {
			return "", nil, fmt.Errorf("collection is required for rename")
		}
		clauses = append(clauses, `COALESCE(c.name, CASE WHEN m.scheduling_group LIKE 'movie:%' THEN substr(m.scheduling_group, 7) ELSE m.scheduling_group END) = ?`)
		args = append(args, from)
	default:
		return "", nil, fmt.Errorf("unsupported collection action %q", m.Action)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func normalizeCollectionInput(collection string) string {
	return normalizeCollectionName(collection)
}

// MusicAlbumRollup returns per-group track counts and total duration for
// music scheduling groups, sorted by group name.
func MusicAlbumRollup(ctx context.Context, conn *sql.DB) ([]SchedulingGroupStats, error) {
	return queryRows(ctx, conn, scanSchedulingGroupStats, `
		SELECT COALESCE(c.name, m.scheduling_group), COUNT(*), COALESCE(SUM(m.duration_ms), 0)
		FROM media m
		LEFT JOIN collections c ON c.id = m.collection_id
		WHERE COALESCE(m.media_kind, 'video') = 'music'
		  AND COALESCE(c.name, m.scheduling_group, '') != ''
		GROUP BY COALESCE(c.id, m.scheduling_group)
		ORDER BY COALESCE(c.name, m.scheduling_group)`)
}

// MediaByGroup returns all media rows with an exact scheduling_group match that
// pass the codec gate, ordered by title then path. Codec-failed rows (e.g. DV
// Profile 5 / HEVC-PQ that can't yet be encoded) are excluded so the schedule
// builder never lists media the encoder would reject — mirroring the
// codec_check_passed filter in MediaPackageCandidates.
func MediaByGroup(ctx context.Context, conn *sql.DB, group string) ([]Media, error) {
	return queryRows(ctx, conn, scanValue(scanMedia), `SELECT `+mediaColumnsWithCollection("m.", "c.")+` FROM media m LEFT JOIN collections c ON c.id = m.collection_id`+
		` WHERE (m.collection_id IN (SELECT id FROM collections WHERE name = ? OR (kind = 'movie' AND ? = 'movie:' || name))
		    OR m.scheduling_group = ? OR m.scheduling_group = ?)
		   AND m.codec_check_passed = 1 ORDER BY m.title, m.path`,
		normalizeCollectionInput(group), group, group, "movie:"+normalizeCollectionInput(group))
}

func SetMediaSchedulingGroup(ctx context.Context, conn *sql.DB, mediaID string, group sql.NullString) error {
	if !group.Valid || strings.TrimSpace(group.String) == "" {
		_, err := conn.ExecContext(ctx, `UPDATE media SET collection_id = NULL WHERE id = ?`, mediaID)
		return err
	}
	m, err := MediaByID(ctx, conn, mediaID)
	if err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	kind := collectionKindForGroup(group.String, m.MediaKind)
	id, err := UpsertCollection(ctx, conn, group.String, kind, "manual")
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `UPDATE media SET collection_id = ? WHERE id = ?`, id, mediaID)
	return err
}

// UpdateMediaFields applies a partial update to a media row's user-editable
// fields. Only non-nil pointers are written; nil means "leave unchanged".
// An empty string title clears the title; an empty string group clears the
// collection. Returns (false, nil) when no media row matched mediaID.
func UpdateMediaFields(ctx context.Context, conn *sql.DB, mediaID string, title, collectionName *string, seasonNumberSet bool, seasonNumber *int64, episodeNumberSet bool, episodeNumber *int64) (bool, error) {
	if title == nil && collectionName == nil && !seasonNumberSet && !episodeNumberSet {
		return false, fmt.Errorf("at least one field must be set")
	}
	setClauses := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if title != nil {
		setClauses = append(setClauses, "title = ?")
		if *title == "" {
			args = append(args, nil)
		} else {
			args = append(args, *title)
		}
	}
	if collectionName != nil {
		if *collectionName == "" {
			setClauses = append(setClauses, "collection_id = ?")
			args = append(args, nil)
		} else {
			m, err := MediaByID(ctx, conn, mediaID)
			if err != nil {
				return false, err
			}
			if m == nil {
				return false, nil
			}
			kind := "show"
			if m.CollectionID != "" {
				if col, colErr := CollectionByID(ctx, conn, m.CollectionID); colErr == nil && col != nil {
					kind = col.Kind
				}
			} else if NormalizeMediaKind(m.MediaKind) == MediaKindMusic {
				kind = "album"
			}
			id, err := UpsertCollection(ctx, conn, *collectionName, kind, "manual")
			if err != nil {
				return false, err
			}
			setClauses = append(setClauses, "collection_id = ?")
			args = append(args, id)
		}
	}
	if seasonNumberSet {
		setClauses = append(setClauses, "season_number = ?")
		if seasonNumber == nil {
			args = append(args, nil)
		} else {
			args = append(args, *seasonNumber)
		}
	}
	if episodeNumberSet {
		setClauses = append(setClauses, "episode_number = ?")
		if episodeNumber == nil {
			args = append(args, nil)
		} else {
			args = append(args, *episodeNumber)
		}
	}
	args = append(args, mediaID)
	q := `UPDATE media SET ` + strings.Join(setClauses, ", ") + ` WHERE id = ?`
	res, err := conn.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MediaByPath returns the media row matched by exact path, or (nil, nil).
func MediaByPath(ctx context.Context, conn *sql.DB, path string) (*Media, error) {
	return scanMedia(conn.QueryRowContext(ctx, mediaSelectWithCollectionSQL()+` WHERE m.path = ?`, path))
}

// MediaByID returns the media row, or (nil, nil) if missing.
func MediaByID(ctx context.Context, conn *sql.DB, id string) (*Media, error) {
	return scanMedia(conn.QueryRowContext(ctx, mediaSelectWithCollectionSQL()+` WHERE m.id = ?`, id))
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
		b.WriteString(mediaSelectWithCollectionSQL())
		b.WriteString(" WHERE m.id IN (")
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
// collection name contain q (case-insensitive LIKE). If channelID is
// non-empty, rows already in that channel's membership are excluded. limit is
// capped at 50; 0 defaults to 20.
func SearchMedia(ctx context.Context, conn *sql.DB, q string, limit int, channelID string) ([]Media, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	pattern := "%" + q + "%"
	if channelID != "" {
		return queryRows(ctx, conn, scanValue(scanMedia), `SELECT `+mediaColumnsWithCollection("m.", "c.")+` FROM media m LEFT JOIN collections c ON c.id = m.collection_id
			WHERE (m.title LIKE ? OR m.path LIKE ? OR c.name LIKE ?)
			  AND m.id NOT IN (SELECT media_id FROM channel_media WHERE channel_id = ?)
			ORDER BY m.title, m.path
			LIMIT ?`, pattern, pattern, pattern, channelID, limit)
	}
	return queryRows(ctx, conn, scanValue(scanMedia), `SELECT `+mediaColumnsWithCollection("m.", "c.")+` FROM media m LEFT JOIN collections c ON c.id = m.collection_id
		WHERE m.title LIKE ? OR m.path LIKE ? OR c.name LIKE ?
		ORDER BY m.title, m.path
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

func scanMediaInventoryRow(row scanner) (MediaInventoryRow, error) {
	var out MediaInventoryRow
	var passed int64
	var title, group, colorTransfer, colorPrimaries, codecReason, mediaKind, sourceRef, description, thumbPath, contentRating, genresJSON, codecTag sql.NullString
	var collectionID sql.NullString
	var seasonNumber, episodeNumber, userPref, videoWidth sql.NullInt64
	if err := row.Scan(&out.ID, &out.Path, &out.Directory, &title, &group, &collectionID,
		&seasonNumber, &episodeNumber, &userPref, &out.DurationMs, &out.Container, &out.VideoCodec, &videoWidth,
		&out.VideoHeight, &out.VideoBitrateBps, &colorTransfer, &colorPrimaries,
		&out.AudioCodec, &passed, &codecReason, &out.IngestedAtMs, &mediaKind, &sourceRef,
		&description, &thumbPath, &contentRating, &genresJSON, &codecTag,
		&out.ReadyPackages,
		&out.PendingPackages,
		&out.ProcessingPackages,
		&out.FailedPackages,
		&out.PackageProfiles,
	); err != nil {
		return MediaInventoryRow{}, err
	}
	out.CodecCheckPassed = passed == 1
	out.Title = title.String
	out.CollectionName = group.String
	out.CollectionID = collectionID.String
	out.VideoWidth = videoWidth.Int64
	out.ColorTransfer = colorTransfer.String
	out.ColorPrimaries = colorPrimaries.String
	out.CodecCheckReason = codecReason.String
	out.MediaKind = MediaKind(mediaKind.String)
	out.SourceRef = sourceRef.String
	out.Description = description.String
	out.ThumbPath = thumbPath.String
	out.ContentRating = contentRating.String
	if genresJSON.Valid && genresJSON.String != "" {
		_ = json.Unmarshal([]byte(genresJSON.String), &out.Genres)
	}
	out.CodecTagString = codecTag.String
	if seasonNumber.Valid {
		v := seasonNumber.Int64
		out.SeasonNumber = &v
	}
	if episodeNumber.Valid {
		v := episodeNumber.Int64
		out.EpisodeNumber = &v
	}
	if userPref.Valid {
		v := userPref.Int64
		out.UserPreference = &v
	}
	return out, nil
}

// UpsertMediaPackage inserts or updates one normalized/package metadata row.
// Callers use this for every package-state transition, so the supplied struct
// must include the complete durable state for that transition rather than a
