package admin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

type cacheSummaryResponse struct {
	GeneratedAt      string                       `json:"generatedAt"`
	CacheRoot        string                       `json:"cacheRoot,omitempty"`
	PackageRoot      string                       `json:"packageRoot,omitempty"`
	CacheRootBytes   *int64                       `json:"cacheRootBytes,omitempty"`
	PackageRootBytes *int64                       `json:"packageRootBytes,omitempty"`
	PackageBytes     *int64                       `json:"packageBytes,omitempty"`
	PackageRootCount int                          `json:"packageRootCount"`
	EncoderCount     int                          `json:"encoderCount"`
	StatusCounts     []cachePackageStatusSummary  `json:"statusCounts"`
	PackageSummaries []cachePackageProfileSummary `json:"packageSummaries"`
	ChannelSummaries []cacheChannelPackageSummary `json:"channelSummaries"`
	ChannelNeeds     []cacheChannelPackageNeed    `json:"channelNeeds"`
	Warnings         []string                     `json:"warnings,omitempty"`
}

type cachePackageStatusSummary struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type cachePackageProfileSummary struct {
	RenditionProfile string `json:"renditionProfile"`
	Status           string `json:"status"`
	PackageCount     int64  `json:"packageCount"`
	PackageBytes     int64  `json:"packageBytes"`
	ReadyDurationMs  int64  `json:"readyDurationMs"`
	OldestUpdatedMs  *int64 `json:"oldestUpdatedMs,omitempty"`
	NewestUpdatedMs  *int64 `json:"newestUpdatedMs,omitempty"`
	Invalid          bool   `json:"invalid,omitempty"`
	Disabled         bool   `json:"disabled,omitempty"`
}

type cacheChannelPackageSummary struct {
	ChannelID        string `json:"channelId"`
	DisplayName      string `json:"displayName"`
	RenditionProfile string `json:"renditionProfile"`
	Status           string `json:"status"`
	PackageCount     int64  `json:"packageCount"`
	PackageBytes     int64  `json:"packageBytes"`
	ReadyDurationMs  int64  `json:"readyDurationMs"`
	OldestUpdatedMs  *int64 `json:"oldestUpdatedMs,omitempty"`
	NewestUpdatedMs  *int64 `json:"newestUpdatedMs,omitempty"`
}

type cacheChannelPackageNeed struct {
	ChannelID        string `json:"channelId"`
	DisplayName      string `json:"displayName"`
	RenditionProfile string `json:"renditionProfile"`
	NeededCount      int64  `json:"neededCount"`
	ReadyCount       int64  `json:"readyCount"`
	ProcessingCount  int64  `json:"processingCount"`
	PendingCount     int64  `json:"pendingCount"`
	FailedCount      int64  `json:"failedCount"`
	MissingCount     int64  `json:"missingCount"`
	RemainingCount   int64  `json:"remainingCount"`
}

type invalidProfileCleanupItem struct {
	ID          string `json:"id"`
	MediaID     string `json:"mediaId"`
	Profile     string `json:"profile"`
	Status      string `json:"status"`
	PackageRoot string `json:"packageRoot,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	DiskSkipped bool   `json:"diskSkipped,omitempty"`
}

type invalidProfileCleanupResponse struct {
	DryRun     bool                        `json:"dryRun"`
	Removed    []invalidProfileCleanupItem `json:"removed"`
	TotalBytes int64                       `json:"totalBytes"`
}

type unreferencedPackageItem struct {
	ID                 string `json:"id"`
	MediaID            string `json:"mediaId"`
	RenditionProfile   string `json:"renditionProfile"`
	Status             string `json:"status"`
	PackageRoot        string `json:"packageRoot,omitempty"`
	PackagedDurationMs int64  `json:"packagedDurationMs,omitempty"`
	Bytes              int64  `json:"bytes,omitempty"`
}

type unreferencedPackagesResponse struct {
	GeneratedAt string                    `json:"generatedAt"`
	Count       int                       `json:"count"`
	TotalBytes  int64                     `json:"totalBytes"`
	Packages    []unreferencedPackageItem `json:"packages"`
}

func (a *App) handleCacheSummary(w http.ResponseWriter, r *http.Request) {
	statusRows, err := db.PackageStatusSummaries(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	channelRows, err := db.ChannelPackageSummaries(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	packageRows, err := db.PackageProfileSummaries(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	needRows, err := db.ChannelPackageNeedSummaries(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	packageRoots, err := db.PackageRoots(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	packageRootRows, err := db.PackageRootSummaries(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	channelRoots, err := db.ChannelPackageRoots(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	profileRecords, err := db.AllPackageProfileRecords(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	knownProfileSet := make(map[string]bool, len(profileRecords))
	disabledProfileSet := make(map[string]bool, len(profileRecords))
	for _, p := range profileRecords {
		knownProfileSet[p.Profile.Name] = true
		if p.Disabled {
			disabledProfileSet[p.Profile.Name] = true
		}
	}

	var encoderCount int
	_ = a.dbConn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM encoders WHERE revoked_at_ms IS NULL`).Scan(&encoderCount)

	resp := cacheSummaryResponse{
		GeneratedAt:      a.now().UTC().Format(time.RFC3339Nano),
		CacheRoot:        a.cacheDir,
		PackageRootCount: len(packageRoots),
		EncoderCount:     encoderCount,
		StatusCounts:     make([]cachePackageStatusSummary, 0, len(statusRows)),
		PackageSummaries: make([]cachePackageProfileSummary, 0, len(packageRows)),
		ChannelSummaries: make([]cacheChannelPackageSummary, 0, len(channelRows)),
		ChannelNeeds:     make([]cacheChannelPackageNeed, 0, len(needRows)),
	}
	if resp.CacheRoot != "" {
		resp.PackageRoot = filepath.Join(resp.CacheRoot, "packages")
		if n, err := dirSize(resp.CacheRoot); err == nil {
			resp.CacheRootBytes = &n
		} else {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("cache root size unavailable: %v", err))
		}
		if n, err := dirSize(resp.PackageRoot); err == nil {
			resp.PackageRootBytes = &n
		} else {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("package root size unavailable: %v", err))
		}
	}

	rootBytes := map[string]int64{}
	var packageBytes int64
	for _, root := range packageRoots {
		n, err := dirSize(root)
		if err != nil {
			continue
		}
		rootBytes[root] = n
		packageBytes += n
	}
	resp.PackageBytes = &packageBytes
	if resp.PackageRootCount == 0 && resp.PackageRootBytes != nil && *resp.PackageRootBytes > 0 {
		resp.Warnings = append(resp.Warnings, "0 packaged media segments tracked in DB but encoded cache files remain on disk — likely orphaned from a prior database; manual cleanup may be needed")
	}

	packageProfileBytes := map[string]int64{}
	for _, row := range packageRootRows {
		key := packageSummaryKey("", row.RenditionProfile, row.Status)
		packageProfileBytes[key] += rootBytes[row.PackageRoot]
	}

	channelBytes := map[string]int64{}
	for _, row := range channelRoots {
		key := packageSummaryKey(row.ChannelID, row.RenditionProfile, row.Status)
		channelBytes[key] += rootBytes[row.PackageRoot]
	}

	for _, row := range statusRows {
		resp.StatusCounts = append(resp.StatusCounts, cachePackageStatusSummary{
			Status: row.Status,
			Count:  row.Count,
		})
	}
	for _, row := range packageRows {
		resp.PackageSummaries = append(resp.PackageSummaries, cachePackageProfileSummary{
			RenditionProfile: row.RenditionProfile,
			Status:           row.Status,
			PackageCount:     row.PackageCount,
			PackageBytes:     packageProfileBytes[packageSummaryKey("", row.RenditionProfile, row.Status)],
			ReadyDurationMs:  row.ReadyDurationMs,
			OldestUpdatedMs:  row.OldestUpdatedMs,
			NewestUpdatedMs:  row.NewestUpdatedMs,
			Invalid:          !knownProfileSet[row.RenditionProfile],
			Disabled:         disabledProfileSet[row.RenditionProfile],
		})
	}
	for _, row := range channelRows {
		resp.ChannelSummaries = append(resp.ChannelSummaries, cacheChannelPackageSummary{
			ChannelID:        row.ChannelID,
			DisplayName:      row.DisplayName,
			RenditionProfile: row.RenditionProfile,
			Status:           row.Status,
			PackageCount:     row.PackageCount,
			PackageBytes:     channelBytes[packageSummaryKey(row.ChannelID, row.RenditionProfile, row.Status)],
			ReadyDurationMs:  row.ReadyDurationMs,
			OldestUpdatedMs:  row.OldestUpdatedMs,
			NewestUpdatedMs:  row.NewestUpdatedMs,
		})
	}
	for _, row := range needRows {
		remaining := row.NeededCount - row.ReadyCount
		if remaining < 0 {
			remaining = 0
		}
		resp.ChannelNeeds = append(resp.ChannelNeeds, cacheChannelPackageNeed{
			ChannelID:        row.ChannelID,
			DisplayName:      row.DisplayName,
			RenditionProfile: row.RenditionProfile,
			NeededCount:      row.NeededCount,
			ReadyCount:       row.ReadyCount,
			ProcessingCount:  row.ProcessingCount,
			PendingCount:     row.PendingCount,
			FailedCount:      row.FailedCount,
			MissingCount:     row.MissingCount,
			RemainingCount:   remaining,
		})
	}
	writeJSON(w, resp)
}

func (a *App) handleCacheInvalidProfilesCleanup(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry-run") != "false"

	packages, err := db.InvalidProfilePackages(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	globalPkgRoot := ""
	if a.cacheDir != "" {
		globalPkgRoot = filepath.Clean(filepath.Join(a.cacheDir, "packages"))
	}

	resp := invalidProfileCleanupResponse{
		DryRun:  dryRun,
		Removed: make([]invalidProfileCleanupItem, 0, len(packages)),
	}

	for _, pkg := range packages {
		item := invalidProfileCleanupItem{
			ID:      pkg.ID,
			MediaID: pkg.MediaID,
			Profile: pkg.RenditionProfile,
			Status:  pkg.Status,
		}

		if pkg.PackageRoot != "" {
			item.PackageRoot = pkg.PackageRoot
			if n, err := dirSize(pkg.PackageRoot); err == nil {
				item.Bytes = n
				resp.TotalBytes += n
			}
		}

		if !dryRun {
			if item.PackageRoot != "" && globalPkgRoot != "" {
				cleaned := filepath.Clean(item.PackageRoot)
				if strings.HasPrefix(cleaned, globalPkgRoot+string(os.PathSeparator)) {
					if err := os.RemoveAll(cleaned); err != nil {
						writeError(w, http.StatusInternalServerError, "fs_error", fmt.Sprintf("remove %s: %v", cleaned, err))
						return
					}
				} else {
					item.DiskSkipped = true
				}
			} else if item.PackageRoot != "" {
				item.DiskSkipped = true
			}

			if err := db.DeleteMediaPackage(r.Context(), a.dbConn, pkg.ID); err != nil {
				writeError(w, http.StatusInternalServerError, "db_error", err.Error())
				return
			}
		}

		resp.Removed = append(resp.Removed, item)
	}

	writeJSON(w, resp)
}

func (a *App) handleCacheUnreferenced(w http.ResponseWriter, r *http.Request) {
	pkgs, err := db.UnreferencedPackages(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	resp := unreferencedPackagesResponse{
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
		Count:       len(pkgs),
		Packages:    make([]unreferencedPackageItem, 0, len(pkgs)),
	}

	var globalRoot string
	if a.cacheDir != "" {
		globalRoot = filepath.Clean(filepath.Join(a.cacheDir, "packages"))
	}

	for _, p := range pkgs {
		item := unreferencedPackageItem{
			ID:               p.ID,
			MediaID:          p.MediaID,
			RenditionProfile: p.RenditionProfile,
			Status:           p.Status,
		}
		if p.PackagedDurationMs != nil {
			item.PackagedDurationMs = *p.PackagedDurationMs
		}
		if p.PackageRoot != nil && *p.PackageRoot != "" {
			cleaned := filepath.Clean(*p.PackageRoot)
			item.PackageRoot = cleaned
			if globalRoot != "" && strings.HasPrefix(cleaned, globalRoot+string(os.PathSeparator)) {
				if n, err := dirSize(cleaned); err == nil {
					item.Bytes = n
					resp.TotalBytes += n
				}
			}
		}
		resp.Packages = append(resp.Packages, item)
	}

	writeJSON(w, resp)
}
