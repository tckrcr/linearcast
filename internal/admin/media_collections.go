package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

type mediaCollectionBulkRequest struct {
	Action         string                          `json:"action"`
	Collection     string                          `json:"collection,omitempty"`
	FromCollection string                          `json:"fromCollection,omitempty"`
	MediaIDs       []string                        `json:"mediaIds,omitempty"`
	Filter         *mediaCollectionBulkFilterInput `json:"filter,omitempty"`
	DryRun         bool                            `json:"dryRun,omitempty"`
}

type mediaCollectionBulkFilterInput struct {
	Q             string `json:"q,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Collection    string `json:"collection,omitempty"`
	PackageStatus string `json:"packageStatus,omitempty"`
	CodecStatus   string `json:"codecStatus,omitempty"`
}

type mediaCollectionBulkResponse struct {
	Action         string `json:"action"`
	Collection     string `json:"collection,omitempty"`
	FromCollection string `json:"fromCollection,omitempty"`
	DryRun         bool   `json:"dryRun"`
	Matched        int64  `json:"matched"`
	Updated        int64  `json:"updated"`
}

func (a *App) handleMediaCollectionBulk(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req mediaCollectionBulkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	action := strings.TrimSpace(req.Action)
	mutation := db.MediaCollectionBulkMutation{
		Action:         action,
		Collection:     strings.TrimSpace(req.Collection),
		FromCollection: strings.TrimSpace(req.FromCollection),
		Scope: db.MediaCollectionBulkScope{
			MediaIDs: req.MediaIDs,
			Filter:   mediaCollectionFilter(req.Filter),
		},
	}

	matched, err := db.CountMediaCollectionBulkMutation(r.Context(), a.dbConn, mutation)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	resp := mediaCollectionBulkResponse{
		Action:         action,
		Collection:     strings.TrimSpace(req.Collection),
		FromCollection: strings.TrimSpace(req.FromCollection),
		DryRun:         req.DryRun,
		Matched:        matched,
	}
	if req.DryRun {
		writeJSON(w, resp)
		return
	}

	updated, err := db.ApplyMediaCollectionBulkMutation(r.Context(), a.dbConn, mutation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	resp.Updated = updated
	writeJSON(w, resp)
}

func mediaCollectionFilter(in *mediaCollectionBulkFilterInput) *db.MediaInventoryFilter {
	if in == nil {
		return nil
	}
	return &db.MediaInventoryFilter{
		Search:        in.Q,
		MediaKind:     in.Kind,
		Collection:    in.Collection,
		PackageStatus: in.PackageStatus,
		CodecStatus:   in.CodecStatus,
	}
}
