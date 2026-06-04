package admin

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
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
	if root := a.effectivePackageRoot(); root != "" {
		globalRoot = filepath.Clean(root)
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
// stream.m3u8, seg*.m4s) from dir, then removes dir itself if it is now
// empty. Files with other names are never touched, so source files that
// happen to share the same directory are always preserved. The directory
// removal is best-effort and silently ignored when it fails (e.g. because
// non-generated files remain).
func deletePackageContents(dir string) error {
	var errs []string
	for _, name := range []string{"init.mp4", "stream.m3u8"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err.Error())
		}
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "seg*.m4s"))
	for _, seg := range segs {
		if err := os.Remove(seg); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err.Error())
		}
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
