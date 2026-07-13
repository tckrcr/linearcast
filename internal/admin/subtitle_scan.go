package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
)

// ---------------------------------------------------------------------------
// Scan job
// ---------------------------------------------------------------------------

type ScannedTrack struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
	IsBitmap bool   `json:"isBitmap"`
}

type ScannedEpisode struct {
	MediaID        string         `json:"mediaId"`
	Filename       string         `json:"filename"`
	Packaged       bool           `json:"packaged"`
	Tracks         []ScannedTrack `json:"tracks"`
	ExtractedLangs []string       `json:"extractedLangs"`
}

type ScannedSeason struct {
	Name     string           `json:"name"`
	Episodes []ScannedEpisode `json:"episodes"`
}

type ScannedShow struct {
	Name    string          `json:"name"`
	Seasons []ScannedSeason `json:"seasons"`
}

type subtitleScanJob struct {
	mu      sync.RWMutex
	status  string // "running" | "done" | "error"
	scanned int
	total   int
	shows   []ScannedShow
	errMsg  string
}

func (j *subtitleScanJob) snapshot() subtitleScanStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return subtitleScanStatus{
		Status:  j.status,
		Scanned: j.scanned,
		Total:   j.total,
		Shows:   j.shows,
		Error:   j.errMsg,
	}
}

type subtitleScanStatus struct {
	Status  string        `json:"status"`
	Scanned int           `json:"scanned"`
	Total   int           `json:"total"`
	Shows   []ScannedShow `json:"shows,omitempty"`
	Error   string        `json:"error,omitempty"`
}

func (a *App) handleSubtitleScanGet(w http.ResponseWriter, _ *http.Request) {
	a.mu().RLock()
	job := a.subtitleScan
	a.mu().RUnlock()

	if job == nil {
		if cached, ok := a.loadScanCache(); ok {
			writeJSON(w, cached)
			return
		}
		writeJSON(w, subtitleScanStatus{Status: "idle"})
		return
	}
	writeJSON(w, job.snapshot())
}

func (a *App) loadScanCache() (subtitleScanStatus, bool) {
	_, status, showsJSON, err := db.LoadSubtitleScanCache(context.Background(), a.dbConn)
	if err != nil || showsJSON == nil {
		return subtitleScanStatus{}, false
	}
	var shows []ScannedShow
	if err := json.Unmarshal(showsJSON, &shows); err != nil {
		return subtitleScanStatus{}, false
	}
	return subtitleScanStatus{Status: status, Shows: shows}, true
}

func (a *App) handleSubtitleScanStart(w http.ResponseWriter, r *http.Request) {
	a.mu().Lock()
	if a.subtitleScan != nil && a.subtitleScan.snapshot().Status == "running" {
		a.mu().Unlock()
		writeError(w, http.StatusConflict, "already_running", "a scan is already in progress")
		return
	}
	job := &subtitleScanJob{status: "running"}
	a.subtitleScan = job
	a.mu().Unlock()

	go a.runSubtitleScan(job)
	writeJSON(w, subtitleScanStatus{Status: "running"})
}

func (a *App) runSubtitleScan(job *subtitleScanJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	packagedIDs, err := db.ReadyPackagedMediaIDs(context.Background(), a.dbConn)
	if err != nil {
		job.mu.Lock()
		job.status = "error"
		job.errMsg = err.Error()
		job.mu.Unlock()
		return
	}

	// Fetch only the media rows that have a ready package.
	var allMedia []db.Media
	for id := range packagedIDs {
		m, err := db.MediaByID(context.Background(), a.dbConn, id)
		if err != nil || m == nil {
			continue
		}
		allMedia = append(allMedia, *m)
	}

	job.mu.Lock()
	job.total = len(allMedia)
	job.mu.Unlock()

	// showKey → seasonKey → episodes
	type showKey = string
	type seasonKey = string
	showOrder := []showKey{}
	showSeen := map[showKey]bool{}
	seasonOrder := map[showKey][]seasonKey{}
	seasonSeen := map[showKey]map[seasonKey]bool{}
	episodes := map[showKey]map[seasonKey][]ScannedEpisode{}

	for _, m := range allMedia {
		if ctx.Err() != nil {
			break
		}

		show, season := showAndSeason(m.Path)

		tracks, err := packager.ProbeSubtitleStreams(ctx, m.Path)
		if err != nil {
			// File may be inaccessible; record it with no tracks.
			tracks = nil
		}

		scanned := make([]ScannedTrack, len(tracks))
		for i, t := range tracks {
			scanned[i] = ScannedTrack{
				Index:    t.Index,
				Codec:    t.Codec,
				Language: t.Language,
				Title:    t.Title,
				IsBitmap: t.IsBitmap,
			}
		}

		var extractedLangs []string
		if pkgs, err := db.MediaPackagesForMedia(context.Background(), a.dbConn, m.ID); err == nil {
			seenLang := map[string]bool{}
			for _, pkg := range pkgs {
				if pkg.Status != db.PackageStatusReady {
					continue
				}
				dbTracks, err := db.PackageTracksByPackageID(context.Background(), a.dbConn, pkg.ID)
				if err != nil {
					continue
				}
				for _, t := range dbTracks {
					if t.Kind == "subtitle" && t.Path != nil && *t.Path != "" && t.Language != "" && !seenLang[t.Language] {
						extractedLangs = append(extractedLangs, t.Language)
						seenLang[t.Language] = true
					}
				}
			}
		}

		ep := ScannedEpisode{
			MediaID:        m.ID,
			Filename:       filepath.Base(m.Path),
			Packaged:       true,
			Tracks:         scanned,
			ExtractedLangs: extractedLangs,
		}

		if !showSeen[show] {
			showSeen[show] = true
			showOrder = append(showOrder, show)
			seasonSeen[show] = map[seasonKey]bool{}
			episodes[show] = map[seasonKey][]ScannedEpisode{}
		}
		if !seasonSeen[show][season] {
			seasonSeen[show][season] = true
			seasonOrder[show] = append(seasonOrder[show], season)
			episodes[show][season] = nil
		}
		episodes[show][season] = append(episodes[show][season], ep)

		job.mu.Lock()
		job.scanned++
		job.mu.Unlock()
	}

	// Build final tree.
	var shows []ScannedShow
	for _, sh := range showOrder {
		var seasons []ScannedSeason
		for _, se := range seasonOrder[sh] {
			seasons = append(seasons, ScannedSeason{
				Name:     se,
				Episodes: episodes[sh][se],
			})
		}
		shows = append(shows, ScannedShow{Name: sh, Seasons: seasons})
	}

	job.mu.Lock()
	job.status = "done"
	job.shows = shows
	job.mu.Unlock()

	if blob, err := json.Marshal(shows); err == nil {
		_ = db.SaveSubtitleScanCache(context.Background(), a.dbConn, time.Now().UTC().UnixMilli(), "done", blob)
	}
}

// showAndSeason extracts show name and season folder from a media path.
// Handles both Show/episode.mkv and Show/Season 01/episode.mkv layouts.
func showAndSeason(path string) (show, season string) {
	dir := filepath.Dir(path)
	parent := filepath.Base(dir)
	grandparent := filepath.Base(filepath.Dir(dir))
	pl := strings.ToLower(parent)
	if strings.HasPrefix(pl, "season") || pl == "specials" || pl == "extras" {
		return grandparent, parent
	}
	return parent, ""
}

// ---------------------------------------------------------------------------
// App-level mutex (thin wrapper so we don't need a new field)
// ---------------------------------------------------------------------------

var appMu sync.RWMutex

func (a *App) mu() *sync.RWMutex {
	return &appMu
}
