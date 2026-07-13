package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

// spotifyURLDefaultDisplayName is the name given to the Spotify URL channel
// when it is first created. The UI exposes only a URL field for one Spotify→HLS
// bridge, so the name is not operator-editable here; updating the URL preserves
// whatever name the row already has.
const spotifyURLDefaultDisplayName = "Spotify"

// spotifyURLResponse is the singleton view of the Spotify URL channel.
// Configured is false when no Spotify URL is set.
type spotifyURLResponse struct {
	Configured     bool                `json:"configured"`
	ChannelID      string              `json:"channelId,omitempty"`
	DisplayName    string              `json:"displayName,omitempty"`
	UpstreamHLSURL string              `json:"upstreamHlsUrl,omitempty"`
	Status         string              `json:"status,omitempty"`
	NowPlaying     *externalNowPlaying `json:"nowPlaying,omitempty"`
}

func (a *App) spotifyURLResponseFor(ctx context.Context, ch *db.Channel) spotifyURLResponse {
	if ch == nil || ch.UpstreamHLSURL == nil {
		return spotifyURLResponse{Configured: false}
	}
	nowPlaying, err := a.fetchExternalNowPlaying(ctx, *ch)
	if err != nil {
		nowPlaying = nil
	}
	return spotifyURLResponse{
		Configured:     true,
		ChannelID:      ch.ID,
		DisplayName:    ch.DisplayName,
		UpstreamHLSURL: *ch.UpstreamHLSURL,
		Status:         a.externalChannelStatus(ctx, *ch),
		NowPlaying:     nowPlaying,
	}
}

// handleSpotifyUrlGet returns the Spotify URL channel (or configured:false).
func (a *App) handleSpotifyUrlGet(w http.ResponseWriter, r *http.Request) {
	ch, err := db.ExternalChannel(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, a.spotifyURLResponseFor(r.Context(), ch))
}

// handleSpotifyUrlSet upserts the Spotify URL channel: if one exists its URL is
// updated, otherwise a new channel is created. The reachability of the URL is
// advisory (the UI's Test button) — saving never requires it.
func (a *App) handleSpotifyUrlSet(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req struct {
		UpstreamHLSURL string `json:"upstreamHlsUrl"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	rawURL := strings.TrimSpace(req.UpstreamHLSURL)
	if !validUpstreamHLSURL(rawURL) {
		writeError(w, http.StatusBadRequest, "invalid_upstream_hls_url", "upstreamHlsUrl must be an http or https URL")
		return
	}

	existing, err := db.ExternalChannel(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing != nil {
		if _, err := db.SetChannelUpstreamHLSURL(r.Context(), a.dbConn, existing.ID, rawURL); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	} else if _, herr := a.createChannel(r.Context(), createChannelRequest{
		DisplayName:    spotifyURLDefaultDisplayName,
		UpstreamHLSURL: rawURL,
	}); herr != nil {
		writeError(w, herr.Status, herr.Code, herr.Message)
		return
	}

	ch, err := db.ExternalChannel(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, a.spotifyURLResponseFor(r.Context(), ch))
}

// handleSpotifyUrlClear deletes the Spotify URL channel. It is idempotent:
// clearing when none is configured returns configured:false. Spotify URL
// channels own no packaged media or schedule, so the row is deleted directly
// without the disable-before-delete guard packaged channels carry.
func (a *App) handleSpotifyUrlClear(w http.ResponseWriter, r *http.Request) {
	existing, err := db.ExternalChannel(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeJSON(w, map[string]any{"configured": false, "deleted": false})
		return
	}
	if _, err := db.DeleteChannel(r.Context(), a.dbConn, existing.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"configured": false,
		"deleted":    true,
		"channelId":  existing.ID,
		"note":       "linearcast drops the channel on its next ~60s refresh tick.",
	})
}
