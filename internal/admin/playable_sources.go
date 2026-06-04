package admin

import (
	"net/http"
	"net/url"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

type playableSourcesResponse struct {
	NowMs       int64            `json:"nowMs"`
	Sources     []playableSource `json:"sources"`
	GeneratedAt string           `json:"generatedAt"`
}

type playableSource struct {
	ID                    string              `json:"id"`
	DisplayName           string              `json:"displayName"`
	ArtworkURL            string              `json:"artworkUrl,omitempty"`
	Kind                  string              `json:"kind"`
	PlaybackType          string              `json:"playbackType"`
	Status                string              `json:"status"`
	ManifestURL           string              `json:"manifestUrl"`
	Enabled               bool                `json:"enabled"`
	Current               *mediaWindow        `json:"current,omitempty"`
	Next                  *mediaWindow        `json:"next,omitempty"`
	ScheduleCoverageMs    int64               `json:"scheduleCoverageMs,omitempty"`
	ScheduleCoverageHours float64             `json:"scheduleCoverageHours,omitempty"`
	PackageCoverageMs     int64               `json:"packageCoverageMs,omitempty"`
	PackageCoverageHours  float64             `json:"packageCoverageHours,omitempty"`
	PackageProfile        string              `json:"packageProfile,omitempty"`
	NowPlaying            *externalNowPlaying `json:"nowPlaying,omitempty"`
}

func (a *App) handlePlayableSources(w http.ResponseWriter, r *http.Request) {
	nowMs := a.now().UTC().UnixMilli()
	_, cacheByChannel := a.fetchUpstreamStatus(r.Context())
	channels, err := db.EnabledGuideChannels(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	sources := make([]playableSource, 0, len(channels))
	for _, ch := range channels {
		if ch.UpstreamHLSURL != nil {
			nowPlaying, err := a.fetchExternalNowPlaying(r.Context(), ch)
			if err != nil {
				nowPlaying = nil
			}
			sources = append(sources, playableSource{
				ID:           ch.ID,
				DisplayName:  ch.DisplayName,
				ArtworkURL:   artworkForExternalChannel(ch, nowPlaying),
				Kind:         "live",
				PlaybackType: "hls",
				Status:       "live",
				ManifestURL:  externalHLSManifestURL(ch.ID),
				Enabled:      ch.Enabled,
				NowPlaying:   nowPlaying,
			})
			continue
		}
		now, err := a.channelNowForRow(r.Context(), nowMs, ch, cacheByChannel[ch.ID])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		sources = append(sources, playableSource{
			ID:                    now.ID,
			DisplayName:           now.DisplayName,
			ArtworkURL:            now.ArtworkURL,
			Kind:                  "vod",
			PlaybackType:          "hls",
			Status:                now.Status,
			ManifestURL:           hlsManifestURL(now.ID),
			Enabled:               now.Enabled,
			Current:               now.Current,
			Next:                  now.Next,
			ScheduleCoverageMs:    now.ScheduleCoverageMs,
			ScheduleCoverageHours: now.ScheduleCoverageHours,
			PackageCoverageMs:     now.PackageCoverageMs,
			PackageCoverageHours:  now.PackageCoverageHours,
			PackageProfile:        now.PackageProfile,
		})
	}
	writeJSON(w, playableSourcesResponse{
		NowMs:       nowMs,
		Sources:     sources,
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
	})
}

func hlsManifestURL(channelID string) string {
	return "/hls/channel/" + url.PathEscape(channelID) + "/stream.m3u8"
}

func externalHLSManifestURL(channelID string) string {
	return "/hls/external/" + url.PathEscape(channelID) + "/stream.m3u8"
}
