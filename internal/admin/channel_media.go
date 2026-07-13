package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

type channelMediaItem struct {
	MediaID            string `json:"mediaId"`
	Title              string `json:"title,omitempty"`
	Path               string `json:"path,omitempty"`
	CollectionName     string `json:"collectionName,omitempty"`
	DurationMs         int64  `json:"durationMs"`
	CodecCheckPassed   bool   `json:"codecCheckPassed"`
	CodecCheckReason   string `json:"codecCheckReason,omitempty"`
	PackageID          string `json:"packageId,omitempty"`
	PackageStatus      string `json:"packageStatus"`
	PackageReady       bool   `json:"packageReady"`
	PackagedDurationMs *int64 `json:"packagedDurationMs,omitempty"`
	PackageError       string `json:"packageError,omitempty"`
}

type channelMediaResponse struct {
	ChannelID              string             `json:"channelId"`
	DisplayName            string             `json:"displayName"`
	RequiredPackageProfile string             `json:"requiredPackageProfile"`
	Count                  int                `json:"count"`
	Media                  []channelMediaItem `json:"media"`
}

// handleChainIntegrity walks every channel's channel_media chain and reports
// invariant violations (missing/multiple heads, multiple successors,
// self-anchors, orphan anchors, cycles, unreachable rows). Same check that
// runs at boot. Returns 200 with an empty issues array when chains are clean.
func (a *App) handleChainIntegrity(w http.ResponseWriter, r *http.Request) {
	issues, err := db.ValidateChannelMediaChains(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if issues == nil {
		issues = []db.ChainIssue{}
	}
	writeJSON(w, map[string]any{
		"issueCount": len(issues),
		"issues":     issues,
	})
}

func (a *App) handleChannelMedia(w http.ResponseWriter, r *http.Request) {
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
	rows, err := db.ChannelMediaPackageList(r.Context(), a.dbConn, channelID, profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	items := make([]channelMediaItem, 0, len(rows))
	for _, row := range rows {
		status := "missing"
		if row.PackageStatus != nil {
			status = *row.PackageStatus
		}
		item := channelMediaItem{
			MediaID:          row.MediaID,
			Path:             row.Path,
			DurationMs:       row.DurationMs,
			CodecCheckPassed: row.CodecCheckPassed,
			PackageStatus:    status,
			PackageReady:     status == string(db.PackageStatusReady) && row.PackagedDurationMs != nil,
		}
		if row.Title != "" {
			item.Title = row.Title
		}
		if row.CollectionName != "" {
			item.CollectionName = row.CollectionName
		}
		if row.CodecCheckReason != "" {
			item.CodecCheckReason = row.CodecCheckReason
		}
		if row.PackageID != nil {
			item.PackageID = *row.PackageID
		}
		if row.PackagedDurationMs != nil {
			v := *row.PackagedDurationMs
			item.PackagedDurationMs = &v
		}
		if row.PackageError != nil {
			item.PackageError = *row.PackageError
		}
		items = append(items, item)
	}
	writeJSON(w, channelMediaResponse{
		ChannelID:              ch.ID,
		DisplayName:            ch.DisplayName,
		RequiredPackageProfile: profile,
		Count:                  len(items),
		Media:                  items,
	})
}

// handleChannelAddMedia adds a single media item to a channel's membership.
// By default the new row appends to the linked-list tail. If the request body
// includes "afterMediaId" (even as the empty string), the row is inserted at
// that position instead: empty string means "head", a non-empty value means
// "directly after that media id". Omitting the field preserves tail-append.
func (a *App) handleChannelAddMedia(w http.ResponseWriter, r *http.Request) {
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
		MediaID      string  `json:"mediaId"`
		AfterMediaID *string `json:"afterMediaId,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
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
		writeError(w, http.StatusNotFound, "media_not_found", "media not found: "+req.MediaID)
		return
	}
	mediaKind := db.NormalizeMediaKind(media.MediaKind)
	if mediaKind != ch.MediaKind {
		writeError(w, http.StatusUnprocessableEntity, "media_kind_mismatch",
			fmt.Sprintf("media kind %s cannot be added to %s channel %s", mediaKind, ch.MediaKind, channelID))
		return
	}
	if !media.CodecCheckPassed {
		writeError(w, http.StatusUnprocessableEntity, "codec_check_failed", "media failed codec check: "+media.CodecCheckReason)
		return
	}

	nowMs := a.now().UTC().UnixMilli()
	var added bool
	if req.AfterMediaID == nil {
		added, err = db.AddChannelMedia(r.Context(), a.dbConn, channelID, req.MediaID, nowMs)
	} else {
		added, err = db.AddChannelMediaAfter(r.Context(), a.dbConn, channelID, req.MediaID, *req.AfterMediaID, nowMs)
	}
	if errors.Is(err, db.ErrInvalidMove) {
		writeError(w, http.StatusBadRequest, "invalid_anchor", "cannot anchor a media item to itself")
		return
	}
	if errors.Is(err, db.ErrMediaNotInChannel) {
		writeError(w, http.StatusBadRequest, "anchor_not_member", "afterMediaId is not a member of this channel")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !added {
		writeError(w, http.StatusConflict, "already_member", "media is already a member of this channel")
		return
	}

	resp := map[string]any{
		"channelID": channelID,
		"mediaId":   req.MediaID,
		"added":     true,
		"note":      "future schedule extension will include this media; run extend to rebuild immediately",
	}
	if req.AfterMediaID != nil {
		resp["afterMediaId"] = *req.AfterMediaID
	}
	writeJSONStatus(w, http.StatusCreated, resp)
}

// handleChannelRemoveMedia removes a media item from a channel's membership.
// By default (?pruneSchedule=true) it also clears all future schedule entries
// for that media and re-extends the schedule from the earliest pruned point,
// so playback remains contiguous. Pass ?pruneSchedule=false to skip that step.
func (a *App) handleChannelRemoveMedia(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	mediaID := r.PathValue("mediaID")
	pruneSchedule := r.URL.Query().Get("pruneSchedule") != "false"

	res, err := a.schedule.RemoveMedia(r.Context(), channelID, mediaID, pruneSchedule)
	if errors.Is(err, errChannelNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if errors.Is(err, errMediaNotInChannel) {
		writeError(w, http.StatusNotFound, "not_member", "media is not a member of this channel", hintRefreshSchedule)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	if res.RebuildStartMs > 0 {
		note := "schedule pruned and rebuilt"
		if res.NoPackages {
			note = "schedule pruned; no ready packages to rebuild tail"
		}
		writeJSON(w, map[string]any{
			"channelID":      channelID,
			"mediaId":        mediaID,
			"removed":        true,
			"prunedSchedule": res.Pruned,
			"rebuildStartMs": res.RebuildStartMs,
			"inserted":       res.Inserted,
			"note":           note,
		})
		return
	}

	note := "membership removed; no future schedule entries found for this media"
	if !pruneSchedule {
		note = "membership removed; existing schedule entries were not pruned"
	}
	writeJSON(w, map[string]any{
		"channelID":      channelID,
		"mediaId":        mediaID,
		"removed":        true,
		"prunedSchedule": int64(0),
		"inserted":       int64(0),
		"note":           note,
	})
}

// handleChannelMoveMedia repositions a single media item in the channel's
// linked-list order. Body: { "afterMediaId": "<id>" } — pass an empty string
// (or omit) to move to the head of the channel. The operation is a pure
// membership change: schedule_entries are not touched. Existing schedule rows
// keep playing in their already-materialized order until the next extend.
func (a *App) handleChannelMoveMedia(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")
	mediaID := r.PathValue("mediaID")

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
		AfterMediaID string `json:"afterMediaId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	err = db.MoveChannelMediaAfter(r.Context(), a.dbConn, channelID, mediaID, req.AfterMediaID)
	if errors.Is(err, db.ErrInvalidMove) {
		writeError(w, http.StatusBadRequest, "invalid_move", "cannot anchor a media item to itself")
		return
	}
	if errors.Is(err, db.ErrMediaNotInChannel) {
		writeError(w, http.StatusNotFound, "not_member", "media is not a member of this channel", hintRefreshSchedule)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, map[string]any{
		"channelID":    channelID,
		"mediaId":      mediaID,
		"afterMediaId": req.AfterMediaID,
		"note":         "membership reordered; future schedule extension will use the new order",
	})
}

// handleChannelReorderMedia replaces the channel_media linked-list ordering
// wholesale from an ordered list of all current member media IDs. The caller
// must supply exactly the current member set — any missing or extra IDs are
// rejected. Anchors are assigned by slice position so the chain matches the
// caller-supplied order.
func (a *App) handleChannelReorderMedia(w http.ResponseWriter, r *http.Request) {
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
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if len(req.Order) == 0 {
		writeError(w, http.StatusBadRequest, "empty_order", "order must not be empty")
		return
	}

	current, err := db.ChannelMediaList(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if len(req.Order) != len(current) {
		writeError(w, http.StatusBadRequest, "order_mismatch",
			fmt.Sprintf("order contains %d IDs but channel has %d members", len(req.Order), len(current)))
		return
	}

	byID := make(map[string]db.ChannelMediaRow, len(current))
	for _, row := range current {
		byID[row.MediaID] = row
	}

	seen := make(map[string]bool, len(req.Order))
	rows := make([]db.ChannelMediaRow, len(req.Order))
	for i, mediaID := range req.Order {
		if seen[mediaID] {
			writeError(w, http.StatusBadRequest, "duplicate_media_id", "duplicate mediaId in order: "+mediaID)
			return
		}
		seen[mediaID] = true
		existing, ok := byID[mediaID]
		if !ok {
			writeError(w, http.StatusBadRequest, "not_member", "mediaId is not a member of this channel: "+mediaID)
			return
		}
		rows[i] = db.ChannelMediaRow{
			ChannelID: channelID,
			MediaID:   mediaID,
			AddedAtMs: existing.AddedAtMs,
		}
	}

	if err := db.WithTx(r.Context(), a.dbConn, func(tx db.Execer) error {
		return db.ReplaceChannelMedia(r.Context(), tx, channelID, rows)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, map[string]any{
		"channelID": channelID,
		"count":     len(rows),
		"note":      "linked-list order replaced; future schedule extension will use the new order",
	})
}
