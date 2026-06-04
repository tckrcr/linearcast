package admin

import (
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

type playHistoryResponse struct {
	ChannelID string                `json:"channelId"`
	SinceMs   int64                 `json:"sinceMs"`
	Count     int                   `json:"count"`
	Entries   []playHistoryAPIEntry `json:"entries"`
}

type playHistoryAPIEntry struct {
	ID              int64  `json:"id"`
	ScheduleEntryID string `json:"scheduleEntryId"`
	MediaID         string `json:"mediaId"`
	MediaTitle      string `json:"mediaTitle,omitempty"`
	MediaPath       string `json:"mediaPath,omitempty"`
	StartedAtMs     int64  `json:"startedAtMs"`
	EndedAtMs       int64  `json:"endedAtMs"`
	DurationMs      int64  `json:"durationMs"`
}

func (a *App) handleChannelHistory(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	sinceMs, ok := parseQueryUnixMs(w, r, "since")
	if !ok {
		return
	}
	existing, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	rows, err := db.PlayHistorySince(r.Context(), a.dbConn, channelID, sinceMs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	entries := make([]playHistoryAPIEntry, 0, len(rows))
	for _, row := range rows {
		entry := playHistoryAPIEntry{
			ID:              row.ID,
			ScheduleEntryID: row.ScheduleEntryID,
			MediaID:         row.MediaID,
			StartedAtMs:     row.StartedAtMs,
			EndedAtMs:       row.EndedAtMs,
			DurationMs:      row.DurationMs,
		}
		if row.MediaTitle != "" {
			entry.MediaTitle = row.MediaTitle
		}
		if row.MediaPath != "" {
			entry.MediaPath = row.MediaPath
		}
		entries = append(entries, entry)
	}
	writeJSON(w, playHistoryResponse{
		ChannelID: channelID,
		SinceMs:   sinceMs,
		Count:     len(entries),
		Entries:   entries,
	})
}
