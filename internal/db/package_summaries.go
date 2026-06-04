package db

import (
	"context"
	"database/sql"
	"strings"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// PackageProfiles returns supported package profiles for operator selection.
// Profiles come from the executable profile registry (built-ins) and the
// package_profiles table (custom). Channel and package rows are deliberately
// excluded so a typo does not become an available option.
func PackageProfiles(ctx context.Context, conn *sql.DB) ([]string, error) {
	return AllPackageProfileNames(ctx, conn)
}

type CandidateFilter struct {
	Search string // substring match on title, path, scheduling_group
	Status string // "missing", "failed", "pending", "processing", "ready", "all", or "" for all non-ready
}

// can see in-flight work accurately instead of re-queueing it blindly.
func MediaPackageCandidates(ctx context.Context, conn *sql.DB, profile string, limit, offset int, f CandidateFilter) ([]MediaPackageCandidate, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultPackageProfile
	}
	profileRecord, err := GetPackageProfile(ctx, conn, profile)
	if err != nil {
		return nil, err
	}
	profileKind := packageprofile.MediaKindVideo
	if profileRecord != nil {
		profileKind = packageprofile.NormalizeMediaKind(profileRecord.MediaKind)
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	args := []any{profile}
	var baseStatusWhere, extraWhere string

	s := strings.TrimSpace(f.Status)
	switch s {
	case "":
		// default: exclude ready items (candidates only)
		baseStatusWhere = ` AND (p.status IS NULL OR p.status <> ?)`
		args = append(args, string(PackageStatusReady))
	case "all":
		// no status filter — return all media regardless of package state
	case "missing":
		baseStatusWhere = ` AND p.status IS NULL`
	case string(PackageStatusReady):
		baseStatusWhere = ` AND p.status = ?`
		args = append(args, string(PackageStatusReady))
	default:
		baseStatusWhere = ` AND (p.status IS NULL OR p.status <> ?) AND p.status = ?`
		args = append(args, string(PackageStatusReady), s)
	}

	if q := strings.TrimSpace(f.Search); q != "" {
		like := "%" + q + "%"
		extraWhere = ` AND (COALESCE(m.title,'') LIKE ? OR m.path LIKE ? OR COALESCE(m.scheduling_group,'') LIKE ?)`
		args = append(args, like, like, like)
	}
	args = append(args, limit, offset)

	args = append([]any{profile, string(profileKind)}, args[1:]...)
	return queryRows(ctx, conn, scanMediaPackageCandidate(profile), `
		SELECT m.id, m.path, m.title, m.scheduling_group, m.duration_ms,
		       p.id, p.status, p.error, p.packaged_duration_ms, p.updated_at_ms
		FROM media m
		LEFT JOIN media_packages p
		       ON p.media_id = m.id
		      AND p.rendition_profile = ?
		WHERE m.codec_check_passed = 1
		  AND COALESCE(m.media_kind, 'video') = ?`+baseStatusWhere+extraWhere+`
		ORDER BY
		  CASE COALESCE(p.status, 'missing')
		    WHEN 'missing' THEN 0
		    WHEN 'failed' THEN 1
		    WHEN 'pending' THEN 2
		    WHEN 'processing' THEN 3
		    WHEN 'ready' THEN 4
		    ELSE 5
		  END,
		  COALESCE(m.title, m.path),
		  m.path
		LIMIT ? OFFSET ?`, args...)
}

func MediaPackageCandidatesAllProfiles(ctx context.Context, conn *sql.DB, limit, offset int, f CandidateFilter) ([]MediaPackageCandidate, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	args := []any{}
	var statusWhere, extraWhere string
	s := strings.TrimSpace(f.Status)
	switch s {
	case "":
		statusWhere = ` AND p.status <> ?`
		args = append(args, string(PackageStatusReady))
	case "all":
	case "missing":
		return []MediaPackageCandidate{}, nil
	default:
		statusWhere = ` AND p.status = ?`
		args = append(args, s)
	}

	if q := strings.TrimSpace(f.Search); q != "" {
		like := "%" + q + "%"
		extraWhere = ` AND (COALESCE(m.title,'') LIKE ? OR m.path LIKE ? OR COALESCE(m.scheduling_group,'') LIKE ? OR p.rendition_profile LIKE ?)`
		args = append(args, like, like, like, like)
	}
	args = append(args, limit, offset)

	return queryRows(ctx, conn, scanMediaPackageCandidateAllProfiles, `
		SELECT m.id, m.path, m.title, m.scheduling_group, m.duration_ms,
		       p.id, p.rendition_profile, p.status, p.error, p.packaged_duration_ms, p.updated_at_ms
		FROM media_packages p
		JOIN media m ON m.id = p.media_id
		WHERE m.codec_check_passed = 1`+statusWhere+extraWhere+`
		ORDER BY
		  CASE p.status
		    WHEN 'failed' THEN 1
		    WHEN 'pending' THEN 2
		    WHEN 'processing' THEN 3
		    WHEN 'ready' THEN 4
		    ELSE 5
		  END,
		  p.rendition_profile,
		  COALESCE(m.title, m.path),
		  m.path
		LIMIT ? OFFSET ?`, args...)
}

func MediaPackageCandidateStatusCounts(ctx context.Context, conn *sql.DB, profile string) ([]PackageStatusSummary, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultPackageProfile
	}
	return queryRows(ctx, conn, scanPackageStatusSummary, `
		SELECT COALESCE(p.status, 'missing') AS status,
		       COUNT(*)
		FROM media m
		LEFT JOIN media_packages p
		       ON p.media_id = m.id
		      AND p.rendition_profile = ?
		WHERE m.codec_check_passed = 1
		GROUP BY COALESCE(p.status, 'missing')
		ORDER BY
		  CASE COALESCE(p.status, 'missing')
		    WHEN 'missing' THEN 0
		    WHEN 'failed' THEN 1
		    WHEN 'pending' THEN 2
		    WHEN 'processing' THEN 3
		    WHEN 'ready' THEN 4
		    ELSE 5
		  END,
		  status`, profile)
}

func MediaPackageCandidateStatusCountsAllProfiles(ctx context.Context, conn *sql.DB) ([]PackageStatusSummary, error) {
	return queryRows(ctx, conn, scanPackageStatusSummary, `
		SELECT p.status, COUNT(*)
		FROM media_packages p
		JOIN media m ON m.id = p.media_id
		WHERE m.codec_check_passed = 1
		GROUP BY p.status
		ORDER BY
		  CASE p.status
		    WHEN 'failed' THEN 1
		    WHEN 'pending' THEN 2
		    WHEN 'processing' THEN 3
		    WHEN 'ready' THEN 4
		    ELSE 5
		  END,
		  p.status`)
}

func PackageStatusSummaries(ctx context.Context, conn *sql.DB) ([]PackageStatusSummary, error) {
	return queryRows(ctx, conn, scanPackageStatusSummary, `
		SELECT status, COUNT(*)
		FROM media_packages
		GROUP BY status
		ORDER BY status`)
}

func PackageProfileSummaries(ctx context.Context, conn *sql.DB) ([]PackageProfileSummary, error) {
	return queryRows(ctx, conn, scanPackageProfileSummary, `
		SELECT rendition_profile,
		       status,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN status = ? THEN COALESCE(packaged_duration_ms, 0) ELSE 0 END), 0),
		       MIN(updated_at_ms),
		       MAX(updated_at_ms)
		FROM media_packages
		GROUP BY rendition_profile, status
		ORDER BY rendition_profile, status`, string(PackageStatusReady))
}

func ChannelPackageSummaries(ctx context.Context, conn *sql.DB) ([]ChannelPackageSummary, error) {
	return queryRows(ctx, conn, scanChannelPackageSummary, `
		SELECT c.id,
		       c.display_name,
		       p.rendition_profile,
		       p.status,
		       COUNT(DISTINCT p.id),
		       COALESCE(SUM(CASE WHEN p.status = ? THEN COALESCE(p.packaged_duration_ms, 0) ELSE 0 END), 0),
		       MIN(p.updated_at_ms),
		       MAX(p.updated_at_ms)
		FROM channels c
		JOIN channel_media cm ON cm.channel_id = c.id
		JOIN media_packages p ON p.media_id = cm.media_id
		GROUP BY c.id, c.display_name, p.rendition_profile, p.status
		ORDER BY c.id, p.rendition_profile, p.status`, string(PackageStatusReady))
}

func ChannelPackageNeedSummaries(ctx context.Context, conn *sql.DB) ([]ChannelPackageNeedSummary, error) {
	return queryRows(ctx, conn, scanChannelPackageNeedSummary, `
		SELECT c.id,
		       c.display_name,
		       COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?) AS rendition_profile,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN p.status = ? THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN p.status = ? THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN p.status = ? THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN p.status = ? THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN p.id IS NULL THEN 1 ELSE 0 END), 0)
		FROM channels c
		JOIN channel_media cm ON cm.channel_id = c.id
		JOIN media m ON m.id = cm.media_id
		LEFT JOIN media_packages p
		  ON p.media_id = cm.media_id
		 AND p.rendition_profile = COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?)
		WHERE c.enabled = 1
		  AND c.playback_mode = ?
		  AND m.codec_check_passed = 1
		GROUP BY c.id, c.display_name, COALESCE(NULLIF(TRIM(c.required_package_profile), ''), ?)
		ORDER BY c.id, rendition_profile`,
		DefaultPackageProfile,
		string(PackageStatusReady),
		string(PackageStatusProcessing),
		string(PackageStatusPending),
		string(PackageStatusFailed),
		DefaultPackageProfile,
		string(PlaybackModePackaged),
		DefaultPackageProfile,
	)
}

// InvalidProfilePackages returns media_packages rows whose rendition_profile is
// not a known executable package profile.
func InvalidProfilePackages(ctx context.Context, conn *sql.DB) ([]InvalidProfilePackage, error) {
	profiles, err := AllPackageProfileRecords(ctx, conn)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		known[p.Profile.Name] = true
	}

	rows, err := queryRows(ctx, conn, scanInvalidProfilePackage, `
		SELECT id, media_id, rendition_profile, status, package_root
		FROM media_packages
		ORDER BY rendition_profile, id`)
	if err != nil {
		return nil, err
	}
	var out []InvalidProfilePackage
	for _, p := range rows {
		if known[p.RenditionProfile] {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func PackageRoots(ctx context.Context, conn *sql.DB) ([]string, error) {
	return queryRows(ctx, conn, scanString, `
		SELECT DISTINCT package_root
		FROM media_packages
		WHERE package_root IS NOT NULL
		  AND TRIM(package_root) != ''
		ORDER BY package_root`)
}

func PackageRootSummaries(ctx context.Context, conn *sql.DB) ([]PackageRoot, error) {
	return queryRows(ctx, conn, scanPackageRoot, `
		SELECT DISTINCT rendition_profile,
		       status,
		       package_root
		FROM media_packages
		WHERE package_root IS NOT NULL
		  AND TRIM(package_root) != ''
		ORDER BY rendition_profile, status, package_root`)
}

func ChannelPackageRoots(ctx context.Context, conn *sql.DB) ([]ChannelPackageRoot, error) {
	return queryRows(ctx, conn, scanChannelPackageRoot, `
		SELECT DISTINCT c.id,
		       p.rendition_profile,
		       p.status,
		       p.package_root
		FROM channels c
		JOIN channel_media cm ON cm.channel_id = c.id
		JOIN media_packages p ON p.media_id = cm.media_id
		WHERE p.package_root IS NOT NULL
		  AND TRIM(p.package_root) != ''
		ORDER BY c.id, p.rendition_profile, p.status, p.package_root`)
}

func scanMediaPackageCandidate(profile string) func(scanner) (MediaPackageCandidate, error) {
	return func(row scanner) (MediaPackageCandidate, error) {
		var c MediaPackageCandidate
		var title, group sql.NullString
		var pkgID, pkgStatus, pkgError sql.NullString
		var pkgDurationMs, updatedAtMs sql.NullInt64
		if err := row.Scan(&c.MediaID, &c.Path, &title, &group, &c.DurationMs,
			&pkgID, &pkgStatus, &pkgError, &pkgDurationMs, &updatedAtMs); err != nil {
			return MediaPackageCandidate{}, err
		}
		c.RenditionProfile = profile
		c.Title = title.String
		c.SchedulingGroup = group.String
		applyPackageCandidateNulls(&c, pkgID, pkgStatus, pkgError, pkgDurationMs, updatedAtMs)
		return c, nil
	}
}

func scanMediaPackageCandidateAllProfiles(row scanner) (MediaPackageCandidate, error) {
	var c MediaPackageCandidate
	var title, group sql.NullString
	var pkgID, pkgStatus, pkgError sql.NullString
	var pkgDurationMs, updatedAtMs sql.NullInt64
	if err := row.Scan(&c.MediaID, &c.Path, &title, &group, &c.DurationMs,
		&pkgID, &c.RenditionProfile, &pkgStatus, &pkgError, &pkgDurationMs, &updatedAtMs); err != nil {
		return MediaPackageCandidate{}, err
	}
	c.Title = title.String
	c.SchedulingGroup = group.String
	applyPackageCandidateNulls(&c, pkgID, pkgStatus, pkgError, pkgDurationMs, updatedAtMs)
	return c, nil
}

func applyPackageCandidateNulls(c *MediaPackageCandidate, pkgID, pkgStatus, pkgError sql.NullString, pkgDurationMs, updatedAtMs sql.NullInt64) {
	if pkgID.Valid {
		v := pkgID.String
		c.PackageID = &v
	}
	if pkgStatus.Valid {
		v := pkgStatus.String
		c.PackageStatus = &v
	}
	if pkgError.Valid {
		v := pkgError.String
		c.PackageError = &v
	}
	if pkgDurationMs.Valid {
		v := pkgDurationMs.Int64
		c.PackagedDurationMs = &v
	}
	if updatedAtMs.Valid {
		v := updatedAtMs.Int64
		c.UpdatedAtMs = &v
	}
}

func scanPackageStatusSummary(row scanner) (PackageStatusSummary, error) {
	var s PackageStatusSummary
	err := row.Scan(&s.Status, &s.Count)
	return s, err
}

func scanPackageProfileSummary(row scanner) (PackageProfileSummary, error) {
	var s PackageProfileSummary
	var oldest, newest sql.NullInt64
	if err := row.Scan(&s.RenditionProfile, &s.Status, &s.PackageCount, &s.ReadyDurationMs, &oldest, &newest); err != nil {
		return PackageProfileSummary{}, err
	}
	applySummaryRange(&s.OldestUpdatedMs, &s.NewestUpdatedMs, oldest, newest)
	return s, nil
}

func scanChannelPackageSummary(row scanner) (ChannelPackageSummary, error) {
	var s ChannelPackageSummary
	var oldest, newest sql.NullInt64
	if err := row.Scan(&s.ChannelID, &s.DisplayName, &s.RenditionProfile, &s.Status, &s.PackageCount, &s.ReadyDurationMs, &oldest, &newest); err != nil {
		return ChannelPackageSummary{}, err
	}
	applySummaryRange(&s.OldestUpdatedMs, &s.NewestUpdatedMs, oldest, newest)
	return s, nil
}

func applySummaryRange(oldestTarget, newestTarget **int64, oldest, newest sql.NullInt64) {
	if oldest.Valid {
		v := oldest.Int64
		*oldestTarget = &v
	}
	if newest.Valid {
		v := newest.Int64
		*newestTarget = &v
	}
}

func scanChannelPackageNeedSummary(row scanner) (ChannelPackageNeedSummary, error) {
	var s ChannelPackageNeedSummary
	err := row.Scan(&s.ChannelID, &s.DisplayName, &s.RenditionProfile, &s.NeededCount, &s.ReadyCount, &s.ProcessingCount, &s.PendingCount, &s.FailedCount, &s.MissingCount)
	return s, err
}

func scanInvalidProfilePackage(row scanner) (InvalidProfilePackage, error) {
	var p InvalidProfilePackage
	var packageRoot sql.NullString
	if err := row.Scan(&p.ID, &p.MediaID, &p.RenditionProfile, &p.Status, &packageRoot); err != nil {
		return InvalidProfilePackage{}, err
	}
	p.PackageRoot = packageRoot.String
	return p, nil
}

func scanPackageRoot(row scanner) (PackageRoot, error) {
	var root PackageRoot
	err := row.Scan(&root.RenditionProfile, &root.Status, &root.PackageRoot)
	return root, err
}

func scanChannelPackageRoot(row scanner) (ChannelPackageRoot, error) {
	var root ChannelPackageRoot
	err := row.Scan(&root.ChannelID, &root.RenditionProfile, &root.Status, &root.PackageRoot)
	return root, err
}
