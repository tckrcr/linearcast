package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/routes"
	"github.com/tckrcr/linearcast/internal/scheduler"
	"github.com/tckrcr/linearcast/internal/sysinfo"
)

// CheckPlaybackReadiness verifies that enabled playback channels can serve
// their current schedules under their configured prefill mode.
func (runtime *Runtime) CheckPlaybackReadiness(ctx context.Context) error {
	if err := runtime.dbConn.PingContext(ctx); err != nil {
		return fmt.Errorf("db unreachable")
	}
	channels := runtime.snapshotChannels()
	if len(channels) == 0 {
		return fmt.Errorf("no enabled channels")
	}
	for _, rt := range channels {
		has, err := db.ChannelHasSchedule(ctx, runtime.dbConn, rt.ID)
		if err != nil {
			return fmt.Errorf("db error")
		}
		if !has {
			continue
		}
		if rt.PrefillMode == "on_demand" {
			continue
		}
		if _, err := runtime.packagedManifestItems(ctx, rt.ID, rt.RequiredPackageProfile, time.Now().UTC().UnixMilli()); err != nil {
			return fmt.Errorf("channel %s packaged manifest not ready: %v", rt.ID, err)
		}
	}
	return nil
}

type nowCurrent struct {
	MediaID        string `json:"mediaID"`
	Title          string `json:"title,omitempty"`
	CollectionName string `json:"collectionName,omitempty"`
	StartMs        int64  `json:"startMs"`
	EndMs          int64  `json:"endMs"`
	ElapsedMs      int64  `json:"elapsedMs"`
	RemainingMs    int64  `json:"remainingMs"`
	DirectPlayURL  string `json:"directPlayURL"`
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

func (runtime *Runtime) handleNow(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	row, err := db.ChannelByID(r.Context(), runtime.dbConn, channelID)
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

	entries, err := db.ScheduleWindow(r.Context(), runtime.dbConn, row.ID, nowMs-scheduler.TargetSegmentMs, nowMs+lookaheadMs)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	hasAny, err := db.ChannelHasSchedule(r.Context(), runtime.dbConn, row.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	current := db.FindScheduleEntry(entries, nowMs)
	if current != nil {
		media, _ := db.MediaByID(r.Context(), runtime.dbConn, current.MediaID)
		cur := &nowCurrent{
			MediaID:       current.MediaID,
			StartMs:       current.StartMs,
			EndMs:         current.StartMs + current.DurationMs,
			ElapsedMs:     nowMs - current.StartMs,
			RemainingMs:   current.StartMs + current.DurationMs - nowMs,
			DirectPlayURL: directPlayURL(r, channelID),
		}
		if media != nil {
			cur.Title = media.Title
			cur.CollectionName = media.CollectionName
		}
		resp.Current = cur
		resp.Status = "playing"
	} else if !hasAny {
		resp.Status = "unscheduled"
	} else {
		resp.Status = "gap"
	}

	next, err := db.NextScheduleEntryAfter(r.Context(), runtime.dbConn, row.ID, nowMs)
	if err == nil && next != nil {
		nxt := &nowNext{
			MediaID:    next.MediaID,
			StartMs:    next.StartMs,
			DurationMs: next.DurationMs,
		}
		if media, _ := db.MediaByID(r.Context(), runtime.dbConn, next.MediaID); media != nil {
			nxt.Title = media.Title
		}
		resp.Next = nxt
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(resp)
}

func directPlayURL(r *http.Request, channelID string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + routes.DirectPlay(channelID)
}

// PlaybackStatus implements StatusProvider for the composed admin module and
// is also the source for the public /status response.
func (runtime *Runtime) PlaybackStatus(ctx context.Context) (Status, error) {
	now := time.Now().UTC()
	nowMs := now.UnixMilli()

	resp := Status{
		NowMs:     nowMs,
		StartedAt: runtime.startedAt.Format(time.RFC3339Nano),
	}

	channels := runtime.snapshotChannels()
	for _, rt := range channels {
		cs := ChannelStatus{
			ID:                     rt.ID,
			DisplayName:            rt.DisplayName,
			PrefillMode:            rt.PrefillMode,
			RequiredPackageProfile: rt.RequiredPackageProfile,
		}

		has, err := db.ChannelHasSchedule(ctx, runtime.dbConn, rt.ID)
		if err != nil {
			return Status{}, err
		}
		cs.HasSchedule = has
		if has {
			if _, err := runtime.packagedManifestItems(ctx, rt.ID, rt.RequiredPackageProfile, nowMs); err != nil {
				cs.PackageError = err.Error()
			} else {
				cs.PackageReady = true
			}
		}

		if entries, err := db.ScheduleWindow(ctx, runtime.dbConn, rt.ID, nowMs-scheduler.TargetSegmentMs, nowMs+scheduler.TargetSegmentMs); err == nil {
			if cur := db.FindScheduleEntry(entries, nowMs); cur != nil {
				cs.CurrentMediaID = cur.MediaID
				if m, err := db.MediaByID(ctx, runtime.dbConn, cur.MediaID); err == nil && m != nil {
					cs.CurrentMediaTitle = m.Title
				}
			}
		}
		resp.Channels = append(resp.Channels, cs)
	}
	return resp, nil
}

// DegradedSignals implements DegradedReader. It evaluates the same
// thresholds the scrape owner uses, so the admin recovery view and /metrics
// report the same state.
func (runtime *Runtime) DegradedSignals(ctx context.Context) ([]DegradedSignal, error) {
	var out []DegradedSignal

	cacheRoot := runtime.cache.Root()
	if cacheRoot != "" {
		free := sysinfo.DiskFreeGB(cacheRoot)
		if free < metrics.DefaultDiskFreeThresholdGB {
			out = append(out, DegradedSignal{
				Signal:   "disk_pressure",
				Degraded: true,
				Detail:   fmt.Sprintf("%.1f GB free on cache filesystem (threshold %d GB)", free, metrics.DefaultDiskFreeThresholdGB),
			})
		} else {
			out = append(out, DegradedSignal{
				Signal:   "disk_pressure",
				Degraded: false,
				Detail:   fmt.Sprintf("%.1f GB free on cache filesystem", free),
			})
		}
	}

	active := runtime.encodings.ActiveCount()
	maxC := runtime.encodings.MaxConcurrent()
	if maxC > 0 && float64(active)/float64(maxC) >= metrics.DefaultCapacityUtilization {
		out = append(out, DegradedSignal{
			Signal:   "at_capacity",
			Degraded: true,
			Detail:   fmt.Sprintf("%d/%d on-demand encodings active (at capacity)", active, maxC),
		})
	} else {
		out = append(out, DegradedSignal{
			Signal:   "at_capacity",
			Degraded: false,
			Detail:   fmt.Sprintf("%d/%d on-demand encodings active", active, maxC),
		})
	}

	return out, nil
}
