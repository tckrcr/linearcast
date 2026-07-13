package admin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/lcingest"
	"github.com/tckrcr/linearcast/internal/packager"
)

type missingMediaItem struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Title string `json:"title,omitempty"`
}

type scanErrorItem struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Error string `json:"error"`
}

type missingMediaResponse struct {
	GeneratedAt string             `json:"generatedAt"`
	DryRun      bool               `json:"dryRun"`
	Checked     int                `json:"checked"`
	Missing     []missingMediaItem `json:"missing"`
	Errors      []scanErrorItem    `json:"errors,omitempty"`
	Deleted     int64              `json:"deleted"`
}

type mediaOrderingBackfillResponse struct {
	GeneratedAt string `json:"generatedAt"`
	Scanned     int    `json:"scanned"`
	Updated     int    `json:"updated"`
}

func (a *App) handleMaintenanceMediaOrderingBackfill(w http.ResponseWriter, r *http.Request) {
	result, err := lcingest.BackfillEpisodeOrdering(r.Context(), a.dbConn, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, mediaOrderingBackfillResponse{
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
		Scanned:     result.Scanned,
		Updated:     result.Updated,
	})
}

// handleMaintenanceMissingMedia scans the media table, checks whether each
// row's path still exists on disk, and (when dry-run=false) deletes the rows
// whose files are gone. Files that fail to stat with a non-ENOENT error are
// reported separately and never deleted — we only act on confirmed-missing
// rows.
//
// Query params:
//
//	dry-run: "false" to perform deletion; anything else (including absent)
//	         defaults to a dry-run that only reports what would be deleted.
func (a *App) handleMaintenanceMissingMedia(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry-run") != "false"

	rows, err := db.AllMediaIDPathTitle(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	resp, missingIDs := scanMissingMediaRows(a.now(), rows)
	resp.DryRun = dryRun

	if !dryRun && len(missingIDs) > 0 {
		deleted, err := db.DeleteMediaByIDs(r.Context(), a.dbConn, missingIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		resp.Deleted = deleted
	}

	writeJSON(w, resp)
}

func scanMissingMediaRows(now time.Time, rows []db.MediaIDPathTitle) (missingMediaResponse, []string) {
	resp := missingMediaResponse{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Checked:     len(rows),
		Missing:     []missingMediaItem{},
	}
	missingIDs := make([]string, 0)
	for _, row := range rows {
		_, err := os.Stat(row.Path)
		if err == nil {
			continue
		}
		if errors.Is(err, fs.ErrNotExist) {
			item := missingMediaItem{ID: row.ID, Path: row.Path}
			if row.Title != "" {
				item.Title = row.Title
			}
			resp.Missing = append(resp.Missing, item)
			missingIDs = append(missingIDs, row.ID)
			continue
		}
		resp.Errors = append(resp.Errors, scanErrorItem{
			ID:    row.ID,
			Path:  row.Path,
			Error: err.Error(),
		})
	}
	return resp, missingIDs
}

type unreferencedPackageCleanupItem struct {
	ID               string `json:"id"`
	MediaID          string `json:"mediaId"`
	RenditionProfile string `json:"renditionProfile"`
	Status           string `json:"status"`
	PackageRoot      string `json:"packageRoot,omitempty"`
	Bytes            int64  `json:"bytes,omitempty"`
	DiskSkipped      bool   `json:"diskSkipped,omitempty"`
}

type orphanPackageDirItem struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes,omitempty"`
}

type orphanPackagesResponse struct {
	GeneratedAt  string                           `json:"generatedAt"`
	DryRun       bool                             `json:"dryRun"`
	PackageRoot  string                           `json:"packageRoot,omitempty"`
	Unreferenced []unreferencedPackageCleanupItem `json:"unreferenced"`
	OrphanDirs   []orphanPackageDirItem           `json:"orphanDirs"`
	TotalBytes   int64                            `json:"totalBytes"`
	DeletedRows  int                              `json:"deletedRows"`
	DeletedDirs  int                              `json:"deletedDirs"`
	Warnings     []string                         `json:"warnings,omitempty"`
}

// handleMaintenanceOrphanPackages combines two cleanups:
//  1. media_packages rows whose media_id is not referenced by any schedule
//     entry — DB rows plus their on-disk package_root directories.
//  2. on-disk package directories under the effective package root that have no
//     corresponding media_packages row at all (true orphans from prior DBs
//     or manual moves).
//
// On-disk deletion is only attempted for paths under the effective package root;
// anything outside that tree is reported as diskSkipped.
func (a *App) handleMaintenanceOrphanPackages(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry-run") != "false"

	resp := orphanPackagesResponse{
		GeneratedAt:  a.now().UTC().Format(time.RFC3339Nano),
		DryRun:       dryRun,
		Unreferenced: []unreferencedPackageCleanupItem{},
		OrphanDirs:   []orphanPackageDirItem{},
	}

	var globalRoot string
	if a.cache.Root() != "" {
		globalRoot = filepath.Clean(a.cache.PackagesDir())
		resp.PackageRoot = globalRoot
	}

	// Part 1: unreferenced DB rows.
	unreferenced, err := db.UnreferencedPackages(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	items, bytes := buildUnreferencedPackageCleanupItems(globalRoot, unreferenced)
	resp.Unreferenced = append(resp.Unreferenced, items...)
	resp.TotalBytes += bytes

	// Part 2: on-disk orphan directories.
	if globalRoot != "" {
		known, err := db.PackageRoots(r.Context(), a.dbConn)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		knownSet := make(map[string]bool, len(known))
		for _, r := range known {
			knownSet[filepath.Clean(r)] = true
		}

		orphans, walkErrs := findOrphanPackageDirs(globalRoot, knownSet)
		for _, w := range walkErrs {
			resp.Warnings = append(resp.Warnings, w)
		}
		for _, path := range orphans {
			item := orphanPackageDirItem{Path: path}
			if n, err := dirSize(path); err == nil {
				item.Bytes = n
				resp.TotalBytes += n
			}
			resp.OrphanDirs = append(resp.OrphanDirs, item)
		}
	}

	if dryRun {
		writeJSON(w, resp)
		return
	}

	// Delete unreferenced DB rows. For each item we attempt disk cleanup first,
	// then always attempt DB row deletion regardless of disk outcome so the row
	// is removed from DB and the orphan-dir scan can catch any leftover dirs on
	// the next run. Errors are accumulated as warnings rather than aborting.
	for _, item := range resp.Unreferenced {
		if item.PackageRoot != "" && !item.DiskSkipped {
			if err := deletePackageContents(item.PackageRoot); err != nil {
				resp.Warnings = append(resp.Warnings, fmt.Sprintf("disk cleanup %s: %v", item.PackageRoot, err))
			}
		}
		if err := db.DeleteMediaPackage(r.Context(), a.dbConn, item.ID); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("delete package row %s: %v", item.ID, err))
			continue
		}
		resp.DeletedRows++
	}

	// Clean generated files from orphan disk dirs.
	for _, item := range resp.OrphanDirs {
		if err := deletePackageContents(item.Path); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("disk cleanup %s: %v", item.Path, err))
			continue
		}
		resp.DeletedDirs++
	}

	// Best-effort: prune now-empty media-slug directories at the top of the package root.
	if globalRoot != "" {
		pruneEmptyChildDirs(globalRoot)
	}

	writeJSON(w, resp)
}

func buildUnreferencedPackageCleanupItems(globalRoot string, packages []db.UnreferencedPackage) ([]unreferencedPackageCleanupItem, int64) {
	items := make([]unreferencedPackageCleanupItem, 0, len(packages))
	var totalBytes int64
	for _, p := range packages {
		item := unreferencedPackageCleanupItem{
			ID:               p.ID,
			MediaID:          p.MediaID,
			RenditionProfile: p.RenditionProfile,
			Status:           p.Status,
		}
		if p.PackageRoot != nil && *p.PackageRoot != "" {
			cleaned := filepath.Clean(*p.PackageRoot)
			item.PackageRoot = cleaned
			under := globalRoot != "" && strings.HasPrefix(cleaned, globalRoot+string(os.PathSeparator))
			if under {
				if n, err := dirSize(cleaned); err == nil {
					item.Bytes = n
					totalBytes += n
				}
			} else {
				item.DiskSkipped = true
			}
		}
		items = append(items, item)
	}
	return items, totalBytes
}

// findOrphanPackageDirs walks <root>/<media-slug>/<profile> and returns
// leaf paths not present in knownSet. Walk errors are returned as warnings
// rather than failing the whole operation.
func findOrphanPackageDirs(root string, knownSet map[string]bool) ([]string, []string) {
	var orphans []string
	var warnings []string

	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warnings = append(warnings, fmt.Sprintf("read %s: %v", root, err))
		}
		return orphans, warnings
	}

	for _, mediaEntry := range entries {
		if !mediaEntry.IsDir() {
			continue
		}
		mediaDir := filepath.Join(root, mediaEntry.Name())
		profiles, err := os.ReadDir(mediaDir)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("read %s: %v", mediaDir, err))
			continue
		}
		for _, profileEntry := range profiles {
			if !profileEntry.IsDir() {
				continue
			}
			profileDir := filepath.Clean(filepath.Join(mediaDir, profileEntry.Name()))
			if knownSet[profileDir] {
				continue
			}
			orphans = append(orphans, profileDir)
		}
	}
	return orphans, warnings
}

// deletePackageContents removes the known-generated files (init.mp4,
// stream.m3u8, seg*.m4s, and package-owned subtitle sidecars) from dir, then
// removes dir itself if it is now empty. Files with other names are never
// touched, so source files that happen to share the same directory are always
// preserved. The directory removal is best-effort and silently ignored when it
// fails (e.g. because non-generated files remain).
func deletePackageContents(dir string) error {
	if filepath.Base(filepath.Clean(dir)) == layout.SubtitlesDirName {
		return os.RemoveAll(dir)
	}
	var errs []string
	for _, name := range []string{layout.InitName, layout.PlaylistName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err.Error())
		}
	}
	segs, _ := filepath.Glob(filepath.Join(dir, layout.SegmentGlob))
	for _, seg := range segs {
		if err := os.Remove(seg); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err.Error())
		}
	}
	if err := os.RemoveAll(layout.PackageSubtitleDir(dir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	_ = os.Remove(dir) // succeeds only when dir is now empty
	return nil
}

// pruneEmptyChildDirs removes immediate child directories of root that are
// empty. Best-effort: errors are silently ignored so the response stays
// authoritative about what actually got deleted.
func pruneEmptyChildDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(root, e.Name())
		children, err := os.ReadDir(full)
		if err != nil || len(children) > 0 {
			continue
		}
		_ = os.Remove(full)
	}
}

type optimizeDBResponse struct {
	GeneratedAt string `json:"generatedAt"`
	DurationMs  int64  `json:"durationMs"`
	SizeBefore  int64  `json:"sizeBefore"`
	SizeAfter   int64  `json:"sizeAfter"`
}

// handleMaintenanceOptimizeDB runs `PRAGMA optimize` followed by `VACUUM` on
// the SQLite database. VACUUM rewrites the database file, reclaiming free
// pages, and blocks all other writes for the duration; on a large DB this can
// take minutes. The endpoint returns size-before/after so the caller can show
// how much space was reclaimed.
func (a *App) handleMaintenanceOptimizeDB(w http.ResponseWriter, r *http.Request) {
	if a.dbPath == "" {
		writeError(w, http.StatusServiceUnavailable, "no_db_path",
			"db path unknown; optimize requires a configured database file")
		return
	}

	sizeBefore, _ := fileSize(a.dbPath)

	start := time.Now()
	if _, err := a.dbConn.ExecContext(r.Context(), `PRAGMA optimize`); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", fmt.Sprintf("optimize: %v", err))
		return
	}
	if _, err := a.dbConn.ExecContext(r.Context(), `VACUUM`); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", fmt.Sprintf("vacuum: %v", err))
		return
	}
	durMs := time.Since(start).Milliseconds()

	sizeAfter, _ := fileSize(a.dbPath)

	writeJSON(w, optimizeDBResponse{
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
		DurationMs:  durMs,
		SizeBefore:  sizeBefore,
		SizeAfter:   sizeAfter,
	})
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

type packageIntegrityItem struct {
	PackageID       string   `json:"packageId"`
	MediaID         string   `json:"mediaId"`
	Profile         string   `json:"profile"`
	Status          string   `json:"status"`
	Checked         bool     `json:"checked"`
	InitPresent     bool     `json:"initPresent"`
	ManifestPresent bool     `json:"manifestPresent"`
	SegmentCount    int      `json:"segmentCount"`
	MissingSegments []string `json:"missingSegments,omitempty"`
	FileError       string   `json:"fileError,omitempty"`
	PackagedMs      int64    `json:"packagedMs,omitempty"`
	SourceMs        int64    `json:"sourceMs,omitempty"`
	ShortfallMs     int64    `json:"shortfallMs,omitempty"`
	DurationUnknown bool     `json:"durationUnknown,omitempty"`
	Truncated       bool     `json:"truncated,omitempty"`
	OK              bool     `json:"ok"`
}

type packageIntegrityResponse struct {
	GeneratedAt     string                 `json:"generatedAt"`
	MediaID         string                 `json:"mediaId,omitempty"`
	Checked         int                    `json:"checked"`
	Problems        int                    `json:"problems"`
	UnknownDuration int                    `json:"unknownDuration"`
	Packages        []packageIntegrityItem `json:"packages"`
}

// handleMaintenancePackageIntegrity reports the on-disk and duration integrity
// of ready packages without changing anything — the read-only diagnostic
// counterpart of the maint package-integrity check. With ?media=<id> it lists
// every package of that media (including non-ready renditions); without it,
// every ready package is swept.
func (a *App) handleMaintenancePackageIntegrity(w http.ResponseWriter, r *http.Request) {
	mediaID := strings.TrimSpace(r.URL.Query().Get("media"))
	inspections, err := packager.InspectMediaPackages(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	resp := packageIntegrityResponse{
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
		MediaID:     mediaID,
		Packages:    make([]packageIntegrityItem, 0, len(inspections)),
	}
	for _, insp := range inspections {
		resp.Packages = append(resp.Packages, packageIntegrityItem{
			PackageID:       insp.PackageID,
			MediaID:         insp.MediaID,
			Profile:         insp.Profile,
			Status:          insp.Status,
			Checked:         insp.Checked,
			InitPresent:     insp.InitPresent,
			ManifestPresent: insp.ManifestPresent,
			SegmentCount:    insp.SegmentCount,
			MissingSegments: insp.MissingSegments,
			FileError:       insp.FileError,
			PackagedMs:      insp.PackagedMs,
			SourceMs:        insp.SourceMs,
			ShortfallMs:     insp.ShortfallMs,
			DurationUnknown: insp.DurationUnknown,
			Truncated:       insp.Truncated,
			OK:              insp.OK,
		})
		if !insp.Checked {
			continue
		}
		resp.Checked++
		if !insp.OK {
			resp.Problems++
		}
		if insp.DurationUnknown {
			resp.UnknownDuration++
		}
	}
	writeJSON(w, resp)
}

type encodeReclaimItem struct {
	MediaID     string `json:"mediaId"`
	PackageID   string `json:"packageId"`
	Profile     string `json:"profile"`
	Status      string `json:"status"`
	PackageRoot string `json:"packageRoot,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	Referenced  bool   `json:"referenced"`
	Skipped     bool   `json:"skipped"`
	Deleted     bool   `json:"deleted"`
}

type encodeReclaimResponse struct {
	GeneratedAt string `json:"generatedAt"`
	DryRun      bool   `json:"dryRun"`
	Force       bool   `json:"force"`
	Candidates  int    `json:"candidates"`
	DeletedRows int    `json:"deletedRows"`
	SkippedRows int    `json:"skippedRows"`
	// TotalBytes is the on-disk size of the packages eligible for deletion
	// (skipped ones excluded). It is reported on dry-run too, so it answers
	// "how much would this free" before committing.
	TotalBytes int64               `json:"totalBytes"`
	Items      []encodeReclaimItem `json:"items"`
	Warnings   []string            `json:"warnings,omitempty"`
}

// reclaimMediaEncodes deletes the package rows and on-disk artifacts for the
// given media, scoped to one rendition profile when profile is non-empty.
// packaged_segments rows fall away via ON DELETE CASCADE. A media still
// referenced by any channel (scheduled or pooled) is skipped and reported
// unless force is set, so the operation can never strand a live channel by
// accident. When dryRun is true nothing is touched; the response reports what
// would be reclaimed. ReclaimedBytes counts the bytes that were (or would be)
// deleted; skipped packages are excluded.
func (a *App) reclaimMediaEncodes(ctx context.Context, mediaIDs []string, profile string, force, dryRun bool) (encodeReclaimResponse, error) {
	res := encodeReclaimResponse{
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
		DryRun:      dryRun,
		Force:       force,
		Items:       []encodeReclaimItem{},
	}
	if len(mediaIDs) == 0 {
		return res, nil
	}

	referenced, err := db.MediaIDsReferenced(ctx, a.dbConn, mediaIDs)
	if err != nil {
		return res, err
	}

	var globalRoot string
	if a.cache.Root() != "" {
		globalRoot = filepath.Clean(a.cache.PackagesDir())
	}

	for _, mediaID := range mediaIDs {
		pkgs, err := db.MediaPackagesForMedia(ctx, a.dbConn, mediaID)
		if err != nil {
			return res, err
		}
		isRef := referenced[mediaID]
		for _, p := range pkgs {
			if profile != "" && p.RenditionProfile != profile {
				continue
			}
			item := encodeReclaimItem{
				MediaID:    mediaID,
				PackageID:  p.ID,
				Profile:    p.RenditionProfile,
				Status:     string(p.Status),
				Referenced: isRef,
			}
			if p.PackageRoot != nil && *p.PackageRoot != "" {
				item.PackageRoot = filepath.Clean(*p.PackageRoot)
				if n, err := dirSize(item.PackageRoot); err == nil {
					item.Bytes = n
				}
			}
			res.Candidates++

			if isRef && !force {
				item.Skipped = true
				res.SkippedRows++
				res.Items = append(res.Items, item)
				continue
			}

			// Eligible for deletion; TotalBytes reflects this on dry-run too.
			res.TotalBytes += item.Bytes
			if dryRun {
				res.Items = append(res.Items, item)
				continue
			}

			// Disk first, then the row — mirrors handleMaintenanceOrphanPackages
			// so a failed disk cleanup still removes the row and the next
			// orphan-dir sweep can finish the job.
			if item.PackageRoot != "" {
				if err := deletePackageContents(item.PackageRoot); err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("disk cleanup %s: %v", item.PackageRoot, err))
				}
			}
			if err := db.DeleteMediaPackage(ctx, a.dbConn, p.ID); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("delete package row %s: %v", p.ID, err))
				res.Items = append(res.Items, item)
				continue
			}

			item.Deleted = true
			res.DeletedRows++
			res.Items = append(res.Items, item)
		}
	}

	if !dryRun && globalRoot != "" {
		pruneEmptyChildDirs(globalRoot)
	}
	return res, nil
}

type orphanEncodeItem struct {
	MediaID     string `json:"mediaId"`
	Title       string `json:"title,omitempty"`
	PackageID   string `json:"packageId"`
	Profile     string `json:"profile"`
	Status      string `json:"status"`
	PackageRoot string `json:"packageRoot,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	// Parked means the media is held only by a disabled channel (no active
	// channel references it, but some channel still does). Parked encodes are
	// surfaced but skipped unless ?include-parked=true, so re-enabling a parked
	// channel doesn't find its encodes gone.
	Parked  bool `json:"parked"`
	Deleted bool `json:"deleted"`
}

type orphanEncodesResponse struct {
	GeneratedAt   string `json:"generatedAt"`
	DryRun        bool   `json:"dryRun"`
	IncludeParked bool   `json:"includeParked"`
	Candidates    int    `json:"candidates"`
	ParkedRows    int    `json:"parkedRows"`
	DeletedRows   int    `json:"deletedRows"`
	// TotalBytes is the on-disk size of the encodes eligible for deletion
	// (parked ones excluded unless includeParked). Reported on dry-run too, so it
	// answers "how much would this free" before committing.
	TotalBytes int64              `json:"totalBytes"`
	Items      []orphanEncodeItem `json:"items"`
	Warnings   []string           `json:"warnings,omitempty"`
}

// handleMaintenanceOrphanEncodes reclaims encodes whose media is referenced by
// no active (enabled) channel — the defense-in-depth backstop for the "every
// media should trace to an active channel" invariant, and the named replacement
// for the old type-a-media-id delete tool. Items carry the media title so an
// operator never has to recognise a raw id. Defaults to a dry-run; pass
// ?dry-run=false to delete. Encodes held only by a *disabled* channel are
// reported as parked and skipped unless ?include-parked=true.
func (a *App) handleMaintenanceOrphanEncodes(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry-run") != "false"
	includeParked := r.URL.Query().Get("include-parked") == "true"

	pkgs, err := db.OrphanedEncodePackages(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	resp := orphanEncodesResponse{
		GeneratedAt:   a.now().UTC().Format(time.RFC3339Nano),
		DryRun:        dryRun,
		IncludeParked: includeParked,
		Items:         []orphanEncodeItem{},
	}

	// Resolve titles + parked classification for the distinct media in one pass.
	mediaSet := make(map[string]struct{}, len(pkgs))
	for _, p := range pkgs {
		mediaSet[p.MediaID] = struct{}{}
	}
	mediaIDs := make([]string, 0, len(mediaSet))
	for id := range mediaSet {
		mediaIDs = append(mediaIDs, id)
	}
	titles := make(map[string]string, len(mediaIDs))
	if len(mediaIDs) > 0 {
		media, err := db.MediaByIDs(r.Context(), a.dbConn, mediaIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		for id, m := range media {
			titles[id] = m.Title
		}
	}
	// OrphanedEncodePackages already excluded active-channel media, so anything
	// MediaIDsReferenced still reports is parked on a disabled channel.
	referenced, err := db.MediaIDsReferenced(r.Context(), a.dbConn, mediaIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	var globalRoot string
	if a.cache.Root() != "" {
		globalRoot = filepath.Clean(a.cache.PackagesDir())
	}

	for _, p := range pkgs {
		item := orphanEncodeItem{
			MediaID:   p.MediaID,
			Title:     titles[p.MediaID],
			PackageID: p.ID,
			Profile:   p.RenditionProfile,
			Status:    p.Status,
			Parked:    referenced[p.MediaID],
		}
		if p.PackageRoot != nil && *p.PackageRoot != "" {
			item.PackageRoot = filepath.Clean(*p.PackageRoot)
			if n, err := dirSize(item.PackageRoot); err == nil {
				item.Bytes = n
			}
		}
		resp.Candidates++
		if item.Parked {
			resp.ParkedRows++
		}

		// Skip parked encodes unless explicitly included — a disabled channel
		// still pools them and may be re-enabled.
		if item.Parked && !includeParked {
			resp.Items = append(resp.Items, item)
			continue
		}

		// Eligible for deletion; TotalBytes reflects this on dry-run too.
		resp.TotalBytes += item.Bytes
		if dryRun {
			resp.Items = append(resp.Items, item)
			continue
		}

		// Disk first, then the row — mirrors handleMaintenanceOrphanPackages so a
		// failed disk cleanup still removes the row and the next sweep finishes.
		if item.PackageRoot != "" {
			if err := deletePackageContents(item.PackageRoot); err != nil {
				resp.Warnings = append(resp.Warnings, fmt.Sprintf("disk cleanup %s: %v", item.PackageRoot, err))
			}
		}
		if err := db.DeleteMediaPackage(r.Context(), a.dbConn, p.ID); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("delete package row %s: %v", p.ID, err))
			resp.Items = append(resp.Items, item)
			continue
		}
		item.Deleted = true
		resp.DeletedRows++
		resp.Items = append(resp.Items, item)
	}

	if !dryRun && globalRoot != "" {
		pruneEmptyChildDirs(globalRoot)
	}
	writeJSON(w, resp)
}

// handleMaintenancePackageDelete reclaims a single media's encodes (package rows
// + on-disk artifacts), optionally limited to one ?profile=. It defaults to a
// dry-run; pass ?dry-run=false to delete. A media still referenced by a channel
// is skipped unless ?force=true.
func (a *App) handleMaintenancePackageDelete(w http.ResponseWriter, r *http.Request) {
	mediaID := strings.TrimSpace(r.URL.Query().Get("media"))
	if mediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media", "media query parameter is required")
		return
	}
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	force := r.URL.Query().Get("force") == "true"
	dryRun := r.URL.Query().Get("dry-run") != "false"

	res, err := a.reclaimMediaEncodes(r.Context(), []string{mediaID}, profile, force, dryRun)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, res)
}

type packageIntegrityRepairResponse struct {
	GeneratedAt     string `json:"generatedAt"`
	FileReset       int64  `json:"fileReset"`
	DurationReset   int64  `json:"durationReset"`
	DurationSkipped int64  `json:"durationSkipped"`
}

// handleMaintenancePackageIntegrityRepair runs CheckReadyPackageIntegrity —
// the same sweep the encoder sweeper runs on its timer — on demand. Ready
// packages with missing/mismatched files or truncated durations are requeued
// for re-encoding.
func (a *App) handleMaintenancePackageIntegrityRepair(w http.ResponseWriter, r *http.Request) {
	result, err := packager.CheckReadyPackageIntegrity(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "check_error", err.Error())
		return
	}
	writeJSON(w, packageIntegrityRepairResponse{
		GeneratedAt:     a.now().UTC().Format(time.RFC3339Nano),
		FileReset:       result.FileReset,
		DurationReset:   result.DurationReset,
		DurationSkipped: result.DurationSkipped,
	})
}

type packageRequeueResponse struct {
	GeneratedAt string `json:"generatedAt"`
	PackageID   string `json:"packageId"`
	MediaID     string `json:"mediaId"`
	Profile     string `json:"profile"`
	Status      string `json:"status"`
	Requeued    bool   `json:"requeued"`
}

// handleMaintenancePackageRequeue requeues a single ready package for
// re-encoding by ID. The package must currently be in ready status; packages
// already pending or processing are reported with requeued=false. Use the GET
// package-integrity endpoint to find broken package IDs before calling this.
func (a *App) handleMaintenancePackageRequeue(w http.ResponseWriter, r *http.Request) {
	packageID := r.PathValue("packageID")

	pkg, err := db.MediaPackageByID(r.Context(), a.dbConn, packageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if pkg == nil {
		writeError(w, http.StatusNotFound, "not_found", "package not found")
		return
	}

	requeued, err := db.MarkReadyPackagePendingForReencode(
		r.Context(), a.dbConn, packageID, a.now().UTC().UnixMilli(), "manual requeue via admin API")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	status := string(pkg.Status)
	if requeued {
		status = string(db.PackageStatusPending)
	}
	writeJSON(w, packageRequeueResponse{
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
		PackageID:   pkg.ID,
		MediaID:     pkg.MediaID,
		Profile:     pkg.RenditionProfile,
		Status:      status,
		Requeued:    requeued,
	})
}

type importPackageItem struct {
	MediaID      string `json:"mediaId"`
	Profile      string `json:"profile"`
	PackageID    string `json:"packageId"`
	SegmentCount int    `json:"segmentCount"`
	DurationMs   int64  `json:"durationMs"`
}

type importSkipItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type importPackagesResponse struct {
	GeneratedAt  string              `json:"generatedAt"`
	Scanned      int                 `json:"scanned"`
	Imported     []importPackageItem `json:"imported"`
	AlreadyReady int                 `json:"alreadyReady"`
	NeedsMedia   []string            `json:"needsMedia"`
	Skipped      []importSkipItem    `json:"skipped"`
}

// handleMaintenanceImportPackages rebuilds DB rows for finalized packages found
// on disk whose media rows already exist (e.g. after rescanning source folders
// into a fresh database), without re-encoding. See packager.ImportPackages.
func (a *App) handleMaintenanceImportPackages(w http.ResponseWriter, r *http.Request) {
	if a.cache.Root() == "" {
		writeError(w, http.StatusInternalServerError, "package_root_missing", "CACHE_DIR is required")
		return
	}
	report, err := packager.ImportPackages(r.Context(), a.dbConn, packager.ImportOptions{
		OutputRoot: a.cache.PackagesDir(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "import_failed", err.Error())
		return
	}

	resp := importPackagesResponse{
		GeneratedAt:  a.now().UTC().Format(time.RFC3339Nano),
		Scanned:      report.Scanned,
		AlreadyReady: report.AlreadyReady,
		Imported:     make([]importPackageItem, 0, len(report.Imported)),
		NeedsMedia:   report.NeedsMedia,
		Skipped:      make([]importSkipItem, 0, len(report.Skipped)),
	}
	if resp.NeedsMedia == nil {
		resp.NeedsMedia = []string{}
	}
	for _, it := range report.Imported {
		resp.Imported = append(resp.Imported, importPackageItem{
			MediaID:      it.MediaID,
			Profile:      it.RenditionProfile,
			PackageID:    it.PackageID,
			SegmentCount: it.SegmentCount,
			DurationMs:   it.DurationMs,
		})
	}
	for _, sk := range report.Skipped {
		resp.Skipped = append(resp.Skipped, importSkipItem{Path: sk.Path, Reason: sk.Reason})
	}
	writeJSON(w, resp)
}
