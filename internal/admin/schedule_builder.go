package admin

import (
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

type mediaSourceStatusResponse struct {
	HasMediaSource     bool `json:"hasMediaSource"`
	PlexConfigured     bool `json:"plexConfigured"`
	JellyfinConfigured bool `json:"jellyfinConfigured"`
	LocalSourceCount   int  `json:"localSourceCount"`
}

func (a *App) handleMediaSourceStatus(w http.ResponseWriter, r *http.Request) {
	plexToken, err := db.GetPlexToken(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex token")
		return
	}
	jellyfinURL, err := a.effectiveJellyfinURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin url")
		return
	}
	jellyfinAPIKey, err := db.GetJellyfinAPIKey(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin api key")
		return
	}
	localSources, err := db.ListLocalMediaSources(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	resp := mediaSourceStatusResponse{
		PlexConfigured:     plexToken != "",
		JellyfinConfigured: jellyfinURL != "" && jellyfinAPIKey != "",
		LocalSourceCount:   len(localSources),
	}
	resp.HasMediaSource = resp.PlexConfigured || resp.JellyfinConfigured || resp.LocalSourceCount > 0
	writeJSON(w, resp)
}
