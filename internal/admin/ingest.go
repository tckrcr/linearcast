// Ingest job plumbing for all media-scan endpoints (local sources, Plex,
// Jellyfin, raw /api/ingest). Each scan is an ingestJob holding a cancel
// context and an optional per-scan log file at <cacheDir>/logs/ingest_<ts>.log.
// The HTTP status endpoint returns counts, log path, and per-reason grouped
// failures — it never streams individual ffprobe lines back to the UI.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tckrcr/linearcast/internal/lcingest"
)

// ingestJobStore holds in-memory ingest jobs. Jobs are never persisted; a
// server restart clears all job history. logsDir, if non-empty, enables
// per-job file logging at logsDir/ingest_<timestamp>.log.
type ingestJobStore struct {
	mu      sync.Mutex
	jobs    map[string]*ingestJob
	logsDir string
}

func newIngestJobStore(cacheDir string) *ingestJobStore {
	s := &ingestJobStore{jobs: make(map[string]*ingestJob)}
	if cacheDir = strings.TrimSpace(cacheDir); cacheDir != "" {
		s.logsDir = filepath.Join(cacheDir, "logs")
	}
	return s
}

func (s *ingestJobStore) create() (string, *ingestJob) {
	now := time.Now()
	id := fmt.Sprintf("%d", now.UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	job := &ingestJob{
		status: "running",
		ctx:    ctx,
		cancel: cancel,
	}
	if s.logsDir != "" {
		if err := os.MkdirAll(s.logsDir, 0o755); err == nil {
			path := filepath.Join(s.logsDir, fmt.Sprintf("ingest_%s.log", now.Format("20060102-150405")))
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				job.logFile = f
				job.logPath = path
			}
		}
	}
	s.mu.Lock()
	s.jobs[id] = job
	s.mu.Unlock()
	return id, job
}

func (s *ingestJobStore) get(id string) (*ingestJob, bool) {
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	return j, ok
}

type ingestJob struct {
	mu           sync.Mutex
	status       string // "running" | "done" | "failed" | "cancelled"
	summary      *lcingest.Result
	errMsg       string
	processedCnt atomic.Int32
	totalCnt     int32

	ctx     context.Context
	cancel  context.CancelFunc
	logFile *os.File
	logPath string
}

// Printf satisfies lcingest.Logger. Writes timestamped lines to the per-job
// log file if one is open; otherwise drops the line.
func (j *ingestJob) Printf(format string, args ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.logFile == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	fmt.Fprintf(j.logFile, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}

func (j *ingestJob) OnProgress() {
	j.processedCnt.Add(1)
}

func (j *ingestJob) setTotal(n int) {
	atomic.StoreInt32(&j.totalCnt, int32(n))
}

func (j *ingestJob) progress() (processed, total int) {
	return int(j.processedCnt.Load()), int(atomic.LoadInt32(&j.totalCnt))
}

// finalize records the terminal status and closes the log file. Caller must
// hold j.mu OR call this only from the runner goroutine after the run loop
// returns; the runners follow the latter pattern.
func (j *ingestJob) finalize(status, errMsg string, summary *lcingest.Result) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = status
	j.errMsg = errMsg
	if summary != nil {
		j.summary = summary
	}
	if j.logFile != nil {
		fmt.Fprintf(j.logFile, "%s scan %s\n", time.Now().UTC().Format(time.RFC3339), status)
		_ = j.logFile.Close()
		j.logFile = nil
	}
}

// handleFSBrowse lists one level of a directory under the configured media
// root. Query param: path (absolute, must be under media root; defaults to
// root itself).
func (a *App) handleFSBrowse(w http.ResponseWriter, r *http.Request) {
	if a.mediaRoot == "" {
		writeError(w, http.StatusServiceUnavailable, "no_media_root",
			"LINEARCAST_MEDIA_ROOT is not configured")
		return
	}
	root := filepath.Clean(a.mediaRoot)

	target := root
	if raw := r.URL.Query().Get("path"); raw != "" {
		clean := filepath.Clean(raw)
		if !isUnderRoot(clean, root) {
			writeError(w, http.StatusBadRequest, "invalid_path",
				"path must be under the media root")
			return
		}
		target = clean
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_dir", err.Error())
		return
	}

	type dirEntry struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		MediaCount int    `json:"mediaCount"`
	}
	dirs := make([]dirEntry, 0)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subPath := filepath.Join(target, e.Name())
		dirs = append(dirs, dirEntry{
			Name:       e.Name(),
			Path:       subPath,
			MediaCount: countMediaShallow(subPath),
		})
	}

	writeJSON(w, map[string]any{
		"root": root,
		"path": target,
		"dirs": dirs,
	})
}

// handleIngestStart kicks off an ingest job for the given directory path and
// returns a job ID immediately. The client polls handleIngestStatus.
func (a *App) handleIngestStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "missing_path", "path is required")
		return
	}

	clean := filepath.Clean(req.Path)
	if a.mediaRoot != "" {
		if !isUnderRoot(clean, filepath.Clean(a.mediaRoot)) {
			writeError(w, http.StatusBadRequest, "invalid_path",
				"path must be under the media root")
			return
		}
	}

	info, err := os.Stat(clean)
	if err != nil {
		writeError(w, http.StatusBadRequest, "path_not_found", err.Error())
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "not_a_directory", "path must be a directory")
		return
	}

	jobID, job := a.ingestJobs.create()

	// Pre-count media files so the progress bar has a denominator from the
	// first poll. The walk is fast compared to ffprobe.
	if n, err := lcingest.CountMediaFiles(clean); err == nil {
		job.setTotal(n)
	}

	go func() {
		res, err := lcingest.Ingest(job.ctx, a.dbConn, clean, job)
		switch {
		case err != nil && job.ctx.Err() != nil:
			job.finalize("cancelled", "scan cancelled", &res)
		case err != nil:
			job.finalize("failed", err.Error(), nil)
		default:
			job.finalize("done", "", &res)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

// handleIngestStatus returns the current state of an ingest job. Failures
// are grouped by reason ("video_codec=hevc; video_height=2160" → N files) so
// a 1500-file scan doesn't dump a 263-line list into the UI.
func (a *App) handleIngestStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := a.ingestJobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "job not found")
		return
	}

	job.mu.Lock()
	resp := map[string]any{"status": job.status}
	if processed, total := job.progress(); total > 0 {
		resp["processed"] = processed
		resp["total"] = total
	}
	if job.logPath != "" {
		resp["logPath"] = job.logPath
	}
	if job.summary != nil {
		resp["summary"] = map[string]any{
			"total":            job.summary.Total,
			"passed":           job.summary.Passed,
			"failed":           job.summary.Failed,
			"failuresByReason": job.summary.FailureReasons,
		}
	}
	if job.errMsg != "" {
		resp["error"] = job.errMsg
	}
	job.mu.Unlock()

	writeJSON(w, resp)
}

// handleIngestCancel cancels the named job. Idempotent: cancelling a
// finished job is a no-op success.
func (a *App) handleIngestCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := a.ingestJobs.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	job.cancel()
	writeJSON(w, map[string]any{"ok": true})
}

// isUnderRoot reports whether path is root itself or directly under it.
// Uses a trailing-slash check to prevent /media-other matching /media.
func isUnderRoot(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// countMediaShallow counts .mkv and .mp4 files directly inside dir (non-recursive).
func countMediaShallow(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var count int
	for _, e := range entries {
		if e.Type()&fs.ModeType != 0 {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".mkv" || ext == ".mp4" {
			count++
		}
	}
	return count
}
