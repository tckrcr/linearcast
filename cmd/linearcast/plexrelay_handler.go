package main

import (
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/plexrelay"
)

// handlePlexRelayEntry is the entry point for a plex_relay channel.
// It creates a viewer Plex transcode session and redirects to the
// per-viewer proxied manifest.
func (a *app) handlePlexRelayEntry(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.channel(channelID)
	if rt == nil {
		http.NotFound(w, r)
		return
	}
	if rt.PlaybackMode != db.PlaybackModePlexRelay {
		http.NotFound(w, r)
		return
	}

	nowMs := time.Now().UTC().UnixMilli()
	entries, err := db.ScheduleWindow(r.Context(), a.dbConn, channelID, nowMs, nowMs+lookaheadMs)
	if err != nil {
		http.Error(w, "schedule lookup failed", http.StatusInternalServerError)
		return
	}
	if len(entries) == 0 {
		http.NotFound(w, r)
		return
	}
	currentEntry := db.FindScheduleEntry(entries, nowMs)
	if currentEntry == nil {
		currentEntry = &entries[0]
	}

	media, err := db.MediaByID(r.Context(), a.dbConn, currentEntry.MediaID)
	if err != nil {
		http.Error(w, "media lookup failed", http.StatusInternalServerError)
		return
	}
	if media == nil {
		http.NotFound(w, r)
		return
	}

	ratingKey := plexRatingKeyFromSourceRef(media.SourceRef)
	if ratingKey == "" {
		log.Printf("plexrelay channel=%s media=%s has no plex source_ref", channelID, media.ID)
		http.Error(w, "media not sourced from Plex", http.StatusBadGateway)
		return
	}

	offsetMs := nowMs - currentEntry.StartMs
	if offsetMs < 0 {
		offsetMs = 0
	}
	if offsetMs > currentEntry.DurationMs {
		offsetMs = currentEntry.DurationMs
	}

	if a.plexRelay == nil {
		http.Error(w, "plex relay not configured", http.StatusBadGateway)
		return
	}
	sess, err := a.plexRelay.CreateSession(r.Context(), "/library/metadata/"+ratingKey, offsetMs)
	if err != nil {
		log.Printf("plexrelay create session channel=%s media=%s ratingKey=%s: %v", channelID, media.ID, ratingKey, err)
		http.Error(w, "failed to create plex transcode session", http.StatusBadGateway)
		return
	}

	http.Redirect(w, r, sess.ProxyPathPrefix(channelID)+"/stream.m3u8", http.StatusFound)
}

// handlePlexRelayProxy handles any proxied plexrelay request:
//   - {viewerToken}/stream.m3u8                    — proxied master manifest
//   - {viewerToken}/session/{id}/base/index.m3u8   — proxied media playlist
//   - {viewerToken}/session/{id}/base/{segment}.ts — proxied segment
func (a *app) handlePlexRelayProxy(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	viewerToken := r.PathValue("viewerToken")
	restPath := r.PathValue("path")

	if a.plexRelay == nil {
		http.Error(w, "plex relay not configured", http.StatusBadGateway)
		return
	}
	sess := a.plexRelay.Lookup(viewerToken)
	if sess == nil {
		http.NotFound(w, r)
		return
	}

	if strings.HasSuffix(restPath, ".m3u8") {
		a.proxyPlexManifest(w, r, sess, channelID, restPath)
	} else {
		a.proxyPlexSegment(w, r, sess, restPath)
	}
}

func (a *app) proxyPlexManifest(w http.ResponseWriter, r *http.Request, sess *plexrelay.Session, channelID, restPath string) {
	client := a.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	body := sess.MasterManifest(channelID)
	if restPath != "stream.m3u8" {
		var err error
		body, err = sess.FetchManifest(r.Context(), client, restPath, channelID)
		if err != nil {
			log.Printf("plexrelay fetch manifest channel=%s viewer=%s err=%v", channelID, sess.ViewerToken, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (a *app) proxyPlexSegment(w http.ResponseWriter, r *http.Request, sess *plexrelay.Session, restPath string) {
	segmentURL := sess.ProxySegmentURL(restPath)
	client := a.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := sess.FetchSegment(r.Context(), client, segmentURL)
	if err != nil {
		log.Printf("plexrelay fetch segment viewer=%s path=%s err=%v", sess.ViewerToken, restPath, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		if strings.HasSuffix(restPath, ".ts") {
			contentType = "video/mp2t"
		} else {
			contentType = "application/octet-stream"
		}
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// plexRatingKeyFromSourceRef extracts the Plex rating key from a source_ref
// string like "plex://{ratingKey}". Returns empty string if not a plex ref.
func plexRatingKeyFromSourceRef(ref string) string {
	const prefix = "plex://"
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	return strings.TrimSpace(ref[len(prefix):])
}
