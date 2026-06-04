package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

type scheduleEntryItem struct {
	EntryID         string `json:"entryId"`
	MediaID         string `json:"mediaId"`
	Title           string `json:"title,omitempty"`
	Path            string `json:"path,omitempty"`
	SchedulingGroup string `json:"schedulingGroup,omitempty"`
	StartMs         int64  `json:"startMs"`
	EndMs           int64  `json:"endMs"`
	OffsetMs        int64  `json:"offsetMs,omitempty"`
	DurationMs      int64  `json:"durationMs"`
}

type channelScheduleResponse struct {
	ChannelID   string              `json:"channelId"`
	DisplayName string              `json:"displayName"`
	FromMs      int64               `json:"fromMs"`
	ToMs        int64               `json:"toMs"`
	Count       int                 `json:"count"`
	Entries     []scheduleEntryItem `json:"entries"`
}

type schedulePreviewWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type schedulePreviewDiff struct {
	Unchanged int                 `json:"unchanged"`
	Added     []scheduleEntryItem `json:"added"`
	Removed   []scheduleEntryItem `json:"removed"`
}

type channelSchedulePreviewResponse struct {
	ChannelID          string                   `json:"channelId"`
	DisplayName        string                   `json:"displayName"`
	Ordering           string                   `json:"ordering"`
	Profile            string                   `json:"profile"`
	FromMs             int64                    `json:"fromMs"`
	ToMs               int64                    `json:"toMs"`
	GeneratedEndMs     int64                    `json:"generatedEndMs"`
	Count              int                      `json:"count"`
	EligibleMedia      int                      `json:"eligibleMedia"`
	EligibleReadyMedia int                      `json:"eligibleReadyMedia"`
	Warnings           []schedulePreviewWarning `json:"warnings"`
	Entries            []scheduleEntryItem      `json:"entries"`
	Diff               schedulePreviewDiff      `json:"diff"`
}

// handleChannelSchedule returns the schedule entries for a channel within a
// time window. Query params: ?hours=N (default 24, from now) and ?from=<unix-ms>
// to shift the window start.
func (a *App) handleChannelSchedule(w http.ResponseWriter, r *http.Request) {
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

	nowMs := a.now().UTC().UnixMilli()
	fromMs := nowMs
	horizonHours := 24

	if f := r.URL.Query().Get("from"); f != "" {
		if _, err := fmt.Sscanf(f, "%d", &fromMs); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_from", "from must be a unix-ms integer")
			return
		}
	}
	if h := r.URL.Query().Get("hours"); h != "" {
		if _, err := fmt.Sscanf(h, "%d", &horizonHours); err != nil || horizonHours <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_hours", "hours must be a positive integer")
			return
		}
	}
	toMs := fromMs + int64(horizonHours)*3600*1000

	raw, err := db.ScheduleWindowEnriched(r.Context(), a.dbConn, channelID, fromMs, toMs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	entries := make([]scheduleEntryItem, 0, len(raw))
	for _, e := range raw {
		item := scheduleEntryItem{
			EntryID:    e.ID,
			MediaID:    e.MediaID,
			Path:       e.Path,
			StartMs:    e.StartMs,
			EndMs:      e.StartMs + e.DurationMs,
			OffsetMs:   e.OffsetMs,
			DurationMs: e.DurationMs,
		}
		item.Title = e.Title
		item.SchedulingGroup = e.SchedulingGroup
		entries = append(entries, item)
	}

	writeJSON(w, channelScheduleResponse{
		ChannelID:   ch.ID,
		DisplayName: ch.DisplayName,
		FromMs:      fromMs,
		ToMs:        toMs,
		Count:       len(entries),
		Entries:     entries,
	})
}

// handleChannelSchedulePreview returns a read-only schedule regeneration
// preview for a channel window. Query params match handleChannelSchedule:
// ?hours=N (default 24, from now) and ?from=<unix-ms>.
func (a *App) handleChannelSchedulePreview(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	nowMs := a.now().UTC().UnixMilli()
	fromMs := nowMs
	horizonHours := 24

	if f := r.URL.Query().Get("from"); f != "" {
		if _, err := fmt.Sscanf(f, "%d", &fromMs); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_from", "from must be a unix-ms integer")
			return
		}
	}
	if h := r.URL.Query().Get("hours"); h != "" {
		if _, err := fmt.Sscanf(h, "%d", &horizonHours); err != nil || horizonHours <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_hours", "hours must be a positive integer")
			return
		}
	}

	preview, err := scheduler.PreviewChannel(r.Context(), a.dbConn, channelID, scheduler.PreviewOptions{
		FromMs:     fromMs,
		DurationMs: int64(horizonHours) * 3600 * 1000,
		NowMs:      nowMs,
	})
	if err != nil {
		if errors.Is(err, scheduler.ErrChannelNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "preview_error", err.Error())
		return
	}

	mediaByID, err := previewMediaDetails(a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	entries := make([]scheduleEntryItem, 0, len(preview.Entries))
	for _, entry := range preview.Entries {
		entries = append(entries, schedulePreviewEntryItem(entry, mediaByID[entry.MediaID]))
	}

	current, err := db.ScheduleWindowEnriched(r.Context(), a.dbConn, channelID, preview.FromMs, preview.ToMs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	diff := buildSchedulePreviewDiff(entries, current)
	warnings := make([]schedulePreviewWarning, 0, len(preview.Warnings))
	for _, warning := range preview.Warnings {
		warnings = append(warnings, schedulePreviewWarning{Code: warning.Code, Message: warning.Message})
	}

	writeJSON(w, channelSchedulePreviewResponse{
		ChannelID:          preview.ChannelID,
		DisplayName:        preview.DisplayName,
		Ordering:           preview.Ordering,
		Profile:            preview.RenditionProfile,
		FromMs:             preview.FromMs,
		ToMs:               preview.ToMs,
		GeneratedEndMs:     preview.GeneratedEndMs,
		Count:              len(entries),
		EligibleMedia:      preview.EligibleMedia,
		EligibleReadyMedia: preview.EligibleReadyMedia,
		Warnings:           warnings,
		Entries:            entries,
		Diff:               diff,
	})
}

type previewMediaDetail struct {
	Path            string
	Title           string
	SchedulingGroup string
}

func previewMediaDetails(conn *sql.DB, channelID string) (map[string]previewMediaDetail, error) {
	rows, err := conn.Query(`
		SELECT m.id, m.path, m.title, m.scheduling_group
		FROM channel_media cm
		JOIN media m ON m.id = cm.media_id
		WHERE cm.channel_id = ?`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]previewMediaDetail{}
	for rows.Next() {
		var id, path string
		var title, group sql.NullString
		if err := rows.Scan(&id, &path, &title, &group); err != nil {
			return nil, err
		}
		detail := previewMediaDetail{Path: path}
		if title.Valid {
			detail.Title = title.String
		}
		if group.Valid {
			detail.SchedulingGroup = group.String
		}
		out[id] = detail
	}
	return out, rows.Err()
}

func schedulePreviewEntryItem(entry db.ScheduleEntry, detail previewMediaDetail) scheduleEntryItem {
	return scheduleEntryItem{
		EntryID:         entry.ID,
		MediaID:         entry.MediaID,
		Title:           detail.Title,
		Path:            detail.Path,
		SchedulingGroup: detail.SchedulingGroup,
		StartMs:         entry.StartMs,
		EndMs:           entry.StartMs + entry.DurationMs,
		OffsetMs:        entry.OffsetMs,
		DurationMs:      entry.DurationMs,
	}
}

func buildSchedulePreviewDiff(planned []scheduleEntryItem, current []db.ScheduleEntryEnriched) schedulePreviewDiff {
	currentByKey := map[string]scheduleEntryItem{}
	for _, entry := range current {
		item := scheduleEntryItem{
			EntryID:    entry.ID,
			MediaID:    entry.MediaID,
			Path:       entry.Path,
			StartMs:    entry.StartMs,
			EndMs:      entry.StartMs + entry.DurationMs,
			OffsetMs:   entry.OffsetMs,
			DurationMs: entry.DurationMs,
		}
		item.Title = entry.Title
		item.SchedulingGroup = entry.SchedulingGroup
		currentByKey[scheduleDiffKey(item)] = item
	}

	plannedByKey := map[string]scheduleEntryItem{}
	diff := schedulePreviewDiff{
		Added:   []scheduleEntryItem{},
		Removed: []scheduleEntryItem{},
	}
	for _, item := range planned {
		key := scheduleDiffKey(item)
		plannedByKey[key] = item
		if _, ok := currentByKey[key]; ok {
			diff.Unchanged++
			continue
		}
		diff.Added = append(diff.Added, item)
	}
	for key, item := range currentByKey {
		if _, ok := plannedByKey[key]; !ok {
			diff.Removed = append(diff.Removed, item)
		}
	}
	return diff
}

func scheduleDiffKey(item scheduleEntryItem) string {
	return fmt.Sprintf("%d\x00%s\x00%d\x00%d", item.StartMs, item.MediaID, item.OffsetMs, item.DurationMs)
}
