package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

type fillerAssetItem struct {
	ID        string `json:"id"`
	MediaID   string `json:"mediaId"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAtMs"`
}

type channelFillerAssetItem struct {
	fillerAssetItem
	ChannelID          string `json:"channelId"`
	Weight             int64  `json:"weight"`
	ChannelEnabled     bool   `json:"channelEnabled"`
	Path               string `json:"path"`
	Title              string `json:"title,omitempty"`
	CollectionName     string `json:"collectionName,omitempty"`
	DurationMs         int64  `json:"durationMs"`
	PackageID          string `json:"packageId,omitempty"`
	PackageStatus      string `json:"packageStatus"`
	PackageReady       bool   `json:"packageReady"`
	PackagedDurationMs *int64 `json:"packagedDurationMs,omitempty"`
	PackageError       string `json:"packageError,omitempty"`
}

func fillerAssetResponse(a db.FillerAsset) fillerAssetItem {
	return fillerAssetItem{
		ID:        a.ID,
		MediaID:   a.MediaID,
		Label:     a.Label,
		Kind:      a.Kind,
		Enabled:   a.Enabled,
		CreatedAt: a.CreatedAtMs,
	}
}

func channelFillerAssetResponse(a db.ChannelFillerAsset) channelFillerAssetItem {
	status := "missing"
	if a.PackageStatus != nil {
		status = *a.PackageStatus
	}
	item := channelFillerAssetItem{
		fillerAssetItem: fillerAssetResponse(a.FillerAsset),
		ChannelID:       a.ChannelID,
		Weight:          a.Weight,
		ChannelEnabled:  a.ChannelEnabled,
		Path:            a.Path,
		DurationMs:      a.DurationMs,
		PackageStatus:   status,
		PackageReady:    status == string(db.PackageStatusReady) && a.PackagedDurationMs != nil,
	}
	if a.Title != "" {
		item.Title = a.Title
	}
	if a.CollectionName != "" {
		item.CollectionName = a.CollectionName
	}
	if a.PackageID != nil {
		item.PackageID = *a.PackageID
	}
	if a.PackagedDurationMs != nil {
		v := *a.PackagedDurationMs
		item.PackagedDurationMs = &v
	}
	if a.PackageError != nil {
		item.PackageError = *a.PackageError
	}
	return item
}

func (a *App) handleFillerAssetCandidates(w http.ResponseWriter, r *http.Request) {
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	candidates, err := db.FillerAssetsForScheduleBuilder(r.Context(), a.dbConn, profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		item := map[string]any{
			"id":            c.ID,
			"mediaId":       c.MediaID,
			"label":         c.Label,
			"kind":          c.Kind,
			"durationMs":    c.DurationMs,
			"packageStatus": c.PackageStatus,
			"packageReady":  c.PackageStatus == string(db.PackageStatusReady) && c.PackagedDurationMs != nil,
		}
		if c.PackageID != nil {
			item["packageId"] = *c.PackageID
		}
		if c.PackagedDurationMs != nil {
			item["packagedDurationMs"] = *c.PackagedDurationMs
		}
		items = append(items, item)
	}
	writeJSON(w, map[string]any{
		"profile": profile,
		"count":   len(items),
		"assets":  items,
	})
}

func (a *App) handleFillerAssets(w http.ResponseWriter, r *http.Request) {
	rows, err := db.FillerAssets(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	items := make([]fillerAssetItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, fillerAssetResponse(row))
	}
	writeJSON(w, map[string]any{
		"count":  len(items),
		"assets": items,
	})
}

func (a *App) handleFillerAssetCreate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req struct {
		MediaID string `json:"mediaId"`
		Label   string `json:"label"`
		Kind    string `json:"kind"`
		Enabled *bool  `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media_id", "mediaId is required")
		return
	}
	media, err := db.MediaByID(r.Context(), a.dbConn, req.MediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if media == nil {
		writeError(w, http.StatusNotFound, "media_not_found", "media not found")
		return
	}
	if !media.CodecCheckPassed {
		writeError(w, http.StatusUnprocessableEntity, "codec_check_failed", "media failed codec check")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = media.ID
		if strings.TrimSpace(media.Title) != "" {
			label = strings.TrimSpace(media.Title)
		}
	}
	asset, err := db.UpsertFillerAsset(r.Context(), a.dbConn, db.FillerAsset{
		MediaID:     media.ID,
		Label:       label,
		Kind:        req.Kind,
		Enabled:     enabled,
		CreatedAtMs: a.now().UTC().UnixMilli(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, fillerAssetResponse(asset))
}

func (a *App) handleChannelFillerAssets(w http.ResponseWriter, r *http.Request) {
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
	profile := requiredPackageProfile(*ch)
	rows, err := db.ChannelFillerAssets(r.Context(), a.dbConn, channelID, profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	items := make([]channelFillerAssetItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, channelFillerAssetResponse(row))
	}
	writeJSON(w, map[string]any{
		"channelId":              channelID,
		"requiredPackageProfile": profile,
		"count":                  len(items),
		"assets":                 items,
	})
}

func (a *App) handleChannelFillerAssetAttach(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
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

	var req struct {
		AssetID string `json:"assetId"`
		MediaID string `json:"mediaId"`
		Weight  int64  `json:"weight"`
		Enabled *bool  `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.AssetID = strings.TrimSpace(req.AssetID)
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.AssetID == "" && req.MediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_asset", "assetId or mediaId is required")
		return
	}
	var asset db.FillerAsset
	if req.AssetID != "" {
		asset, err = db.FillerAssetByID(r.Context(), a.dbConn, req.AssetID)
	} else {
		asset, err = db.FillerAssetByMediaID(r.Context(), a.dbConn, req.MediaID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "asset_not_found", "filler asset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// Filler must match the channel's media kind, mirroring the channel-add
	// gate (channel_media.go). A music asset can only package under a music
	// profile, so attaching it to a video channel would leave it permanently
	// unready and the gap-filler would never place it.
	media, err := db.MediaByID(r.Context(), a.dbConn, asset.MediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if media == nil {
		writeError(w, http.StatusNotFound, "media_not_found", "filler media not found")
		return
	}
	if mediaKind := db.NormalizeMediaKind(media.MediaKind); mediaKind != ch.MediaKind {
		writeError(w, http.StatusUnprocessableEntity, "media_kind_mismatch",
			fmt.Sprintf("media kind %s cannot be used as filler on %s channel %s", mediaKind, ch.MediaKind, channelID))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := db.AttachChannelFillerAsset(r.Context(), a.dbConn, channelID, asset.ID, req.Weight, enabled); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"channelId": channelID,
		"assetId":   asset.ID,
		"mediaId":   asset.MediaID,
		"attached":  true,
	})
}

func (a *App) handleChannelFillerAssetDetach(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	assetID := r.PathValue("assetID")
	removed, err := db.DetachChannelFillerAsset(r.Context(), a.dbConn, channelID, assetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "not_found", "filler asset attachment not found")
		return
	}
	writeJSON(w, map[string]any{
		"channelId": channelID,
		"assetId":   assetID,
		"removed":   true,
	})
}
