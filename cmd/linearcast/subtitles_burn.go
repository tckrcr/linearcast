package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/packager"
)

type burnSubtitleTrackResponse struct {
	Language    string `json:"language"`
	StreamIndex int    `json:"streamIndex"`
	Codec       string `json:"codec,omitempty"`
	Forced      bool   `json:"forced"`
	Label       string `json:"label"`
}

func (a *app) burnSubtitleStreamIndexForMedia(ctx context.Context, channelID, mediaID string) int {
	if a.sessions == nil {
		return -1
	}
	lang := a.sessions.BurnSubtitleLanguage(channelID)
	if lang == "" {
		return -1
	}
	if media, err := db.MediaByID(ctx, a.dbConn, mediaID); err == nil && media != nil {
		if err := packager.BackfillSubtitleTracks(ctx, a.dbConn, media.ID, media.Path, "", nil); err != nil {
			log.Printf("WARN burn subtitle admission inventory channel=%s media=%s: %v", channelID, mediaID, err)
		}
	}
	tracks, err := db.BitmapSubtitleTracksForMedia(ctx, a.dbConn, mediaID)
	if err != nil {
		log.Printf("WARN burn subtitle track lookup channel=%s media=%s: %v", channelID, mediaID, err)
		return -1
	}
	for _, t := range tracks {
		if strings.EqualFold(t.Language, lang) {
			return t.StreamIndex
		}
	}
	return -1
}

type burnSubtitleListResponse struct {
	ActiveLanguage string                      `json:"activeLanguage,omitempty"`
	Tracks         []burnSubtitleTrackResponse `json:"tracks"`
}

type burnSubtitleSetRequest struct {
	Language string `json:"language"`
}

func (a *app) handleBurnSubtitleList(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if _, ok := a.burnSubtitleProfile(r, rt); !ok {
		writeBurnSubtitleJSON(w, burnSubtitleListResponse{})
		return
	}
	tracks, err := a.burnSubtitleTracksForWindow(r, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	active := ""
	if a.sessions != nil {
		active = a.sessions.BurnSubtitleLanguage(channelID)
	}
	writeBurnSubtitleJSON(w, burnSubtitleListResponse{ActiveLanguage: active, Tracks: tracks})
}

func (a *app) handleBurnSubtitleSet(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if _, ok := a.burnSubtitleProfile(r, rt); !ok {
		http.Error(w, "burn-in subtitles require an on-demand transcode channel", http.StatusBadRequest)
		return
	}
	var req burnSubtitleSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	lang := strings.ToLower(strings.TrimSpace(req.Language))
	if lang != "" {
		tracks, err := a.burnSubtitleTracksForWindow(r, channelID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		found := false
		for _, t := range tracks {
			if t.Language == lang {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "subtitle language is not burnable for this channel window", http.StatusBadRequest)
			return
		}
	}
	if a.sessions != nil {
		a.sessions.SetBurnSubtitleLanguage(channelID, lang)
	}
	writeBurnSubtitleJSON(w, burnSubtitleListResponse{ActiveLanguage: lang})
}

func (a *app) burnSubtitleProfile(r *http.Request, rt *channelRuntime) (*packageprofile.Profile, bool) {
	if rt.PrefillMode != "on_demand" || rt.PlaybackMode != db.PlaybackModePackaged {
		return nil, false
	}
	p, err := db.GetPackageProfile(r.Context(), a.dbConn, rt.RequiredPackageProfile)
	if err != nil || p == nil {
		return nil, false
	}
	return p, p.Video.Mode == packageprofile.VideoModeTranscode
}

func (a *app) burnSubtitleTracksForWindow(r *http.Request, channelID string) ([]burnSubtitleTrackResponse, error) {
	nowMs := time.Now().UTC().UnixMilli()
	entries, err := db.ScheduleWindow(r.Context(), a.dbConn, channelID, nowMs, nowMs+manifestAheadMs)
	if err != nil {
		return nil, fmt.Errorf("schedule window: %w", err)
	}
	seen := map[string]bool{}
	var out []burnSubtitleTrackResponse
	for _, entry := range entries {
		media, err := db.MediaByID(r.Context(), a.dbConn, entry.MediaID)
		if err != nil {
			return nil, fmt.Errorf("media %s: %w", entry.MediaID, err)
		}
		if media == nil {
			continue
		}
		if err := packager.BackfillSubtitleTracks(r.Context(), a.dbConn, media.ID, media.Path, "", nil); err != nil {
			log.Printf("WARN burn subtitle inventory media=%s path=%s: %v", media.ID, media.Path, err)
		}
		tracks, err := db.BitmapSubtitleTracksForMedia(r.Context(), a.dbConn, media.ID)
		if err != nil {
			return nil, err
		}
		for _, t := range tracks {
			lang := strings.ToLower(t.Language)
			if lang == "" || lang == "und" || seen[lang] {
				continue
			}
			seen[lang] = true
			out = append(out, burnSubtitleTrackResponse{
				Language:    lang,
				StreamIndex: t.StreamIndex,
				Codec:       t.Codec,
				Forced:      t.Forced,
				Label:       burnSubtitleLabel(lang, t.Forced),
			})
		}
	}
	return out, nil
}

func burnSubtitleLabel(language string, forced bool) string {
	if forced {
		return language + " burned-in forced"
	}
	return language + " burned-in"
}

func writeBurnSubtitleJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(v)
}
