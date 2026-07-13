package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func UpsertPackageTrack(ctx context.Context, conn *sql.DB, t PackageTrack) error {
	if t.Source == "" {
		t.Source = TrackSourceEmbedded
	}
	var err error
	if t.Source == TrackSourceEmbedded || t.Source == TrackSourceEmbeddedBitmap {
		// Conflict target: partial unique index on (package_id, kind, stream_index)
		// where source IN ('embedded_text', 'embedded_bitmap').
		_, err = conn.ExecContext(ctx, `
			INSERT INTO package_tracks
			  (package_id, kind, stream_index, language, title, codec, source, default_flag, forced, hearing_impaired, path)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(package_id, kind, stream_index)
			  WHERE source IN ('embedded_text', 'embedded_bitmap')
			DO UPDATE SET
			  language         = excluded.language,
			  title            = excluded.title,
			  codec            = excluded.codec,
			  source           = excluded.source,
			  default_flag     = excluded.default_flag,
			  forced           = excluded.forced,
			  hearing_impaired = excluded.hearing_impaired,
			  path             = excluded.path`,
			t.PackageID, t.Kind, t.StreamIndex, nullString(t.Language), nullString(t.Title), nullString(t.Codec), string(t.Source),
			boolToInt(t.DefaultFlag), boolToInt(t.Forced), boolToInt(t.HearingImpaired), nullableString(t.Path))
	} else {
		// Conflict target: partial unique index on (package_id, language, source)
		// where source is external (not an embedded source) and kind = 'subtitle'.
		_, err = conn.ExecContext(ctx, `
			INSERT INTO package_tracks
			  (package_id, kind, stream_index, language, title, codec, source, default_flag, forced, hearing_impaired, path)
			VALUES (?, ?, -1, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(package_id, language, source)
			  WHERE source NOT IN ('embedded_text', 'embedded_bitmap') AND kind = 'subtitle'
			DO UPDATE SET
			  title            = excluded.title,
			  codec            = excluded.codec,
			  default_flag     = excluded.default_flag,
			  forced           = excluded.forced,
			  hearing_impaired = excluded.hearing_impaired,
			  path             = excluded.path`,
			t.PackageID, t.Kind, nullString(t.Language), nullString(t.Title), nullString(t.Codec), string(t.Source),
			boolToInt(t.DefaultFlag), boolToInt(t.Forced), boolToInt(t.HearingImpaired), nullableString(t.Path))
	}
	if err != nil {
		return fmt.Errorf("upsert package track: %w", err)
	}
	return nil
}

// DeletePackageTextSubtitleTracks removes embedded_text subtitle rows for a
// package before re-finalizing it. Bitmap inventory rows (path=NULL) are left
// untouched.
func DeletePackageTextSubtitleTracks(ctx context.Context, conn *sql.DB, packageID string) error {
	_, err := conn.ExecContext(ctx, `
		DELETE FROM package_tracks
		WHERE package_id = ? AND kind = 'subtitle' AND source = 'embedded_text'`,
		packageID)
	if err != nil {
		return fmt.Errorf("delete package text subtitle tracks: %w", err)
	}
	return nil
}

func DeletePackageTracks(ctx context.Context, conn *sql.DB, packageID string) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM package_tracks WHERE package_id = ?`, packageID)
	if err != nil {
		return fmt.Errorf("delete package tracks: %w", err)
	}
	return nil
}

func PackageTracksByPackageID(ctx context.Context, conn *sql.DB, packageID string) ([]PackageTrack, error) {
	tracks, err := queryRows(ctx, conn, scanPackageTrack, `
		SELECT id, package_id, kind, stream_index, language, title, codec, source, default_flag, forced, hearing_impaired, path
		FROM package_tracks
		WHERE package_id = ?
		ORDER BY kind, stream_index`,
		packageID)
	if err != nil {
		return nil, fmt.Errorf("package tracks: %w", err)
	}
	return tracks, nil
}

// plainSubtitleCandidate reports whether t can serve as the plain per-language
// text rendition. Forced narrative tracks never qualify: they are burned into
// transcodes or advertised as their own FORCED rendition, and serving one as
// "English" makes the CC track look empty.
func plainSubtitleCandidate(t PackageTrack) bool {
	return t.Kind == "subtitle" && !t.Forced && t.Path != nil
}

// forcedSubtitleCandidate reports whether t can serve as the forced-narrative
// rendition for its language: a forced track with an extracted sidecar. Bitmap
// forced tracks (NULL path) never qualify — they can only be burned.
func forcedSubtitleCandidate(t PackageTrack) bool {
	return t.Kind == "subtitle" && t.Forced && t.Path != nil
}

func subtitleSourceRank(s TrackSource) int {
	switch s {
	case TrackSourceEmbedded:
		return 0
	case TrackSourceManual:
		return 1
	default:
		return 2
	}
}

// preferSubtitleTrack reports whether a beats b for the per-language pick: full
// dialogue before SDH, then embedded_text before manual, then source stream
// order.
func preferSubtitleTrack(a, b PackageTrack) bool {
	if a.HearingImpaired != b.HearingImpaired {
		return !a.HearingImpaired
	}
	if ra, rb := subtitleSourceRank(a.Source), subtitleSourceRank(b.Source); ra != rb {
		return ra < rb
	}
	return a.StreamIndex < b.StreamIndex
}

func bestSubtitleTrack(tracks []PackageTrack, language string, candidate func(PackageTrack) bool) *PackageTrack {
	var best *PackageTrack
	for i := range tracks {
		t := tracks[i]
		if !candidate(t) || t.Language != language {
			continue
		}
		if best == nil || preferSubtitleTrack(t, *best) {
			best = &tracks[i]
		}
	}
	return best
}

// PreferredSubtitleTrack picks the track to serve as the plain rendition for
// one language, or nil when the language has no qualifying (non-forced,
// extracted) track.
func PreferredSubtitleTrack(tracks []PackageTrack, language string) *PackageTrack {
	return bestSubtitleTrack(tracks, language, plainSubtitleCandidate)
}

// ForcedSubtitleTrack picks the track to serve as the forced-narrative
// rendition for one language, or nil when the language has no extracted forced
// track.
func ForcedSubtitleTrack(tracks []PackageTrack, language string) *PackageTrack {
	return bestSubtitleTrack(tracks, language, forcedSubtitleCandidate)
}

// PackageSubtitleTracksForMediaIDs returns the advertisable plain subtitle track
// per language across ready packages for the given media IDs and profile.
func PackageSubtitleTracksForMediaIDs(ctx context.Context, conn *sql.DB, mediaIDs []string, profile string) ([]PackageTrack, error) {
	return packageSubtitleRenditionsForMediaIDs(ctx, conn, mediaIDs, profile, plainSubtitleCandidate)
}

// ForcedPackageSubtitleTracksForMediaIDs returns the forced-narrative track per
// language across ready packages for the given media IDs and profile. Callers
// advertise these as FORCED renditions unless the profile burns the language in.
func ForcedPackageSubtitleTracksForMediaIDs(ctx context.Context, conn *sql.DB, mediaIDs []string, profile string) ([]PackageTrack, error) {
	return packageSubtitleRenditionsForMediaIDs(ctx, conn, mediaIDs, profile, forcedSubtitleCandidate)
}

func packageSubtitleRenditionsForMediaIDs(ctx context.Context, conn *sql.DB, mediaIDs []string, profile string, candidate func(PackageTrack) bool) ([]PackageTrack, error) {
	if len(mediaIDs) == 0 || strings.TrimSpace(profile) == "" {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(mediaIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(mediaIDs)+2)
	args = append(args, profile, string(PackageStatusReady))
	for _, id := range mediaIDs {
		args = append(args, id)
	}
	all, err := queryRows(ctx, conn, scanPackageTrack, `
		SELECT pt.id, pt.package_id, pt.kind, pt.stream_index, pt.language, pt.title, pt.codec,
		       pt.source, pt.default_flag, pt.forced, pt.hearing_impaired, pt.path
		FROM package_tracks pt
		JOIN media_packages mp ON mp.id = pt.package_id
		WHERE mp.rendition_profile = ?
		  AND mp.status = ?
		  AND mp.media_id IN (`+placeholders+`)
		  AND pt.kind = 'subtitle'
		  AND pt.path IS NOT NULL
		ORDER BY mp.media_id, pt.stream_index`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("package subtitle tracks: %w", err)
	}
	// One track per language, preferred across the whole window; output keeps
	// first-seen language order so the manifest stays deterministic.
	byLang := make(map[string]int)
	var out []PackageTrack
	for _, t := range all {
		if !candidate(t) {
			continue
		}
		if i, ok := byLang[t.Language]; ok {
			if preferSubtitleTrack(t, out[i]) {
				out[i] = t
			}
			continue
		}
		byLang[t.Language] = len(out)
		out = append(out, t)
	}
	return out, nil
}

func scanPackageTrack(row scanner) (PackageTrack, error) {
	var t PackageTrack
	var defaultInt, forcedInt, hearingImpairedInt int
	var language, title, codec, source, path *string
	if err := row.Scan(&t.ID, &t.PackageID, &t.Kind, &t.StreamIndex, &language, &title, &codec, &source,
		&defaultInt, &forcedInt, &hearingImpairedInt, &path); err != nil {
		return PackageTrack{}, err
	}
	t.Language = valueOrEmpty(language)
	t.Title = valueOrEmpty(title)
	t.Codec = valueOrEmpty(codec)
	t.Source = TrackSource(valueOrEmpty(source))
	t.DefaultFlag = defaultInt == 1
	t.Forced = forcedInt == 1
	t.HearingImpaired = hearingImpairedInt == 1
	t.Path = path
	return t, nil
}
