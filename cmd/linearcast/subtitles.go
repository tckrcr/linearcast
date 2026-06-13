package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// handleSubtitlePlaylist serves a live HLS subtitle media playlist for one
// language tag. Each schedule entry in the current window becomes one subtitle
// segment whose URI points to the media's WebVTT sidecar.
//
// #EXT-X-PROGRAM-DATE-TIME tags anchor every segment to wall-clock time so
// that hls.js can synchronise subtitle cues with the video playlist, which
// also carries PDT tags.
func (a *app) handleSubtitlePlaylist(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	language := r.PathValue("language")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if rt.PlaybackMode != db.PlaybackModePackaged {
		http.NotFound(w, r)
		return
	}
	profile := a.packagedProfile
	if rt.RequiredPackageProfile != "" {
		profile = rt.RequiredPackageProfile
	}

	nowMs := time.Now().UTC().UnixMilli()
	entries, err := db.ScheduleWindow(r.Context(), a.dbConn, channelID, nowMs, nowMs+manifestAheadMs)
	if err != nil || len(entries) == 0 {
		http.NotFound(w, r)
		return
	}

	// #EXT-X-MEDIA-SEQUENCE = count of schedule entries fully before nowMs.
	var seqBase int64
	_ = a.dbConn.QueryRow(`
		SELECT COALESCE(COUNT(*),0)
		FROM schedule_entries
		WHERE channel_id = ?
		  AND start_ms + duration_ms <= ?`,
		channelID, nowMs).Scan(&seqBase)

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString("#EXT-X-TARGETDURATION:7200\n")
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", seqBase)

	for _, entry := range entries {
		pkg, err := db.ReadyMediaPackage(r.Context(), a.dbConn, entry.MediaID, profile)
		if err != nil || pkg == nil {
			break
		}

		// Anchor PDT at (StartMs - OffsetMs) so VTT timestamp 0 aligns with
		// the start of the source file. At wall time StartMs the player seeks
		// to VTT time OffsetMs, which is where this segment actually begins.
		pdt := time.UnixMilli(entry.StartMs - entry.OffsetMs).UTC().Format(pdtLayout)
		durSec := float64(entry.DurationMs) / 1000.0

		tracks, _ := db.MediaTracksByMediaID(r.Context(), a.dbConn, entry.MediaID)
		hasTrack := false
		for _, t := range tracks {
			if t.Kind == "subtitle" && t.Language == language && t.Path != nil {
				hasTrack = true
				break
			}
		}

		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", durSec)
		if hasTrack {
			fmt.Fprintf(&b, "/channel/%s/%s/subs/%s/%s.vtt\n", channelID, packagedPath, pkg.ID, language)
		} else {
			// No subtitle for this entry — serve empty WebVTT to maintain timing.
			fmt.Fprintf(&b, "/channel/%s/%s/subs/empty.vtt\n", channelID, packagedPath)
		}
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

// handleSubtitleVTT serves the WebVTT sidecar for a specific package and
// language. The file was extracted from the source media at packaging time.
func (a *app) handleSubtitleVTT(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name") // e.g. "en.vtt"
	if !strings.HasSuffix(name, ".vtt") {
		http.NotFound(w, r)
		return
	}
	language := strings.TrimSuffix(name, ".vtt")

	pkg, err := db.MediaPackageByID(r.Context(), a.dbConn, r.PathValue("packageID"))
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if pkg == nil || pkg.Status != db.PackageStatusReady {
		http.NotFound(w, r)
		return
	}

	tracks, err := db.MediaTracksByMediaID(r.Context(), a.dbConn, pkg.MediaID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	var vttPath string
	for _, t := range tracks {
		if t.Kind == "subtitle" && t.Language == language && t.Path != nil {
			vttPath = *t.Path
			break
		}
	}
	if vttPath == "" || missingPackagedFile(vttPath) {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/vtt")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, vttPath)
}

// handleEmptySubtitle returns a valid but empty WebVTT document. Used as a
// placeholder for schedule entries that have no subtitle track in the
// requested language, preserving timing continuity in the subtitle playlist.
func (a *app) handleEmptySubtitle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/vtt")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte("WEBVTT\n\n"))
}
