package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

type scheduleBuilderSourceStatusResponse struct {
	HasMediaSource     bool `json:"hasMediaSource"`
	PlexConfigured     bool `json:"plexConfigured"`
	JellyfinConfigured bool `json:"jellyfinConfigured"`
	LocalSourceCount   int  `json:"localSourceCount"`
}

type scheduleBuilderCreateChannelResponse struct {
	ChannelID       string                        `json:"channelID"`
	DisplayName     string                        `json:"displayName"`
	Created         bool                          `json:"created"`
	SyncedMedia     int                           `json:"syncedMedia"`
	ScheduleEntries int                           `json:"scheduleEntries"`
	PackageProfile  string                        `json:"profile"`
	Queued          []string                      `json:"queued"`
	AlreadyPending  []string                      `json:"alreadyPending"`
	AlreadyReady    []string                      `json:"alreadyReady"`
	Failed          []mediaPackageFailureResponse `json:"failed"`
}

func (a *App) handleScheduleBuilderSourceStatus(w http.ResponseWriter, r *http.Request) {
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
	resp := scheduleBuilderSourceStatusResponse{
		PlexConfigured:     plexToken != "",
		JellyfinConfigured: jellyfinURL != "" && jellyfinAPIKey != "",
		LocalSourceCount:   len(localSources),
	}
	resp.HasMediaSource = resp.PlexConfigured || resp.JellyfinConfigured || resp.LocalSourceCount > 0
	writeJSON(w, resp)
}

func (a *App) handleScheduleBuilderCreateChannel(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req createChannelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	channelResp, herr := a.createChannel(r.Context(), req)
	if herr != nil {
		writeError(w, herr.Status, herr.Code, herr.Message)
		return
	}

	packageResult, err := db.RequestMediaPackages(r.Context(), a.dbConn, req.MediaIDs, strings.TrimSpace(req.PackageProfile))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	resp := scheduleBuilderCreateChannelResponse{
		ChannelID:       channelResp.ChannelID,
		DisplayName:     channelResp.DisplayName,
		Created:         channelResp.Created,
		SyncedMedia:     channelResp.SyncedMedia,
		ScheduleEntries: channelResp.ScheduleEntries,
		PackageProfile:  packageResult.Profile,
		Queued:          packageResult.Queued,
		AlreadyPending:  packageResult.AlreadyPending,
		AlreadyReady:    packageResult.AlreadyReady,
		Failed:          make([]mediaPackageFailureResponse, 0, len(packageResult.Failed)),
	}
	for _, failure := range packageResult.Failed {
		resp.Failed = append(resp.Failed, mediaPackageFailureResponse{
			MediaID: failure.MediaID,
			Code:    failure.Code,
			Message: failure.Message,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusCreated, resp)
}
