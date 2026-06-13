package db

import (
	"context"
	"database/sql"
	"errors"
)

// channelColumns returns the channel column list optionally prefixed (e.g.,
// "c." for joined queries). Pass "" for unqualified columns.
func channelColumns(prefix string) string {
	return prefix + "id, " + prefix + "display_name, " + prefix + "source_directory, " +
		prefix + "ordering, " + prefix + "enabled, " + prefix + "created_at_ms, " +
		prefix + "description, " + prefix + "hidden_from_guide, " + prefix + "artwork_url, " + prefix + "playback_mode, " +
		prefix + "required_package_profile, " + prefix + "abr_ladder_json, " + prefix + "package_prefill_ms, " + prefix + "media_kind, " +
		prefix + "schedule_mode, " + prefix + "slot_duration_ms, " + prefix + "upstream_hls_url, " +
		prefix + "prefill_mode"
}

func channelSelectSQL() string {
	return `SELECT ` + channelColumns("") + ` FROM channels`
}

func scanChannel(row scanner) (*Channel, error) {
	var c Channel
	var enabled int64
	var hidden int64
	var description, artworkURL, requiredProfile, abrLadderJSON, upstreamURL sql.NullString
	var prefillMs, slotDurationMs sql.NullInt64
	if err := row.Scan(&c.ID, &c.DisplayName, &c.SourceDirectory, &c.Ordering, &enabled,
		&c.CreatedAtMs, &description, &hidden, &artworkURL, &c.PlaybackMode, &requiredProfile,
		&abrLadderJSON, &prefillMs, &c.MediaKind, &c.ScheduleMode, &slotDurationMs, &upstreamURL, &c.PrefillMode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c.Enabled = enabled == 1
	c.HiddenFromGuide = hidden == 1
	c.Description = description.String
	c.ArtworkURL = artworkURL.String
	c.RequiredPackageProfile = requiredProfile.String
	c.ABRLadder = NormalizeABRLadder(c.RequiredPackageProfile, abrLadderJSON.String)
	if prefillMs.Valid {
		v := prefillMs.Int64
		c.PackagePrefillMs = &v
	}
	if slotDurationMs.Valid {
		v := slotDurationMs.Int64
		c.SlotDurationMs = &v
	}
	if upstreamURL.Valid {
		v := upstreamURL.String
		c.UpstreamHLSURL = &v
	}
	return &c, nil
}

// mediaColumns returns the media column list optionally prefixed (e.g., "m."
// for joined queries). Pass "" for unqualified columns.
func mediaColumns(prefix string) string {
	return prefix + "id, " + prefix + "path, " + prefix + "directory, " + prefix + "title, " +
		prefix + "scheduling_group, " + prefix + "user_preference, " + prefix + "duration_ms, " +
		prefix + "container, " + prefix + "video_codec, " + prefix + "video_width, " +
		prefix + "video_height, " + prefix + "color_transfer, " + prefix + "color_primaries, " +
		prefix + "audio_codec, " + prefix + "codec_check_passed, " + prefix + "codec_check_reason, " +
		prefix + "ingested_at_ms, " + prefix + "media_kind, " + prefix + "source_ref"
}

func mediaSelectSQL() string {
	return `SELECT ` + mediaColumns("") + ` FROM media`
}

func scanMedia(row scanner) (*Media, error) {
	var m Media
	var passed int64
	var title, group, colorTransfer, colorPrimaries, codecReason, mediaKind, sourceRef sql.NullString
	var userPref, videoWidth sql.NullInt64
	if err := row.Scan(&m.ID, &m.Path, &m.Directory, &title, &group,
		&userPref, &m.DurationMs, &m.Container, &m.VideoCodec, &videoWidth,
		&m.VideoHeight, &colorTransfer, &colorPrimaries,
		&m.AudioCodec, &passed, &codecReason, &m.IngestedAtMs, &mediaKind, &sourceRef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	m.CodecCheckPassed = passed == 1
	m.Title = title.String
	m.SchedulingGroup = group.String
	m.VideoWidth = videoWidth.Int64
	m.ColorTransfer = colorTransfer.String
	m.ColorPrimaries = colorPrimaries.String
	m.CodecCheckReason = codecReason.String
	m.MediaKind = MediaKind(mediaKind.String)
	m.SourceRef = sourceRef.String
	if userPref.Valid {
		v := userPref.Int64
		m.UserPreference = &v
	}
	return &m, nil
}

func mediaPackageSelectSQL() string {
	return `SELECT id, media_id, rendition_profile, status, package_root, init_segment_path,
		segment_base_path, container, video_codec, video_profile, video_width, video_height,
		audio_codec, audio_profile, timescale, packaged_duration_ms, error, last_attempt_error,
		attempts, created_at_ms, updated_at_ms FROM media_packages`
}

type scanner interface {
	Scan(dest ...any) error
}

func queryRows[T any](ctx context.Context, conn Execer, scan func(scanner) (T, error), query string, args ...any) (out []T, err error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanString(row scanner) (string, error) {
	var value string
	err := row.Scan(&value)
	return value, err
}

func scanValue[T any](scan func(scanner) (*T, error)) func(scanner) (T, error) {
	return func(row scanner) (T, error) {
		item, err := scan(row)
		if err != nil {
			var zero T
			return zero, err
		}
		if item == nil {
			var zero T
			return zero, sql.ErrNoRows
		}
		return *item, nil
	}
}

func scanMediaPackage(row scanner) (*MediaPackage, error) {
	var p MediaPackage
	var status string
	var segBase, container, videoCodec, videoProfile sql.NullString
	var videoWidth, videoHeight, timescale sql.NullInt64
	var audioCodec, audioProfile sql.NullString
	var pkgRoot, initSegPath, pkgErr, lastAttempt sql.NullString
	var pkgDurationMs sql.NullInt64
	if err := row.Scan(&p.ID, &p.MediaID, &p.RenditionProfile, &status, &pkgRoot,
		&initSegPath, &segBase, &container, &videoCodec, &videoProfile,
		&videoWidth, &videoHeight, &audioCodec, &audioProfile, &timescale,
		&pkgDurationMs, &pkgErr, &lastAttempt, &p.Attempts,
		&p.CreatedAtMs, &p.UpdatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.Status = PackageStatus(status)
	if pkgRoot.Valid {
		v := pkgRoot.String
		p.PackageRoot = &v
	}
	if initSegPath.Valid {
		v := initSegPath.String
		p.InitSegmentPath = &v
	}
	p.SegmentBasePath = segBase.String
	p.Container = container.String
	p.VideoCodec = videoCodec.String
	p.VideoProfile = videoProfile.String
	if videoWidth.Valid {
		v := videoWidth.Int64
		p.VideoWidth = &v
	}
	if videoHeight.Valid {
		v := videoHeight.Int64
		p.VideoHeight = &v
	}
	p.AudioCodec = audioCodec.String
	p.AudioProfile = audioProfile.String
	if timescale.Valid {
		v := timescale.Int64
		p.Timescale = &v
	}
	if pkgDurationMs.Valid {
		v := pkgDurationMs.Int64
		p.PackagedDurationMs = &v
	}
	if pkgErr.Valid {
		v := pkgErr.String
		p.Error = &v
	}
	if lastAttempt.Valid {
		v := lastAttempt.String
		p.LastAttemptError = &v
	}
	return &p, nil
}

// ReplacePackagedSegments atomically replaces the segment metadata for a
// package. It does not modify the parent media_packages row; callers update

func packagedSegmentSelectSQL() string {
	return `SELECT package_id, segment_number, media_start_ms, duration_ms, path, byte_range_start, byte_range_length
		FROM packaged_segments`
}

func scanPackagedSegment(row scanner) (*PackagedSegment, error) {
	var s PackagedSegment
	var path sql.NullString
	var byteRangeStart, byteRangeLength sql.NullInt64
	if err := row.Scan(&s.PackageID, &s.SegmentNumber, &s.MediaStartMs, &s.DurationMs, &path, &byteRangeStart, &byteRangeLength); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if path.Valid {
		v := path.String
		s.Path = &v
	}
	if byteRangeStart.Valid {
		v := byteRangeStart.Int64
		s.ByteRangeStart = &v
	}
	if byteRangeLength.Valid {
		v := byteRangeLength.Int64
		s.ByteRangeLength = &v
	}
	return &s, nil
}
