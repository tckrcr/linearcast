package packager

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
)

// ImportOptions configures a disaster-recovery import of on-disk packages.
type ImportOptions struct {
	// OutputRoot is the package root that finalized encodes live under
	// (OUTPUT_ROOT/<mediaID>/<rendition_profile>). Required.
	OutputRoot string
}

// ImportResult identifies one package that was (or would be) imported.
type ImportResult struct {
	MediaID          string
	RenditionProfile string
	PackageID        string
	SegmentCount     int
	DurationMs       int64
}

// ImportSkip records a package-looking directory that could not be imported.
type ImportSkip struct {
	Path   string
	Reason string
}

// ImportReport summarizes an ImportPackages run.
type ImportReport struct {
	// Scanned counts directories that look like packages (contain stream.m3u8).
	Scanned int
	// Imported lists packages rebuilt into the DB.
	Imported []ImportResult
	// AlreadyReady counts packages that already have a ready row (no-op).
	AlreadyReady int
	// NeedsMedia lists media IDs whose package dirs exist on disk but have no
	// media row — rescan source folders first, then re-run import.
	NeedsMedia []string
	// Skipped lists package dirs that could not be matched or rebuilt.
	Skipped []ImportSkip
}

// ImportPackages rebuilds media_packages + packaged_segments rows for finalized
// packages found on disk, without re-encoding. It is the disaster-recovery
// counterpart to packaging: after a lost DB is rehydrated by rescanning source
// folders (which restores media rows with their deterministic IDs), this walks
// OUTPUT_ROOT and re-attaches each package whose <mediaID> now matches a media
// row by rebuilding its rows from the package's own files (segment timing from
// stream.m3u8, codecs/duration from probing the package — see FinalizePackage).
// The run is idempotent: packages already marked ready are left untouched.
func ImportPackages(ctx context.Context, conn *sql.DB, opts ImportOptions) (ImportReport, error) {
	var rep ImportReport
	if opts.OutputRoot == "" {
		return rep, errors.New("output root is required")
	}

	profiles, err := db.AllPackageProfileNames(ctx, conn)
	if err != nil {
		return rep, err
	}
	profileSet := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		profileSet[p] = true
	}

	pkgDirs, err := findImportPackageDirs(opts.OutputRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, err
	}
	rep.Scanned = len(pkgDirs)

	mediaIDs := make([]string, 0, len(pkgDirs))
	seenMediaID := map[string]struct{}{}
	for _, pkgDir := range pkgDirs {
		if _, ok := seenMediaID[pkgDir.mediaID]; ok {
			continue
		}
		seenMediaID[pkgDir.mediaID] = struct{}{}
		mediaIDs = append(mediaIDs, pkgDir.mediaID)
	}
	mediaByID, err := db.MediaByIDs(ctx, conn, mediaIDs)
	if err != nil {
		return rep, err
	}
	readyPackages, err := db.ReadyMediaPackages(ctx, conn)
	if err != nil {
		return rep, err
	}
	readySet := make(map[packageIdentity]struct{}, len(readyPackages))
	for _, p := range readyPackages {
		readySet[packageIdentity{mediaID: p.MediaID, profile: p.RenditionProfile}] = struct{}{}
	}

	seenNeedsMedia := map[string]bool{}
	for _, pkgDir := range pkgDirs {
		media, ok := mediaByID[pkgDir.mediaID]
		if !ok {
			if !seenNeedsMedia[pkgDir.mediaID] {
				seenNeedsMedia[pkgDir.mediaID] = true
				rep.NeedsMedia = append(rep.NeedsMedia, pkgDir.mediaID)
			}
			continue
		}

		profile, ok := resolvePackageIdentity(pkgDir.dirName, profileSet)
		if !ok {
			rep.Skipped = append(rep.Skipped, ImportSkip{Path: pkgDir.path, Reason: "no active profile matches directory name"})
			continue
		}

		if _, ok := readySet[packageIdentity{mediaID: pkgDir.mediaID, profile: profile}]; ok {
			rep.AlreadyReady++
			continue
		}

		res, err := rebuildPackage(ctx, conn, media, profile, layout.ID(pkgDir.mediaID, profile), opts)
		if err != nil {
			rep.Skipped = append(rep.Skipped, ImportSkip{Path: pkgDir.path, Reason: err.Error()})
			continue
		}
		rep.Imported = append(rep.Imported, res)
	}
	return rep, nil
}

type importPackageDir struct {
	mediaID string
	dirName string
	path    string
}

type packageIdentity struct {
	mediaID string
	profile string
}

func findImportPackageDirs(outputRoot string) ([]importPackageDir, error) {
	mediaDirs, err := os.ReadDir(outputRoot)
	if err != nil {
		return nil, err
	}

	var out []importPackageDir
	for _, mediaEntry := range mediaDirs {
		if !mediaEntry.IsDir() {
			continue
		}
		mediaPath := filepath.Join(outputRoot, mediaEntry.Name())
		pkgDirs, err := os.ReadDir(mediaPath)
		if err != nil {
			return nil, err
		}
		for _, pkgEntry := range pkgDirs {
			if !pkgEntry.IsDir() {
				continue
			}
			packageRoot := filepath.Join(mediaPath, pkgEntry.Name())
			// A real finalized package carries an HLS playlist; anything else under
			// the media dir is not ours to import.
			if !fileExists(layout.PlaylistPath(packageRoot)) {
				continue
			}
			out = append(out, importPackageDir{
				mediaID: mediaEntry.Name(),
				dirName: pkgEntry.Name(),
				path:    packageRoot,
			})
		}
	}
	return out, nil
}

// resolvePackageIdentity maps a package directory name back to its rendition
// profile. Package output lives under <mediaID>/<profile>, so a directory named
// exactly after an active profile is a package; anything else is not ours.
func resolvePackageIdentity(dirName string, profileSet map[string]bool) (profile string, ok bool) {
	if profileSet[dirName] {
		return dirName, true
	}
	return "", false
}

// rebuildPackage recreates the DB rows for an existing on-disk package without
// re-encoding, mirroring PackageOne's post-encode tail.
func rebuildPackage(ctx context.Context, conn *sql.DB, media db.Media, profile, packageID string, opts ImportOptions) (ImportResult, error) {
	nowMs := time.Now().UTC().UnixMilli()
	packageRoot := layout.PackageRoot(opts.OutputRoot, media.ID, profile)
	pkg := db.MediaPackage{
		ID:               packageID,
		MediaID:          media.ID,
		RenditionProfile: profile,
		Status:           db.PackageStatusProcessing,
		SegmentBasePath:  packageRoot,
		Container:        "fmp4",
		CreatedAtMs:      nowMs,
		UpdatedAtMs:      nowMs,
	}
	pr := packageRoot
	ip := layout.InitPath(packageRoot)
	pkg.PackageRoot = &pr
	pkg.InitSegmentPath = &ip
	if err := db.MarkPackageProcessing(ctx, conn, pkg); err != nil {
		return ImportResult{}, err
	}

	res, finalized, err := FinalizePackage(ctx, conn, FinalizeOptions{
		MediaPath:        media.Path,
		MediaID:          media.ID,
		Profile:          profile,
		OutputRoot:       opts.OutputRoot,
		PackageID:        packageID,
		NowMs:            nowMs,
		SourceDurationMs: media.DurationMs,
	})
	if err != nil {
		// Don't leave the row stuck in processing after a failed rebuild.
		_ = db.MarkPackageFailed(ctx, conn, pkg, err, time.Now().UTC().UnixMilli())
		return ImportResult{}, err
	}

	pkg.Status = db.PackageStatusReady
	applyFinalizedPackage(&pkg, finalized)
	pkg.Error = nil
	pkg.UpdatedAtMs = time.Now().UTC().UnixMilli()
	if err := db.MarkPackageReady(ctx, conn, pkg); err != nil {
		return ImportResult{}, err
	}

	return ImportResult{
		MediaID:          media.ID,
		RenditionProfile: profile,
		PackageID:        packageID,
		SegmentCount:     res.SegmentCount,
		DurationMs:       res.DurationMs,
	}, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
