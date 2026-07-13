package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/subtitlepolicy"
)

var loggedOnDemandSubtitleDecisions sync.Map

type burnSubtitleTrackResponse struct {
	Language    string `json:"language"`
	Mode        string `json:"mode"`
	StreamIndex int    `json:"streamIndex"`
	Codec       string `json:"codec,omitempty"`
	Forced      bool   `json:"forced"`
	Label       string `json:"label"`
}

func (a *app) burnSubtitleStreamIndexForMedia(ctx context.Context, channelID, mediaID string, profile packageprofile.Profile) int {
	mgr := a.encodingManagerForChannel(channelID)
	if mgr == nil {
		return -1
	}
	tracks, err := a.bitmapSubtitleTracksForMedia(ctx, channelID, mediaID)
	if err != nil {
		log.Printf("WARN burn subtitle track lookup channel=%s media=%s: %v", channelID, mediaID, err)
		return -1
	}
	if lang := mgr.BurnSubtitleLanguage(channelID); lang != "" {
		for _, t := range tracks {
			if strings.EqualFold(t.Language, lang) {
				logOnDemandSubtitleDecision(channelID, mediaID, "burn", "manual_language", t.StreamIndex, strings.ToLower(t.Language))
				return t.StreamIndex
			}
		}
		return -1
	}
	decision := subtitlepolicy.Resolve(subtitlepolicy.Request{
		Mode:     subtitlepolicy.Mode(profile.Subtitles.Mode),
		Language: profile.Subtitles.Language,
	}, profile, tracks)
	if decision.Action != subtitlepolicy.ActionBurn {
		return -1
	}
	logOnDemandSubtitleDecision(channelID, mediaID, string(decision.Action), string(decision.Reason), decision.StreamIndex, decision.Language)
	return decision.StreamIndex
}

func logOnDemandSubtitleDecision(channelID, mediaID, action, reason string, streamIndex int, language string) {
	key := fmt.Sprintf("%s|%s|%s|%s|%d|%s", channelID, mediaID, action, reason, streamIndex, language)
	if _, loaded := loggedOnDemandSubtitleDecisions.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("INFO on-demand subtitle decision channel_id=%s media_id=%s action=%s reason=%s stream_index=%d language=%s",
		channelID, mediaID, action, reason, streamIndex, language)
}

func (a *app) bitmapSubtitleTracksForMedia(ctx context.Context, channelID, mediaID string) ([]db.PackageTrack, error) {
	media, err := db.MediaByID(ctx, a.dbConn, mediaID)
	if err != nil {
		return nil, err
	}
	if media == nil {
		return nil, nil
	}
	infos := a.subtitleStreams(ctx, media.ID, media.Path)
	tracks := make([]db.PackageTrack, 0, len(infos))
	for _, si := range infos {
		if !si.IsBitmap {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(si.Language))
		if lang == "" {
			lang = "und"
		}
		tracks = append(tracks, db.PackageTrack{
			Kind:        "subtitle",
			StreamIndex: si.Index,
			Language:    lang,
			Title:       si.Title,
			Codec:       strings.ToLower(si.Codec),
			Source:      db.TrackSourceEmbeddedBitmap,
			Forced:      si.Forced,
		})
	}
	return tracks, nil
}

type burnSubtitleListResponse struct {
	ActiveLanguage string                      `json:"activeLanguage,omitempty"`
	Mode           string                      `json:"mode,omitempty"`
	Unavailable    string                      `json:"unavailable,omitempty"`
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
	if _, unavailable := a.burnSubtitleProfile(r, rt); unavailable != "" {
		writeBurnSubtitleJSON(w, burnSubtitleListResponse{Mode: "burn", Unavailable: unavailable, Tracks: []burnSubtitleTrackResponse{}})
		return
	}
	tracks, err := a.burnSubtitleTracksForWindow(r, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	active := ""
	if mgr := a.encodingManagerForChannel(channelID); mgr != nil {
		active = mgr.BurnSubtitleLanguage(channelID)
	}
	writeBurnSubtitleJSON(w, burnSubtitleListResponse{ActiveLanguage: active, Mode: "burn", Tracks: tracks})
}

func (a *app) handleBurnSubtitleSet(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if _, unavailable := a.burnSubtitleProfile(r, rt); unavailable != "" {
		http.Error(w, unavailable, http.StatusBadRequest)
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
	if mgr := a.encodingManagerForChannel(channelID); mgr != nil {
		mgr.SetBurnSubtitleLanguage(channelID, lang)
	}
	writeBurnSubtitleJSON(w, burnSubtitleListResponse{ActiveLanguage: lang, Mode: "burn"})
}

func (a *app) burnSubtitleProfile(r *http.Request, rt *channelRuntime) (*packageprofile.Profile, string) {
	if rt.PlaybackMode != db.PlaybackModePackaged {
		return nil, "burn-in subtitles require packaged playback"
	}
	if rt.PrefillMode == "eager" {
		return nil, "burn-in subtitles require an on-demand channel"
	}
	p, err := db.GetPackageProfile(r.Context(), a.dbConn, rt.RequiredPackageProfile)
	if err != nil || p == nil {
		return nil, "burn-in subtitles require a valid package profile"
	}
	if p.Video.Mode != packageprofile.VideoModeTranscode {
		return nil, "burn-in subtitles require a transcode video profile"
	}
	return p, ""
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
		tracks, err := a.bitmapSubtitleTracksForMedia(r.Context(), channelID, entry.MediaID)
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
				Mode:        "burn",
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
