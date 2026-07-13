package admin

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

type nullableIntPatch struct {
	Set   bool
	Value *int64
}

func (p *nullableIntPatch) UnmarshalJSON(data []byte) error {
	p.Set = true
	var v *int64
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	p.Value = v
	return nil
}

type mediaUpdateRequest struct {
	Title          *string          `json:"title"`
	CollectionName *string          `json:"collectionName"`
	SeasonNumber   nullableIntPatch `json:"seasonNumber"`
	EpisodeNumber  nullableIntPatch `json:"episodeNumber"`
}

type mediaUpdateResponse struct {
	MediaID        string `json:"mediaId"`
	Path           string `json:"path"`
	Title          string `json:"title"`
	CollectionName string `json:"collectionName"`
	SeasonNumber   *int64 `json:"seasonNumber,omitempty"`
	EpisodeNumber  *int64 `json:"episodeNumber,omitempty"`
}

// handleMediaUpdate applies a partial update to a media row's user-editable
// fields (title, collectionName, seasonNumber, episodeNumber). Only fields present in the JSON body are
// written; absent fields are left unchanged. An explicit empty string clears
// text fields; an explicit null clears numeric ordering fields.
func (a *App) handleMediaUpdate(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaID")

	var req mediaUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.Title == nil && req.CollectionName == nil && !req.SeasonNumber.Set && !req.EpisodeNumber.Set {
		writeError(w, http.StatusBadRequest, "no_fields", "request must include at least one of: title, collectionName, seasonNumber, episodeNumber")
		return
	}
	if err := validateOrderingPatch("seasonNumber", req.SeasonNumber); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_season_number", err.Error())
		return
	}
	if err := validateOrderingPatch("episodeNumber", req.EpisodeNumber); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_episode_number", err.Error())
		return
	}

	found, err := db.UpdateMediaFields(
		r.Context(),
		a.dbConn,
		mediaID,
		req.Title,
		req.CollectionName,
		req.SeasonNumber.Set,
		req.SeasonNumber.Value,
		req.EpisodeNumber.Set,
		req.EpisodeNumber.Value,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "media not found")
		return
	}

	m, err := db.MediaByID(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, mediaUpdateResponse{
		MediaID:        m.ID,
		Path:           m.Path,
		Title:          m.Title,
		CollectionName: m.CollectionName,
		SeasonNumber:   m.SeasonNumber,
		EpisodeNumber:  m.EpisodeNumber,
	})
}

func validateOrderingPatch(name string, patch nullableIntPatch) error {
	if !patch.Set || patch.Value == nil {
		return nil
	}
	if *patch.Value < 1 {
		return fmt.Errorf("%s must be null or a positive integer", name)
	}
	return nil
}
