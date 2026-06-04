package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

const readyCoverageThresholdMs int64 = 3600000

func (a *app) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (a *app) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := a.dbConn.Ping(); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	channels := a.snapshotChannels()
	if len(channels) == 0 {
		http.Error(w, "no enabled channels", http.StatusServiceUnavailable)
		return
	}
	for _, rt := range channels {
		has, err := db.ChannelHasSchedule(r.Context(), a.dbConn, rt.ID)
		if err != nil {
			http.Error(w, "db error", http.StatusServiceUnavailable)
			return
		}
		if !has {
			continue
		}
		if _, err := a.packagedManifestItems(r.Context(), rt.ID, rt.RequiredPackageProfile, time.Now().UTC().UnixMilli()); err != nil {
			http.Error(w, fmt.Sprintf("channel %s packaged manifest not ready: %v", rt.ID, err), http.StatusServiceUnavailable)
			return
		}
		profile := rt.RequiredPackageProfile
		if profile == "" {
			profile = "h264-main-1080p"
		}
		coverageMs, _ := db.ChannelPackageCoverageMs(r.Context(), a.dbConn, rt.ID, profile)
		if coverageMs < readyCoverageThresholdMs {
			http.Error(w, fmt.Sprintf("channel %s package coverage low: %dms < %dms threshold", rt.ID, coverageMs, readyCoverageThresholdMs), http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ready\n"))
}

type nowCurrent struct {
	MediaID         string `json:"mediaID"`
	Title           string `json:"title,omitempty"`
	SchedulingGroup string `json:"schedulingGroup,omitempty"`
	StartMs         int64  `json:"startMs"`
	EndMs           int64  `json:"endMs"`
	ElapsedMs       int64  `json:"elapsedMs"`
	RemainingMs     int64  `json:"remainingMs"`
}

type nowNext struct {
	MediaID    string `json:"mediaID"`
	Title      string `json:"title,omitempty"`
	StartMs    int64  `json:"startMs"`
	DurationMs int64  `json:"durationMs"`
}

type nowResponse struct {
	ChannelID   string      `json:"channelID"`
	DisplayName string      `json:"displayName"`
	NowMs       int64       `json:"nowMs"`
	Status      string      `json:"status"`
	Current     *nowCurrent `json:"current"`
	Next        *nowNext    `json:"next"`
}

func (a *app) handleNow(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	row, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if row == nil || !row.Enabled {
		http.NotFound(w, r)
		return
	}

	nowMs := time.Now().UTC().UnixMilli()
	resp := nowResponse{
		ChannelID:   row.ID,
		DisplayName: row.DisplayName,
		NowMs:       nowMs,
	}

	entries, err := db.ScheduleWindow(r.Context(), a.dbConn, row.ID, nowMs-scheduler.TargetSegmentMs, nowMs+lookaheadMs)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	hasAny, err := db.ChannelHasSchedule(r.Context(), a.dbConn, row.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	current := db.FindScheduleEntry(entries, nowMs)
	if current != nil {
		media, _ := db.MediaByID(r.Context(), a.dbConn, current.MediaID)
		cur := &nowCurrent{
			MediaID:     current.MediaID,
			StartMs:     current.StartMs,
			EndMs:       current.StartMs + current.DurationMs,
			ElapsedMs:   nowMs - current.StartMs,
			RemainingMs: current.StartMs + current.DurationMs - nowMs,
		}
		if media != nil {
			cur.Title = media.Title
			cur.SchedulingGroup = media.SchedulingGroup
		}
		resp.Current = cur
		resp.Status = "playing"
	} else if !hasAny {
		resp.Status = "unscheduled"
	} else {
		resp.Status = "gap"
	}

	next, err := db.NextScheduleEntryAfter(r.Context(), a.dbConn, row.ID, nowMs)
	if err == nil && next != nil {
		nxt := &nowNext{
			MediaID:    next.MediaID,
			StartMs:    next.StartMs,
			DurationMs: next.DurationMs,
		}
		if media, _ := db.MediaByID(r.Context(), a.dbConn, next.MediaID); media != nil {
			nxt.Title = media.Title
		}
		resp.Next = nxt
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(resp)
}

type channelStatus struct {
	ID                     string `json:"id"`
	DisplayName            string `json:"displayName"`
	PlaybackMode           string `json:"playbackMode"`
	RequiredPackageProfile string `json:"requiredPackageProfile"`
	HasSchedule            bool   `json:"hasSchedule"`
	PackageReady           bool   `json:"packageReady"`
	PackageError           string `json:"packageError,omitempty"`
	CurrentMediaID         string `json:"currentMediaID,omitempty"`
	CurrentMediaTitle      string `json:"currentMediaTitle,omitempty"`
}

type statusResponse struct {
	NowMs     int64           `json:"nowMs"`
	StartedAt string          `json:"startedAt"`
	Channels  []channelStatus `json:"channels"`
}

func (a *app) handleStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	nowMs := now.UnixMilli()

	resp := statusResponse{
		NowMs:     nowMs,
		StartedAt: a.startedAt.Format(time.RFC3339Nano),
	}

	channels := a.snapshotChannels()
	for _, rt := range channels {
		cs := channelStatus{
			ID:                     rt.ID,
			DisplayName:            rt.DisplayName,
			PlaybackMode:           string(rt.PlaybackMode),
			RequiredPackageProfile: rt.RequiredPackageProfile,
		}

		has, _ := db.ChannelHasSchedule(r.Context(), a.dbConn, rt.ID)
		cs.HasSchedule = has
		if has {
			if _, err := a.packagedManifestItems(r.Context(), rt.ID, rt.RequiredPackageProfile, nowMs); err != nil {
				cs.PackageError = err.Error()
			} else {
				cs.PackageReady = true
			}
		}

		if entries, err := db.ScheduleWindow(r.Context(), a.dbConn, rt.ID, nowMs-scheduler.TargetSegmentMs, nowMs+scheduler.TargetSegmentMs); err == nil {
			if cur := db.FindScheduleEntry(entries, nowMs); cur != nil {
				cs.CurrentMediaID = cur.MediaID
				if m, err := db.MediaByID(r.Context(), a.dbConn, cur.MediaID); err == nil && m != nil {
					cs.CurrentMediaTitle = m.Title
				}
			}
		}
		resp.Channels = append(resp.Channels, cs)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(resp)
}
