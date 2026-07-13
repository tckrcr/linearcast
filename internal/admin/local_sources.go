package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/lcingest"
)

type localSourceRequest struct {
	Name      string   `json:"name"`
	MediaKind string   `json:"mediaKind"`
	Paths     []string `json:"paths"`
}

func (a *App) handleLocalSourcesList(w http.ResponseWriter, r *http.Request) {
	sources, err := db.ListLocalMediaSources(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{"sources": sources})
}

func (a *App) handleLocalSourceCreate(w http.ResponseWriter, r *http.Request) {
	source, ok := a.decodeLocalSourceRequest(w, r, "")
	if !ok {
		return
	}
	saved, err := db.UpsertLocalMediaSource(r.Context(), a.dbConn, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, saved)
}

func (a *App) handleLocalSourceUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	source, ok := a.decodeLocalSourceRequest(w, r, id)
	if !ok {
		return
	}
	if existing, err := db.GetLocalMediaSource(r.Context(), a.dbConn, id); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	} else if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "local media source not found")
		return
	}
	saved, err := db.UpsertLocalMediaSource(r.Context(), a.dbConn, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, saved)
}

func (a *App) handleLocalSourceDelete(w http.ResponseWriter, r *http.Request) {
	deleted, err := db.DeleteLocalMediaSource(r.Context(), a.dbConn, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "local media source not found")
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

func (a *App) handleLocalSourceScan(w http.ResponseWriter, r *http.Request) {
	source, err := db.GetLocalMediaSource(r.Context(), a.dbConn, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if source == nil {
		writeError(w, http.StatusNotFound, "not_found", "local media source not found")
		return
	}
	paths, ok := a.validateLocalSourcePaths(w, source.Paths, true)
	if !ok {
		return
	}

	jobID, job := a.ingestJobs.create()
	go a.runLocalSourcesScan(job, []localScanGroup{{mediaKind: source.MediaKind, paths: paths}})

	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

// handleLocalSourcesScanAll scans every configured local media source in a
// single job. Unlike a per-source scan it is lenient about paths: missing or
// out-of-root directories are skipped rather than failing the request, so one
// stale source can't block scanning the rest.
func (a *App) handleLocalSourcesScanAll(w http.ResponseWriter, r *http.Request) {
	sources, err := db.ListLocalMediaSources(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	var groups []localScanGroup
	for _, source := range sources {
		paths := a.existingScanPaths(source.Paths)
		if len(paths) == 0 {
			continue
		}
		groups = append(groups, localScanGroup{mediaKind: source.MediaKind, paths: paths})
	}
	if len(groups) == 0 {
		writeError(w, http.StatusBadRequest, "no_sources", "no local media sources with readable paths to scan")
		return
	}

	jobID, job := a.ingestJobs.create()
	go a.runLocalSourcesScan(job, groups)

	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

// existingScanPaths returns the cleaned, de-duplicated subset of rawPaths that
// are readable directories under the media root. Invalid entries are dropped
// silently — used by scan-all where partial coverage beats failing outright.
func (a *App) existingScanPaths(rawPaths []string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, raw := range rawPaths {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		clean := filepath.Clean(raw)
		if a.mediaRoot != "" && !isUnderRoot(clean, filepath.Clean(a.mediaRoot)) {
			continue
		}
		info, err := os.Stat(clean)
		if err != nil || !info.IsDir() {
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		paths = append(paths, clean)
	}
	return paths
}

func (a *App) decodeLocalSourceRequest(w http.ResponseWriter, r *http.Request, id string) (db.LocalMediaSource, bool) {
	defer r.Body.Close()
	var req localSourceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return db.LocalMediaSource{}, false
	}
	kind := normalizeLocalMediaKind(req.MediaKind)
	if kind == "" {
		writeError(w, http.StatusBadRequest, "invalid_media_kind", "mediaKind must be movies, shows, music, or filler")
		return db.LocalMediaSource{}, false
	}
	paths, ok := a.validateLocalSourcePaths(w, req.Paths, false)
	if !ok {
		return db.LocalMediaSource{}, false
	}
	source := db.LocalMediaSource{
		ID:        id,
		Name:      strings.TrimSpace(req.Name),
		MediaKind: kind,
		Paths:     paths,
	}
	if source.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "name is required")
		return db.LocalMediaSource{}, false
	}
	return source, true
}

func (a *App) validateLocalSourcePaths(w http.ResponseWriter, rawPaths []string, requireExists bool) ([]string, bool) {
	paths := make([]string, 0, len(rawPaths))
	seen := map[string]bool{}
	for _, raw := range rawPaths {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		clean := filepath.Clean(raw)
		if a.mediaRoot != "" && !isUnderRoot(clean, filepath.Clean(a.mediaRoot)) {
			writeError(w, http.StatusBadRequest, "invalid_path", "path must be under the media root")
			return nil, false
		}
		if requireExists {
			info, err := os.Stat(clean)
			if err != nil {
				writeError(w, http.StatusBadRequest, "path_not_found", err.Error())
				return nil, false
			}
			if !info.IsDir() {
				writeError(w, http.StatusBadRequest, "not_a_directory", "path must be a directory")
				return nil, false
			}
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		paths = append(paths, clean)
	}
	if len(paths) == 0 {
		writeError(w, http.StatusBadRequest, "missing_paths", "at least one path is required")
		return nil, false
	}
	return paths, true
}

func normalizeLocalMediaKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "movies", "movie":
		return "movies"
	case "shows", "show", "tv":
		return "shows"
	case "music", "audio":
		return "music"
	case "filler":
		return "filler"
	default:
		return ""
	}
}

// localScanGroup pairs a media kind with the paths to scan under it, so one
// ingest job can cover several sources (each with its own kind) at once.
type localScanGroup struct {
	mediaKind string
	paths     []string
}

func (a *App) runLocalSourcesScan(job *ingestJob, groups []localScanGroup) {
	ctx := job.ctx

	var total int
	for _, g := range groups {
		for _, path := range g.paths {
			var n int
			var err error
			if g.mediaKind == "music" {
				n, err = lcingest.CountMusicFiles(path)
			} else {
				n, err = lcingest.CountMediaFiles(path)
			}
			if err != nil {
				job.Printf("error counting files in %s: %v", path, err)
				continue
			}
			total += n
		}
	}
	job.setTotal(total)

	acc := lcingest.Result{}
	cancelled := false
	for _, g := range groups {
		if cancelled {
			break
		}
		for _, path := range g.paths {
			if ctx.Err() != nil {
				cancelled = true
				break
			}
			job.Printf("scanning local source path=%s kind=%s", path, g.mediaKind)
			var (
				res lcingest.Result
				err error
			)
			if g.mediaKind == "music" {
				res, err = lcingest.IngestMusic(ctx, a.dbConn, path, job)
			} else {
				res, err = lcingest.Ingest(ctx, a.dbConn, path, job)
			}
			acc.Add(res)
			if err != nil {
				if ctx.Err() != nil {
					cancelled = true
					break
				}
				job.Printf("error scanning %s: %v", path, err)
				acc.Add(lcingest.Result{
					Failed:         1,
					FailureReasons: map[string]int{err.Error(): 1},
				})
				continue
			}
			if g.mediaKind == "filler" {
				nowMs := a.now().UTC().UnixMilli()
				if err := db.RegisterFillerAssetsFromDirectory(ctx, a.dbConn, path, nowMs); err != nil {
					job.Printf("error registering filler assets for %s: %v", path, err)
				}
			}
		}
	}
	switch {
	case cancelled:
		job.finalize("cancelled", "scan cancelled", &acc)
	case acc.Failed > 0:
		job.finalize("failed", "one or more local media paths failed to scan", &acc)
	default:
		job.finalize("done", "", &acc)
	}
}
