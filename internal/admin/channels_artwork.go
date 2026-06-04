package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

type channelArtworkRequest struct {
	ArtworkURL string `json:"artworkUrl"`
}

type channelArtworkResponse struct {
	ChannelID  string `json:"channelId"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
}

func (a *App) handleChannelArtwork(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	ch, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if ch == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	writeJSON(w, channelArtworkResponse{
		ChannelID:  ch.ID,
		ArtworkURL: ch.ArtworkURL,
	})
}

func (a *App) handleChannelArtworkUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")
	var req channelArtworkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	artworkURL := strings.TrimSpace(req.ArtworkURL)
	if artworkURL != "" {
		parsed, err := url.Parse(artworkURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			writeError(w, http.StatusBadRequest, "invalid_artwork_url", "artworkUrl must be an absolute http or https URL")
			return
		}
	}
	updated, err := db.SetChannelArtworkURL(r.Context(), a.dbConn, channelID, artworkURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	writeJSON(w, channelArtworkResponse{ChannelID: channelID, ArtworkURL: artworkURL})
}

func (a *App) handleChannelArtworkReset(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	updated, err := db.SetChannelArtworkURL(r.Context(), a.dbConn, channelID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	writeJSON(w, channelArtworkResponse{ChannelID: channelID})
}
