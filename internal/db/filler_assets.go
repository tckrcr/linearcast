package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
)

const (
	FillerKindFiller    = "filler"
	FillerKindBumper    = "bumper"
	FillerKindStationID = "station_id"
)

func normalizeFillerKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case FillerKindBumper:
		return FillerKindBumper
	case FillerKindStationID:
		return FillerKindStationID
	default:
		return FillerKindFiller
	}
}

func UpsertFillerAsset(ctx context.Context, conn *sql.DB, asset FillerAsset) (FillerAsset, error) {
	asset.Kind = normalizeFillerKind(asset.Kind)
	asset.Label = strings.TrimSpace(asset.Label)
	if asset.Label == "" {
		asset.Label = asset.MediaID
	}
	if asset.ID == "" {
		asset.ID = uuid.New().String()
	}
	if asset.CreatedAtMs == 0 {
		asset.CreatedAtMs = 1
	}
	enabled := 0
	if asset.Enabled {
		enabled = 1
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(media_id) DO UPDATE SET
		  label = excluded.label,
		  kind = excluded.kind,
		  enabled = excluded.enabled`,
		asset.ID, asset.MediaID, asset.Label, asset.Kind, enabled, asset.CreatedAtMs)
	if err != nil {
		return FillerAsset{}, err
	}
	return FillerAssetByMediaID(ctx, conn, asset.MediaID)
}

func FillerAssetByMediaID(ctx context.Context, conn *sql.DB, mediaID string) (FillerAsset, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT id, media_id, label, kind, enabled, created_at_ms
		FROM filler_assets
		WHERE media_id = ?`, mediaID)
	return scanFillerAsset(row)
}

func FillerAssetByID(ctx context.Context, conn *sql.DB, id string) (FillerAsset, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT id, media_id, label, kind, enabled, created_at_ms
		FROM filler_assets
		WHERE id = ?`, id)
	return scanFillerAsset(row)
}

func FillerAssets(ctx context.Context, conn *sql.DB) ([]FillerAsset, error) {
	return queryRows(ctx, conn, scanFillerAsset, `
		SELECT id, media_id, label, kind, enabled, created_at_ms
		FROM filler_assets
		ORDER BY label COLLATE NOCASE, id`)
}

func AttachChannelFillerAsset(ctx context.Context, conn *sql.DB, channelID, assetID string, weight int64, enabled bool) error {
	if weight <= 0 {
		weight = 1
	}
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO channel_filler_assets (channel_id, asset_id, weight, enabled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(channel_id, asset_id) DO UPDATE SET
		  weight = excluded.weight,
		  enabled = excluded.enabled`,
		channelID, assetID, weight, enabledInt)
	return err
}

func DetachChannelFillerAsset(ctx context.Context, conn *sql.DB, channelID, assetID string) (bool, error) {
	res, err := conn.ExecContext(ctx, `DELETE FROM channel_filler_assets WHERE channel_id = ? AND asset_id = ?`, channelID, assetID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func ChannelFillerAssetMediaExists(ctx context.Context, conn *sql.DB, channelID, mediaID string) (bool, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT 1
		FROM channel_filler_assets cfa
		JOIN filler_assets fa ON fa.id = cfa.asset_id
		WHERE cfa.channel_id = ?
		  AND fa.media_id = ?
		  AND cfa.enabled = 1
		  AND fa.enabled = 1
		LIMIT 1`, channelID, mediaID)
	var n int
	if err := row.Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func ChannelFillerAssets(ctx context.Context, conn Execer, channelID, renditionProfile string) ([]ChannelFillerAsset, error) {
	return queryRows(ctx, conn, scanChannelFillerAsset, `
		SELECT cfa.channel_id, cfa.weight, cfa.enabled,
		       fa.id, fa.media_id, fa.label, fa.kind, fa.enabled, fa.created_at_ms,
		       m.path, m.title, m.scheduling_group, m.duration_ms,
		       p.id, p.status, p.packaged_duration_ms, p.error
		FROM channel_filler_assets cfa
		JOIN filler_assets fa ON fa.id = cfa.asset_id
		JOIN media m ON m.id = fa.media_id
		LEFT JOIN media_packages p
		  ON p.media_id = m.id
		 AND p.rendition_profile = ?
		WHERE cfa.channel_id = ?
		ORDER BY cfa.enabled DESC, fa.enabled DESC, cfa.weight DESC, fa.label COLLATE NOCASE`,
		renditionProfile, channelID)
}

// FillerAssetCandidate is a filler asset with per-profile package status,
// used by the schedule-builder filler tab.
type FillerAssetCandidate struct {
	ID                 string
	MediaID            string
	Label              string
	Kind               string
	DurationMs         int64
	PackageID          *string
	PackageStatus      string
	PackagedDurationMs *int64
}

// FillerAssetsForScheduleBuilder returns all enabled filler assets joined with
// package status for the given profile. Used by the schedule-builder filler tab.
func FillerAssetsForScheduleBuilder(ctx context.Context, conn *sql.DB, profile string) ([]FillerAssetCandidate, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultPackageProfile
	}
	return queryRows(ctx, conn, scanFillerAssetCandidate(profile), `
		SELECT fa.id, fa.media_id, fa.label, fa.kind, m.duration_ms,
		       p.id, p.status, p.packaged_duration_ms
		FROM filler_assets fa
		JOIN media m ON m.id = fa.media_id
		LEFT JOIN media_packages p
		       ON p.media_id = m.id
		      AND p.rendition_profile = ?
		WHERE fa.enabled = 1
		ORDER BY fa.label COLLATE NOCASE, fa.id`,
		profile)
}

func scanFillerAssetCandidate(profile string) func(scanner) (FillerAssetCandidate, error) {
	return func(row scanner) (FillerAssetCandidate, error) {
		var c FillerAssetCandidate
		var pkgID, pkgStatus sql.NullString
		var pkgDur sql.NullInt64
		if err := row.Scan(&c.ID, &c.MediaID, &c.Label, &c.Kind, &c.DurationMs,
			&pkgID, &pkgStatus, &pkgDur); err != nil {
			return FillerAssetCandidate{}, err
		}
		if pkgID.Valid {
			v := pkgID.String
			c.PackageID = &v
		}
		c.PackageStatus = "missing"
		if pkgStatus.Valid {
			c.PackageStatus = pkgStatus.String
		}
		if pkgDur.Valid {
			v := pkgDur.Int64
			c.PackagedDurationMs = &v
		}
		return c, nil
	}
}

// RegisterFillerAssetsFromDirectory upserts a filler_assets row for every
// video media row whose path is under dir. Existing rows are left unchanged
// (INSERT OR IGNORE). Safe to call after each filler source scan.
func RegisterFillerAssetsFromDirectory(ctx context.Context, conn *sql.DB, dir string, nowMs int64) error {
	dir = strings.TrimRight(dir, "/")
	rows, err := conn.QueryContext(ctx, `
		SELECT id, COALESCE(NULLIF(TRIM(title), ''), id)
		FROM media
		WHERE path LIKE ? || '/%'
		  AND codec_check_passed = 1
		  AND COALESCE(media_kind, 'video') = 'video'`,
		dir)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID, label string
		if err := rows.Scan(&mediaID, &label); err != nil {
			continue
		}
		id := uuid.New().String()
		if _, err := conn.ExecContext(ctx, `
			INSERT OR IGNORE INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
			VALUES (?, ?, ?, 'filler', 1, ?)`,
			id, mediaID, label, nowMs); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanFillerAsset(row scanner) (FillerAsset, error) {
	var a FillerAsset
	var enabled int64
	if err := row.Scan(&a.ID, &a.MediaID, &a.Label, &a.Kind, &enabled, &a.CreatedAtMs); err != nil {
		return FillerAsset{}, err
	}
	a.Enabled = enabled == 1
	return a, nil
}

func scanChannelFillerAsset(row scanner) (ChannelFillerAsset, error) {
	var item ChannelFillerAsset
	var assetEnabled, channelEnabled int64
	var title, group sql.NullString
	var pkgID, pkgStatus, pkgError sql.NullString
	var pkgDurationMs sql.NullInt64
	if err := row.Scan(&item.ChannelID, &item.Weight, &channelEnabled,
		&item.ID, &item.MediaID, &item.Label, &item.Kind, &assetEnabled, &item.CreatedAtMs,
		&item.Path, &title, &group, &item.DurationMs,
		&pkgID, &pkgStatus, &pkgDurationMs, &pkgError); err != nil {
		return ChannelFillerAsset{}, err
	}
	item.Enabled = assetEnabled == 1
	item.ChannelEnabled = channelEnabled == 1
	item.Title = title.String
	item.SchedulingGroup = group.String
	if pkgID.Valid {
		v := pkgID.String
		item.PackageID = &v
	}
	if pkgStatus.Valid {
		v := pkgStatus.String
		item.PackageStatus = &v
	}
	if pkgDurationMs.Valid {
		v := pkgDurationMs.Int64
		item.PackagedDurationMs = &v
	}
	if pkgError.Valid {
		v := pkgError.String
		item.PackageError = &v
	}
	return item, nil
}
