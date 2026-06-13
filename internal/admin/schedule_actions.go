package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/scheduler"
)

type scheduleEntryWriteRequest struct {
	MediaID string `json:"mediaId"`
	StartMs int64  `json:"startMs"`
}

type scheduleWindowSaveOrderedRequest struct {
	FromMs     int64                            `json:"fromMs"`
	ToMs       int64                            `json:"toMs"`
	TailMode   string                           `json:"tailMode"`
	ExtendTail *bool                            `json:"extendTail,omitempty"`
	Entries    []scheduleWindowSaveOrderedEntry `json:"entries"`
}

type scheduleWindowSaveOrderedEntry struct {
	MediaID string `json:"mediaId"`
}

type scheduleGapFillRequest struct {
	MediaID  string `json:"mediaId"`
	StartMs  int64  `json:"startMs"`
	OffsetMs int64  `json:"offsetMs"`
	// OffsetMode selects how the filler start offset is chosen. "" / "zero" use
	// the client-provided OffsetMs as-is; "sequential" lets the server continue
	// the filler rotation from the previous placement of the same asset.
	OffsetMode string `json:"offsetMode"`
}

type scheduleEntryInsertRequest struct {
	MediaID string `json:"mediaId"`
}

type readyScheduleMedia struct {
	mediaID    string
	durationMs int64
}

const (
	hintAddMediaToChannel = "Add the media to this channel before scheduling it, or choose a current channel member."
	hintPackageNotReady   = "Queue package work for this media and profile, wait for the packager-worker to finish, then retry."
	hintRefreshSchedule   = "Refresh the schedule editor and retry; this entry may have already changed."
	hintScheduleBoundary  = "Use a time aligned to the 6-second schedule grid; refresh the editor if the grid looks stale."
)

// scheduleServiceError maps a sentinel error from scheduleService to an HTTP
// status code and a JSON error code string.
func scheduleServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errChannelNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, errEntryNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), hintRefreshSchedule)
	case errors.Is(err, errMediaNotInChannel):
		writeError(w, http.StatusBadRequest, "media_not_in_channel", err.Error(), hintAddMediaToChannel)
	case errors.Is(err, errPackageNotReady):
		writeError(w, http.StatusConflict, "package_not_ready", err.Error(), hintPackageNotReady)
	case errors.Is(err, errInsideScheduleEntry):
		writeError(w, http.StatusConflict, "inside_schedule_entry", err.Error(), "Choose the existing entry start time or the next aligned boundary after it.")
	case errors.Is(err, errNoScheduleGap):
		writeError(w, http.StatusConflict, "no_schedule_gap", err.Error(), "Drop filler onto an open schedule gap with a 6-second-aligned boundary.")
	case errors.Is(err, errFillerTooShort):
		writeError(w, http.StatusConflict, "filler_too_short", err.Error(), "Choose a longer filler asset or an earlier filler offset.")
	case errors.Is(err, errScheduleEntryLocked):
		writeError(w, http.StatusConflict, "schedule_entry_locked", err.Error(), hintRefreshSchedule)
	case errors.Is(err, errNotSlotGrid):
		writeError(w, http.StatusConflict, "not_slot_grid", err.Error())
	case errors.Is(err, scheduler.ErrNoReadyPackages):
		writeError(w, http.StatusConflict, "no_ready_packages", "no eligible ready packaged media to recompose the schedule", hintPackageNotReady)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (a *App) handleChannelFillScheduleGap(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")

	var req scheduleGapFillRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media_id", "mediaId is required")
		return
	}
	if req.StartMs%segmentMs != 0 {
		writeError(w, http.StatusBadRequest, "invalid_start_ms", "startMs must be aligned to the schedule segment grid", hintScheduleBoundary)
		return
	}
	if req.OffsetMs < 0 || req.OffsetMs%segmentMs != 0 {
		writeError(w, http.StatusBadRequest, "invalid_offset_ms", "offsetMs must be non-negative and aligned to the schedule segment grid", hintScheduleBoundary)
		return
	}
	req.OffsetMode = strings.TrimSpace(req.OffsetMode)
	switch req.OffsetMode {
	case "", "zero", "sequential":
	default:
		writeError(w, http.StatusBadRequest, "invalid_offset_mode", "offsetMode must be empty, zero, or sequential")
		return
	}

	res, err := a.schedule.FillGap(r.Context(), channelID, req)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"channelID":      channelID,
		"entryId":        res.EntryID,
		"mediaId":        res.MediaID,
		"startMs":        res.StartMs,
		"endMs":          res.EndMs,
		"durationMs":     res.DurationMs,
		"offsetMs":       res.OffsetMs,
		"packageProfile": res.PackageProfile,
	})
}

// handleChannelRecomposeSlotGrid rebuilds a slot-grid channel's future schedule
// gap-free in one shot (clear-after-now + server-side slot tiling). It is the
// existing-channel edit-path replacement for one-gap-at-a-time FillGap.
func (a *App) handleChannelRecomposeSlotGrid(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")

	res, err := a.schedule.RecomposeSlotGridFuture(r.Context(), channelID)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	note := ""
	if res.Gappy {
		note = "no usable filler; schedule rebuilt with gaps — attach a ready filler asset and recompose again"
	}
	writeJSON(w, map[string]any{
		"channelID": channelID,
		"fromMs":    res.FromMs,
		"cleared":   res.Cleared,
		"inserted":  res.Inserted,
		"lastEndMs": res.LastEndMs,
		"gappy":     res.Gappy,
		"note":      note,
	})
}

func (a *App) handleChannelSaveScheduleWindowOrdered(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")

	var req scheduleWindowSaveOrderedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.TailMode = strings.TrimSpace(req.TailMode)
	if req.TailMode == "" {
		req.TailMode = "preserve"
	}
	if req.TailMode != "preserve" && req.TailMode != "jump" {
		writeError(w, http.StatusBadRequest, "invalid_tail_mode", "tailMode must be preserve or jump")
		return
	}
	extendTail := true
	if req.ExtendTail != nil {
		extendTail = *req.ExtendTail
	}
	if req.FromMs%segmentMs != 0 {
		writeError(w, http.StatusBadRequest, "invalid_from_ms", "fromMs must be aligned to the schedule segment grid", hintScheduleBoundary)
		return
	}
	if req.ToMs <= req.FromMs {
		writeError(w, http.StatusBadRequest, "invalid_window", "toMs must be greater than fromMs")
		return
	}
	if len(req.Entries) == 0 && extendTail {
		writeError(w, http.StatusBadRequest, "missing_entries", "entries must contain at least one item")
		return
	}
	for _, entry := range req.Entries {
		entry.MediaID = strings.TrimSpace(entry.MediaID)
		if entry.MediaID == "" {
			writeError(w, http.StatusBadRequest, "missing_media_id", "entries mediaId is required")
			return
		}
	}

	res, err := a.schedule.SaveWindowOrdered(r.Context(), channelID, req)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	note := ""
	if res.NoPackages {
		note = "no ready packages; schedule extended up to the gap only"
	}
	writeJSON(w, map[string]any{
		"channelID":        channelID,
		"fromMs":           req.FromMs,
		"toMs":             req.ToMs,
		"tailMode":         req.TailMode,
		"extendTail":       extendTail,
		"cleared":          res.Cleared,
		"inserted":         res.Inserted,
		"lastEndMs":        res.LastEndMs,
		"resumeAfterMedia": res.ResumeAfterMediaID,
		"note":             note,
	})
}

func (a *App) handleChannelInsertScheduleEntryAfter(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")
	entryID := r.PathValue("entryId")
	if entryID == "" {
		writeError(w, http.StatusBadRequest, "invalid_entry_id", "entryId path parameter is required")
		return
	}

	var req scheduleEntryInsertRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media_id", "mediaId is required")
		return
	}

	res, err := a.schedule.InsertEntryAfter(r.Context(), channelID, entryID, req.MediaID)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"channelID":    channelID,
		"entryId":      res.EntryID,
		"afterEntryId": entryID,
		"mediaId":      req.MediaID,
		"startMs":      res.StartMs,
		"durationMs":   res.DurationMs,
		"inserted":     1,
	})
}

func (a *App) handleChannelInsertScheduleEntryBefore(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")
	entryID := r.PathValue("entryId")
	if entryID == "" {
		writeError(w, http.StatusBadRequest, "invalid_entry_id", "entryId path parameter is required")
		return
	}

	var req scheduleEntryInsertRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media_id", "mediaId is required")
		return
	}

	res, err := a.schedule.InsertEntryBefore(r.Context(), channelID, entryID, req.MediaID)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"channelID":     channelID,
		"entryId":       res.EntryID,
		"beforeEntryId": entryID,
		"mediaId":       req.MediaID,
		"startMs":       res.StartMs,
		"durationMs":    res.DurationMs,
		"inserted":      1,
	})
}

func (a *App) handleChannelUpsertScheduleEntry(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")

	var req scheduleEntryWriteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media_id", "mediaId is required")
		return
	}
	if req.StartMs%segmentMs != 0 {
		writeError(w, http.StatusBadRequest, "invalid_start_ms", "startMs must be aligned to the schedule segment grid", hintScheduleBoundary)
		return
	}

	res, err := a.schedule.UpsertEntry(r.Context(), channelID, req)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	note := ""
	if res.NoPackages {
		note = "no ready packages; schedule extended up to the gap only"
	}
	writeJSON(w, map[string]any{
		"channelID":        channelID,
		"startMs":          req.StartMs,
		"mediaId":          req.MediaID,
		"durationMs":       res.DurationMs,
		"cleared":          res.Cleared,
		"inserted":         res.Inserted,
		"lastEndMs":        res.LastEndMs,
		"packageProfile":   res.PackageProfile,
		"resumeAfterMedia": req.MediaID,
		"note":             note,
	})
}

func (a *App) handleChannelDeleteScheduleRange(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")

	fromMs, ok := parseQueryUnixMs(w, r, "from")
	if !ok {
		return
	}
	toMs, ok := parseQueryUnixMs(w, r, "to")
	if !ok {
		return
	}
	if toMs <= fromMs {
		writeError(w, http.StatusBadRequest, "invalid_range", "to must be greater than from")
		return
	}

	rebuild := r.URL.Query().Get("rebuild") != "false"
	res, err := a.schedule.DeleteRange(r.Context(), channelID, fromMs, toMs, rebuild)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	if res.NoOp {
		writeJSON(w, map[string]any{
			"channelID": channelID,
			"fromMs":    fromMs,
			"toMs":      toMs,
			"deleted":   0,
			"inserted":  0,
		})
		return
	}

	note := ""
	if res.NoPackages {
		note = "no ready packages; schedule extended up to the gap only"
	}
	writeJSON(w, map[string]any{
		"channelID":        channelID,
		"fromMs":           fromMs,
		"toMs":             toMs,
		"rebuildStartMs":   res.RebuildStartMs,
		"deleted":          res.Deleted,
		"clearedTail":      res.ClearedTail,
		"inserted":         res.Inserted,
		"lastEndMs":        res.LastEndMs,
		"resumeAfterMedia": res.ResumeAfterMediaID,
		"note":             note,
	})
}

// handleChannelDeleteScheduleEntry removes a schedule entry by its stable ID,
// then clears all entries at or after that point and re-extends the schedule
// so start times remain contiguous. Returns 404 if the entry does not exist.
func (a *App) handleChannelDeleteScheduleEntry(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	entryID := r.PathValue("entryId")
	if entryID == "" {
		writeError(w, http.StatusBadRequest, "invalid_entry_id", "entryId path parameter is required")
		return
	}

	rebuild := r.URL.Query().Get("rebuild") != "false"
	res, err := a.schedule.DeleteEntry(r.Context(), channelID, entryID, rebuild)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	note := ""
	if res.NoPackages {
		note = "no ready packages; schedule extended up to the gap only"
	}
	writeJSON(w, map[string]any{
		"channelID": channelID,
		"entryId":   entryID,
		"inserted":  res.Inserted,
		"note":      note,
	})
}

// handleChannelRestartPlayback clears the existing schedule and immediately
// re-extends it. Both steps run in a single SQLite transaction so a partial
// failure leaves the schedule unchanged. linearcast observes the new schedule
// on its next ~60s refresh.
func (a *App) handleChannelRestartPlayback(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")

	res, err := a.schedule.Restart(r.Context(), channelID)
	if err != nil {
		scheduleServiceError(w, err)
		return
	}

	resp := map[string]any{
		"channelID": channelID,
		"cleared":   res.Cleared,
		"inserted":  res.Inserted,
		"lastEndMs": res.LastEndMs,
		"note":      "linearcast picks up the new schedule on its next ~60s refresh tick.",
	}
	if res.NoPackages {
		resp["warning"] = "no ready packages yet; schedule cleared but not re-extended. Queue packages in the Encoding tab and run Extend once ready."
	}
	writeJSON(w, resp)
}
