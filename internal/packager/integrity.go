package packager

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// IntegrityResult tallies what a ready-package sweep requeued, split by failure
// mode so the two are separable in metrics and logs.
type IntegrityResult struct {
	// FileReset counts ready packages requeued for missing/broken files.
	FileReset int64
	// DurationReset counts ready packages requeued as truncated — packaged
	// output materially short of the source media duration.
	DurationReset int64
	// DurationSkipped counts ready packages whose packaged or source duration
	// was unknown, so the truncation comparison could not run. These are logged
	// for visibility but left ready rather than requeued blindly.
	DurationSkipped int64
}

// DurationShortfall describes a ready package flagged by the duration audit:
// either its packaged output is materially short of the source (a likely
// truncated encode), or its source/packaged duration is unknown so the
// comparison could not be made (UnknownSource).
type DurationShortfall struct {
	PackageID     string
	MediaID       string
	Profile       string
	PackagedMs    int64
	SourceMs      int64
	ShortfallMs   int64
	ToleranceMs   int64
	UnknownSource bool
}

// CheckReadyPackageIntegrity verifies ready packages — filesystem outputs and
// packaged-vs-source duration — and moves broken or truncated rows back to
// pending so the normal worker claim path re-encodes them. The duration check
// shares PackagedDurationShortfall with the finalize guard so detection can
// never drift from what finalize rejects.
func CheckReadyPackageIntegrity(ctx context.Context, conn *sql.DB) (IntegrityResult, error) {
	var res IntegrityResult
	packages, err := db.ReadyMediaPackages(ctx, conn)
	if err != nil {
		return res, fmt.Errorf("list ready packages: %w", err)
	}
	durations, err := sourceDurations(ctx, conn, packages)
	if err != nil {
		return res, err
	}

	nowMs := time.Now().UTC().UnixMilli()
	for _, pkg := range packages {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		// File integrity first: a package with missing files is requeued
		// regardless of duration, so there is no point also probing its length.
		if err := validateReadyPackageFiles(pkg); err != nil {
			reason := fmt.Sprintf("package integrity check failed: %v", err)
			changed, markErr := db.MarkReadyPackagePendingForReencode(ctx, conn, pkg.ID, nowMs, reason)
			if markErr != nil {
				return res, fmt.Errorf("mark package %s pending: %w", pkg.ID, markErr)
			}
			if changed {
				res.FileReset++
				log.Printf("package integrity reset id=%s media=%s profile=%s reason=%q",
					pkg.ID, pkg.MediaID, pkg.RenditionProfile, reason)
			}
			continue
		}

		ds, flagged := evalPackageDuration(pkg, durations)
		if !flagged {
			continue
		}
		if ds.UnknownSource {
			res.DurationSkipped++
			log.Printf("package duration audit skipped id=%s media=%s profile=%s: unknown packaged/source duration",
				pkg.ID, pkg.MediaID, pkg.RenditionProfile)
			continue
		}
		reason := fmt.Sprintf("packaged duration %dms is %dms short of source %dms; encode likely truncated",
			ds.PackagedMs, ds.ShortfallMs, ds.SourceMs)
		changed, markErr := db.MarkReadyPackagePendingForReencode(ctx, conn, pkg.ID, nowMs, reason)
		if markErr != nil {
			return res, fmt.Errorf("mark package %s pending: %w", pkg.ID, markErr)
		}
		if changed {
			res.DurationReset++
			log.Printf("package duration reset id=%s media=%s profile=%s reason=%q",
				pkg.ID, pkg.MediaID, pkg.RenditionProfile, reason)
		}
	}
	return res, nil
}

// AuditReadyPackageDurations reports every ready package whose packaged duration
// falls materially short of its source (a likely truncated encode), plus any
// whose packaged/source duration is unknown so the check could not run. It is
// read-only: it reports, it does not requeue. The maint audit-duration command
// uses it for listing; CheckReadyPackageIntegrity shares the same per-package
// evaluation (evalPackageDuration) when it requeues.
func AuditReadyPackageDurations(ctx context.Context, conn *sql.DB) ([]DurationShortfall, error) {
	packages, err := db.ReadyMediaPackages(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("list ready packages: %w", err)
	}
	durations, err := sourceDurations(ctx, conn, packages)
	if err != nil {
		return nil, err
	}
	var out []DurationShortfall
	for _, pkg := range packages {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if ds, flagged := evalPackageDuration(pkg, durations); flagged {
			out = append(out, ds)
		}
	}
	return out, nil
}

// evalPackageDuration compares one ready package against its source duration.
// flagged is true when the package should be surfaced — either truncated, or
// unknown (DurationShortfall.UnknownSource) because the packaged or source
// duration is missing and the comparison cannot be made.
func evalPackageDuration(pkg db.MediaPackage, sourceMs map[string]int64) (ds DurationShortfall, flagged bool) {
	src := sourceMs[pkg.MediaID]
	ds = DurationShortfall{
		PackageID: pkg.ID,
		MediaID:   pkg.MediaID,
		Profile:   pkg.RenditionProfile,
		SourceMs:  src,
	}
	if pkg.PackagedDurationMs == nil || src <= 0 {
		ds.UnknownSource = true
		return ds, true
	}
	ds.PackagedMs = *pkg.PackagedDurationMs
	ds.ToleranceMs = durationShortfallTolerance(src)
	shortfall, truncated := PackagedDurationShortfall(ds.PackagedMs, src)
	ds.ShortfallMs = shortfall
	return ds, truncated
}

// sourceDurations batch-loads source media durations for the supplied packages,
// keyed by media ID. Media rows that no longer exist are simply absent (their
// packages then read as UnknownSource).
func sourceDurations(ctx context.Context, conn *sql.DB, packages []db.MediaPackage) (map[string]int64, error) {
	ids := make([]string, 0, len(packages))
	for _, p := range packages {
		ids = append(ids, p.MediaID)
	}
	media, err := db.MediaByIDs(ctx, conn, ids)
	if err != nil {
		return nil, fmt.Errorf("load source durations: %w", err)
	}
	out := make(map[string]int64, len(media))
	for id, m := range media {
		out[id] = m.DurationMs
	}
	return out, nil
}

func validateReadyPackageFiles(pkg db.MediaPackage) error {
	if pkg.InitSegmentPath == nil || *pkg.InitSegmentPath == "" {
		return fmt.Errorf("missing init_segment_path")
	}
	if err := requireRegularFile(*pkg.InitSegmentPath); err != nil {
		return fmt.Errorf("init segment %s: %w", *pkg.InitSegmentPath, err)
	}

	root, segments, err := readyPackageManifestSegments(pkg)
	if err != nil {
		return err
	}
	for _, seg := range segments {
		segmentPath := filepath.Join(root, filepath.FromSlash(seg.URI))
		if err := requireRegularFile(segmentPath); err != nil {
			return fmt.Errorf("segment %s: %w", segmentPath, err)
		}
	}
	return nil
}

// readyPackageManifestSegments resolves a ready package's manifest and returns
// its package root plus every segment the manifest lists, in order. It is the
// shared enumeration used by both validateReadyPackageFiles (the fail-fast
// requeue guard) and InspectMediaPackages (the read-only diagnostic) so the two
// can never disagree about which files a ready package is expected to have.
func readyPackageManifestSegments(pkg db.MediaPackage) (string, []HLSSegment, error) {
	if pkg.PackageRoot == nil || *pkg.PackageRoot == "" {
		return "", nil, fmt.Errorf("missing package_root")
	}
	root := *pkg.PackageRoot
	playlist := filepath.Join(root, "stream.m3u8")
	if err := requireRegularFile(playlist); err != nil {
		return "", nil, fmt.Errorf("manifest %s: %w", playlist, err)
	}
	segments, err := ParseHLSManifest(playlist)
	if err != nil {
		return "", nil, fmt.Errorf("parse manifest %s: %w", playlist, err)
	}
	if len(segments) == 0 {
		return "", nil, fmt.Errorf("manifest %s contains no segments", playlist)
	}
	return root, segments, nil
}

// PackageInspection is a read-only per-package integrity report: filesystem
// presence of every expected artifact plus packaged-vs-source duration status.
// Unlike CheckReadyPackageIntegrity it never requeues, and unlike
// validateReadyPackageFiles it enumerates every missing segment rather than
// stopping at the first. Checked is false for non-ready packages, where the
// file/duration fields are not meaningful.
type PackageInspection struct {
	PackageID       string
	MediaID         string
	Profile         string
	Status          string
	Checked         bool
	InitPresent     bool
	ManifestPresent bool
	SegmentCount    int
	MissingSegments []string
	// FileError records a structural problem that stopped enumeration: a
	// missing package_root, a missing/unparseable manifest, or an empty one.
	FileError       string
	PackagedMs      int64
	SourceMs        int64
	ShortfallMs     int64
	DurationUnknown bool
	Truncated       bool
	// OK is true only for a ready package with every file present, a known
	// duration, and no truncation.
	OK bool
}

// InspectMediaPackages returns a read-only integrity report for the packages of
// mediaID, or for every ready package when mediaID is empty. It mirrors the
// maint package-integrity check without mutating anything: every expected
// on-disk artifact is checked, every missing segment is reported, and each
// ready package is compared against its source duration via the same
// evalPackageDuration path CheckReadyPackageIntegrity uses to requeue. When
// mediaID is set, all of that media's packages are listed (including non-ready
// renditions, marked Checked=false) so the full ladder is visible.
func InspectMediaPackages(ctx context.Context, conn *sql.DB, mediaID string) ([]PackageInspection, error) {
	var packages []db.MediaPackage
	var err error
	if mediaID != "" {
		packages, err = db.MediaPackagesForMedia(ctx, conn, mediaID)
	} else {
		packages, err = db.ReadyMediaPackages(ctx, conn)
	}
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	durations, err := sourceDurations(ctx, conn, packages)
	if err != nil {
		return nil, err
	}

	out := make([]PackageInspection, 0, len(packages))
	for _, pkg := range packages {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		insp := PackageInspection{
			PackageID: pkg.ID,
			MediaID:   pkg.MediaID,
			Profile:   pkg.RenditionProfile,
			Status:    string(pkg.Status),
		}
		if pkg.Status != db.PackageStatusReady {
			out = append(out, insp)
			continue
		}
		insp.Checked = true

		fileRep := inspectPackageFiles(pkg)
		insp.InitPresent = fileRep.InitPresent
		insp.ManifestPresent = fileRep.ManifestPresent
		insp.SegmentCount = fileRep.SegmentCount
		insp.MissingSegments = fileRep.MissingSegments
		insp.FileError = fileRep.FileError

		ds, flagged := evalPackageDuration(pkg, durations)
		insp.PackagedMs = ds.PackagedMs
		insp.SourceMs = ds.SourceMs
		insp.ShortfallMs = ds.ShortfallMs
		if flagged {
			insp.DurationUnknown = ds.UnknownSource
			insp.Truncated = !ds.UnknownSource
		}

		insp.OK = insp.InitPresent && insp.ManifestPresent &&
			insp.FileError == "" && len(insp.MissingSegments) == 0 &&
			!insp.DurationUnknown && !insp.Truncated
		out = append(out, insp)
	}
	return out, nil
}

// inspectPackageFiles checks a ready package's on-disk artifacts read-only,
// collecting every missing segment instead of failing at the first. It shares
// readyPackageManifestSegments with validateReadyPackageFiles.
func inspectPackageFiles(pkg db.MediaPackage) PackageInspection {
	var rep PackageInspection
	rep.InitPresent = pkg.InitSegmentPath != nil && *pkg.InitSegmentPath != "" &&
		requireRegularFile(*pkg.InitSegmentPath) == nil

	root, segments, err := readyPackageManifestSegments(pkg)
	if err != nil {
		rep.FileError = err.Error()
		return rep
	}
	rep.ManifestPresent = true
	rep.SegmentCount = len(segments)
	for _, seg := range segments {
		if requireRegularFile(filepath.Join(root, filepath.FromSlash(seg.URI))) != nil {
			rep.MissingSegments = append(rep.MissingSegments, seg.URI)
		}
	}
	return rep
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	return nil
}
