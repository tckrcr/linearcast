package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func UpsertMediaTrack(ctx context.Context, conn *sql.DB, t MediaTrack) error {
	if t.Source == "" {
		t.Source = TrackSourceEmbedded
	}
	var err error
	if t.Source == TrackSourceEmbedded || t.Source == TrackSourceEmbeddedBitmap {
		// Conflict target: partial unique index on (media_id, kind, stream_index)
		// where source IN ('embedded_text', 'embedded_bitmap'). Both embedded
		// sources key on the source stream index, so a bitmap inventory row and a
		// text sidecar for the same stream upsert in place rather than colliding.
		_, err = conn.ExecContext(ctx, `
			INSERT INTO media_tracks
			  (media_id, kind, stream_index, language, codec, source, default_flag, forced, path)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(media_id, kind, stream_index)
			  WHERE source IN ('embedded_text', 'embedded_bitmap')
			DO UPDATE SET
			  language     = excluded.language,
			  codec        = excluded.codec,
			  source       = excluded.source,
			  default_flag = excluded.default_flag,
			  forced       = excluded.forced,
			  path         = excluded.path`,
			t.MediaID, t.Kind, t.StreamIndex, nullString(t.Language), nullString(t.Codec), string(t.Source),
			boolToInt(t.DefaultFlag), boolToInt(t.Forced), nullableString(t.Path))
	} else {
		// Conflict target: partial unique index on (media_id, language, source)
		// where source is external (not an embedded source) and kind = 'subtitle'.
		_, err = conn.ExecContext(ctx, `
			INSERT INTO media_tracks
			  (media_id, kind, stream_index, language, codec, source, default_flag, forced, path)
			VALUES (?, ?, -1, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(media_id, language, source)
			  WHERE source NOT IN ('embedded_text', 'embedded_bitmap') AND kind = 'subtitle'
			DO UPDATE SET
			  codec        = excluded.codec,
			  default_flag = excluded.default_flag,
			  forced       = excluded.forced,
			  path         = excluded.path`,
			t.MediaID, t.Kind, nullString(t.Language), nullString(t.Codec), string(t.Source),
			boolToInt(t.DefaultFlag), boolToInt(t.Forced), nullableString(t.Path))
	}
	if err != nil {
		return fmt.Errorf("upsert media track: %w", err)
	}
	return nil
}

func MediaTracksByMediaID(ctx context.Context, conn *sql.DB, mediaID string) ([]MediaTrack, error) {
	tracks, err := queryRows(ctx, conn, scanMediaTrack, `
		SELECT id, media_id, kind, stream_index, language, codec, source, default_flag, forced, path
		FROM media_tracks
		WHERE media_id = ?
		ORDER BY kind, stream_index`,
		mediaID)
	if err != nil {
		return nil, fmt.Errorf("media tracks: %w", err)
	}
	return tracks, nil
}

// PreferredSubtitleTracksByMediaID returns the best available subtitle track
// per language for a media item, applying source precedence:
// embedded_text > opensubtitles > manual. Sentinel rows (path IS NULL) are
// excluded — they exist only to suppress re-querying the API.
func PreferredSubtitleTracksByMediaID(ctx context.Context, conn *sql.DB, mediaID string) ([]MediaTrack, error) {
	all, err := queryRows(ctx, conn, scanMediaTrack, `
		SELECT id, media_id, kind, stream_index, language, codec, source, default_flag, forced, path
		FROM media_tracks
		WHERE media_id = ?
		  AND kind = 'subtitle'
		  AND path IS NOT NULL
		ORDER BY
		  language,
		  CASE source
		    WHEN 'embedded_text'  THEN 0
		    WHEN 'opensubtitles'  THEN 1
		    WHEN 'manual'         THEN 2
		    ELSE 3
		  END`,
		mediaID)
	if err != nil {
		return nil, fmt.Errorf("preferred subtitle tracks: %w", err)
	}
	// Keep first (highest-priority) row per language.
	seen := make(map[string]bool)
	out := all[:0]
	for _, t := range all {
		key := t.Language
		if !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// SubtitleTracksForMediaIDs returns the preferred subtitle track per language
// for each of the given media IDs in a single query.
func SubtitleTracksForMediaIDs(ctx context.Context, conn *sql.DB, mediaIDs []string) ([]MediaTrack, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(mediaIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(mediaIDs))
	for i, id := range mediaIDs {
		args[i] = id
	}
	all, err := queryRows(ctx, conn, scanMediaTrack, `
		SELECT id, media_id, kind, stream_index, language, codec, source, default_flag, forced, path
		FROM media_tracks
		WHERE media_id IN (`+placeholders+`)
		  AND kind = 'subtitle'
		  AND path IS NOT NULL
		ORDER BY
		  stream_index,
		  CASE source
		    WHEN 'embedded_text'  THEN 0
		    WHEN 'opensubtitles'  THEN 1
		    WHEN 'manual'         THEN 2
		    ELSE 3
		  END`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("subtitle tracks: %w", err)
	}
	// Deduplicate by language, keeping first (highest-priority) occurrence.
	seen := make(map[string]bool)
	out := all[:0]
	for _, t := range all {
		key := t.Language
		if !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// BitmapSubtitleTracksForMedia returns embedded bitmap subtitle inventory rows
// for one media item. These rows have no sidecar path; selecting one requires
// burning it into a live transcode session.
func BitmapSubtitleTracksForMedia(ctx context.Context, conn *sql.DB, mediaID string) ([]MediaTrack, error) {
	tracks, err := queryRows(ctx, conn, scanMediaTrack, `
		SELECT id, media_id, kind, stream_index, language, codec, source, default_flag, forced, path
		FROM media_tracks
		WHERE media_id = ?
		  AND kind = 'subtitle'
		  AND source = 'embedded_bitmap'
		ORDER BY language, forced DESC, stream_index`,
		mediaID)
	if err != nil {
		return nil, fmt.Errorf("bitmap subtitle tracks: %w", err)
	}
	return tracks, nil
}

// HasSubtitleTrackForLang returns whether a non-sentinel subtitle track
// (embedded_text or real opensubtitles) exists for a given (media_id, language).
func HasSubtitleTrackForLang(ctx context.Context, conn *sql.DB, mediaID, language string) (bool, error) {
	var n int
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM media_tracks
		WHERE media_id = ?
		  AND kind = 'subtitle'
		  AND language = ?
		  AND path IS NOT NULL
		  AND source IN ('embedded_text', 'opensubtitles', 'manual')`,
		mediaID, language).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has subtitle track: %w", err)
	}
	return n > 0, nil
}

func scanMediaTrack(row scanner) (MediaTrack, error) {
	var t MediaTrack
	var defaultInt, forcedInt int
	var source string
	var language, codec, path sql.NullString
	if err := row.Scan(&t.ID, &t.MediaID, &t.Kind, &t.StreamIndex,
		&language, &codec, &source, &defaultInt, &forcedInt, &path); err != nil {
		return MediaTrack{}, err
	}
	t.Language = language.String
	t.Codec = codec.String
	if path.Valid {
		v := path.String
		t.Path = &v
	}
	t.Source = TrackSource(source)
	t.DefaultFlag = defaultInt == 1
	t.Forced = forcedInt == 1
	return t, nil
}

// DeleteMediaTrack removes a subtitle track row for a given media_id and language.
// It deletes both real tracks and sentinel rows (path IS NULL).
func DeleteMediaTrack(ctx context.Context, conn *sql.DB, mediaID, language string) error {
	_, err := conn.ExecContext(ctx, `
		DELETE FROM media_tracks
		WHERE media_id = ? AND kind = 'subtitle' AND language = ?`,
		mediaID, language)
	if err != nil {
		return fmt.Errorf("delete media track: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
