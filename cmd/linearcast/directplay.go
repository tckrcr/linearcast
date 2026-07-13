package main

import (
	"net/http"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func (a *app) handleDirectPlay(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	ch, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if ch == nil || !ch.Enabled {
		http.NotFound(w, r)
		return
	}

	nowMs := time.Now().UTC().UnixMilli()
	entries, err := db.ScheduleWindow(r.Context(), a.dbConn, ch.ID, nowMs-scheduler.TargetSegmentMs, nowMs+lookaheadMs)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	current := db.FindScheduleEntry(entries, nowMs)
	if current == nil {
		http.NotFound(w, r)
		return
	}

	media, err := db.MediaByID(r.Context(), a.dbConn, current.MediaID)
	if err != nil || media == nil {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, media.Path)
}
