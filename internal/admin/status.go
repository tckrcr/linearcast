package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

type statusResponse struct {
	NowMs       int64            `json:"nowMs"`
	Upstream    *upstreamSummary `json:"upstream,omitempty"`
	Channels    []channelNow     `json:"channels"`
	GeneratedAt string           `json:"generatedAt"`
}

type channelNow struct {
	ID                    string              `json:"id"`
	DisplayName           string              `json:"displayName"`
	Enabled               bool                `json:"enabled"`
	HiddenFromGuide       bool                `json:"hiddenFromGuide"`
	ArtworkURL            string              `json:"artworkUrl,omitempty"`
	Ordering              string              `json:"ordering"`
	MediaKind             string              `json:"mediaKind"`
	Status                string              `json:"status"`
	Current               *mediaWindow        `json:"current"`
	Next                  *mediaWindow        `json:"next"`
	ScheduleCoverageMs    int64               `json:"scheduleCoverageMs"`
	ScheduleCoverageHours float64             `json:"scheduleCoverageHours"`
	ScheduleEndMs         *int64              `json:"scheduleEndMs,omitempty"`
	PackageCoverageMs     int64               `json:"packageCoverageMs"`
	PackageCoverageHours  float64             `json:"packageCoverageHours"`
	PackageReadyCount     int                 `json:"packageReadyCount"`
	PackageProfile        string              `json:"packageProfile"`
	IsExternal            bool                `json:"isExternal,omitempty"`
	UpstreamHLSURL        string              `json:"upstreamHlsUrl,omitempty"`
	NowPlaying            *externalNowPlaying `json:"nowPlaying,omitempty"`
	Cache                 *cacheStatus        `json:"cache,omitempty"`
}

type mediaWindow struct {
	MediaID         string `json:"mediaID"`
	Title           string `json:"title,omitempty"`
	Path            string `json:"path,omitempty"`
	SchedulingGroup string `json:"schedulingGroup,omitempty"`
	PackageStatus   string `json:"packageStatus,omitempty"`
	PackageError    string `json:"packageError,omitempty"`
	StartMs         int64  `json:"startMs"`
	EndMs           int64  `json:"endMs"`
	DurationMs      int64  `json:"durationMs"`
	ElapsedMs       int64  `json:"elapsedMs,omitempty"`
	RemainingMs     int64  `json:"remainingMs,omitempty"`
}

type playingResponse struct {
	NowMs    int64            `json:"nowMs"`
	Channels []channelPlaying `json:"channels"`
}

type channelPlaying struct {
	ID             string `json:"id"`
	DisplayName    string `json:"displayName"`
	Status         string `json:"status"`
	MediaID        string `json:"mediaID,omitempty"`
	Title          string `json:"title,omitempty"`
	StartedAtMs    int64  `json:"startedAtMs,omitempty"`
	EndsAtMs       int64  `json:"endsAtMs,omitempty"`
	ElapsedMs      int64  `json:"elapsedMs,omitempty"`
	RemainingMs    int64  `json:"remainingMs,omitempty"`
	NextMediaID    string `json:"nextMediaID,omitempty"`
	NextTitle      string `json:"nextTitle,omitempty"`
	NextStartsAtMs int64  `json:"nextStartsAtMs,omitempty"`
}

type queueDepthResponse struct {
	NowMs    int64               `json:"nowMs"`
	Channels []channelQueueDepth `json:"channels"`
}

type channelQueueDepth struct {
	ID                     string  `json:"id"`
	DisplayName            string  `json:"displayName"`
	Status                 string  `json:"status"`
	ScheduleCoverageMs     int64   `json:"scheduleCoverageMs"`
	ScheduleCoverageHours  float64 `json:"scheduleCoverageHours"`
	ScheduleEndMs          *int64  `json:"scheduleEndMs,omitempty"`
	CacheSize              int     `json:"cacheSize,omitempty"`
	CacheLookaheadSegments *int64  `json:"cacheLookaheadSegments,omitempty"`
	CacheLookaheadSeconds  *int64  `json:"cacheLookaheadSeconds,omitempty"`
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	nowMs := a.now().UTC().UnixMilli()
	upstream, cacheByChannel := a.fetchUpstreamStatus(r.Context())
	channels, err := a.channelNowList(r.Context(), nowMs, cacheByChannel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, statusResponse{
		NowMs:       nowMs,
		Upstream:    upstream,
		Channels:    channels,
		GeneratedAt: a.now().UTC().Format(time.RFC3339Nano),
	})
}

func (a *App) handleNow(w http.ResponseWriter, r *http.Request) {
	nowMs := a.now().UTC().UnixMilli()
	_, cacheByChannel := a.fetchUpstreamStatus(r.Context())
	channels, err := a.channelNowList(r.Context(), nowMs, cacheByChannel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"nowMs":    nowMs,
		"channels": channels,
	})
}

func (a *App) handlePlaying(w http.ResponseWriter, r *http.Request) {
	nowMs := a.now().UTC().UnixMilli()
	_, cacheByChannel := a.fetchUpstreamStatus(r.Context())
	channels, err := a.channelNowList(r.Context(), nowMs, cacheByChannel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]channelPlaying, 0, len(channels))
	for _, ch := range channels {
		row := channelPlaying{
			ID:          ch.ID,
			DisplayName: ch.DisplayName,
			Status:      ch.Status,
		}
		if ch.Current != nil {
			row.MediaID = ch.Current.MediaID
			row.Title = ch.Current.Title
			row.StartedAtMs = ch.Current.StartMs
			row.EndsAtMs = ch.Current.EndMs
			row.ElapsedMs = ch.Current.ElapsedMs
			row.RemainingMs = ch.Current.RemainingMs
		}
		if ch.Next != nil {
			row.NextMediaID = ch.Next.MediaID
			row.NextTitle = ch.Next.Title
			row.NextStartsAtMs = ch.Next.StartMs
		}
		out = append(out, row)
	}
	writeJSON(w, playingResponse{NowMs: nowMs, Channels: out})
}

func (a *App) handleQueueDepth(w http.ResponseWriter, r *http.Request) {
	nowMs := a.now().UTC().UnixMilli()
	_, cacheByChannel := a.fetchUpstreamStatus(r.Context())
	channels, err := a.channelNowList(r.Context(), nowMs, cacheByChannel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]channelQueueDepth, 0, len(channels))
	for _, ch := range channels {
		row := channelQueueDepth{
			ID:                    ch.ID,
			DisplayName:           ch.DisplayName,
			Status:                ch.Status,
			ScheduleCoverageMs:    ch.ScheduleCoverageMs,
			ScheduleCoverageHours: ch.ScheduleCoverageHours,
			ScheduleEndMs:         ch.ScheduleEndMs,
		}
		if ch.Cache != nil {
			row.CacheSize = ch.Cache.CacheSize
			row.CacheLookaheadSegments = ch.Cache.LookaheadDepthSegments
			row.CacheLookaheadSeconds = ch.Cache.LookaheadDepthSeconds
		}
		out = append(out, row)
	}
	writeJSON(w, queueDepthResponse{NowMs: nowMs, Channels: out})
}

func (a *App) handleChannelNow(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	nowMs := a.now().UTC().UnixMilli()
	_, cacheByChannel := a.fetchUpstreamStatus(r.Context())
	ch, err := a.channelNowByID(r.Context(), nowMs, channelID, cacheByChannel[channelID])
	if err != nil {
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, ch)
}

func (a *App) channelNowList(ctx context.Context, nowMs int64, cacheByChannel map[string]cacheStatus) ([]channelNow, error) {
	channels, err := db.EnabledChannels(ctx, a.dbConn)
	if err != nil {
		return nil, err
	}
	out := make([]channelNow, 0, len(channels))
	for _, ch := range channels {
		row, err := a.channelNowForRow(ctx, nowMs, ch, cacheByChannel[ch.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (a *App) channelNowByID(ctx context.Context, nowMs int64, channelID string, cache cacheStatus) (channelNow, error) {
	ch, err := db.ChannelByID(ctx, a.dbConn, channelID)
	if err != nil {
		return channelNow{}, err
	}
	if ch == nil {
		return channelNow{}, errNotFound
	}
	return a.channelNowForRow(ctx, nowMs, *ch, cache)
}

func (a *App) channelNowForRow(ctx context.Context, nowMs int64, ch db.Channel, cache cacheStatus) (channelNow, error) {
	resp := channelNow{
		ID:              ch.ID,
		DisplayName:     ch.DisplayName,
		Enabled:         ch.Enabled,
		HiddenFromGuide: ch.HiddenFromGuide,
		ArtworkURL:      ch.ArtworkURL,
		Ordering:        ch.Ordering,
		MediaKind:       string(db.NormalizeMediaKind(ch.MediaKind)),
		Status:          "unknown",
	}
	if cache.Format != "" || cache.CacheSize > 0 || cache.LookaheadDepthSegments != nil {
		c := cache
		resp.Cache = &c
	}
	if ch.UpstreamHLSURL != nil {
		nowPlaying, err := a.fetchExternalNowPlaying(ctx, ch)
		if err == nil {
			resp.NowPlaying = nowPlaying
			resp.ArtworkURL = artworkForExternalChannel(ch, nowPlaying)
		}
		resp.IsExternal = true
		resp.UpstreamHLSURL = *ch.UpstreamHLSURL
		resp.Status = "live"
		return resp, nil
	}
	profile := ch.RequiredPackageProfile
	if profile == "" {
		profile = db.DefaultPackageProfile
	}
	resp.PackageProfile = profile

	hasAny, err := db.ChannelHasSchedule(ctx, a.dbConn, ch.ID)
	if err != nil {
		return resp, err
	}
	entries, err := db.ScheduleWindow(ctx, a.dbConn, ch.ID, nowMs-segmentMs, nowMs+24*60*60*1000)
	if err != nil {
		return resp, err
	}
	if cur := db.FindScheduleEntry(entries, nowMs); cur != nil {
		window, err := a.buildMediaWindow(ctx, cur, nowMs, profile)
		if err != nil {
			return resp, err
		}
		resp.Current = window
		resp.Status = "playing"
	} else if !hasAny {
		resp.Status = "unscheduled"
	} else {
		resp.Status = "gap"
	}

	next, err := db.NextScheduleEntryAfter(ctx, a.dbConn, ch.ID, nowMs)
	if err != nil {
		return resp, err
	}
	if next != nil {
		window, err := a.buildMediaWindow(ctx, next, nowMs, profile)
		if err != nil {
			return resp, err
		}
		resp.Next = window
	}

	last, err := db.LastScheduleEntry(ctx, a.dbConn, ch.ID)
	if err != nil {
		return resp, err
	}
	if last != nil {
		endMs := last.StartMs + last.DurationMs
		resp.ScheduleEndMs = &endMs
		resp.ScheduleCoverageMs = endMs - nowMs
		if resp.ScheduleCoverageMs < 0 {
			resp.ScheduleCoverageMs = 0
		}
		resp.ScheduleCoverageHours = float64(resp.ScheduleCoverageMs) / float64(time.Hour/time.Millisecond)
	}

	pkgCov, err := db.ChannelPackageCoverageMs(ctx, a.dbConn, ch.ID, profile)
	if err != nil {
		return resp, err
	}
	resp.PackageCoverageMs = pkgCov
	resp.PackageCoverageHours = float64(pkgCov) / float64(time.Hour/time.Millisecond)

	pkgCount, err := db.ChannelPackageReadyCount(ctx, a.dbConn, ch.ID, profile)
	if err != nil {
		return resp, err
	}
	resp.PackageReadyCount = pkgCount

	return resp, nil
}

func (a *App) buildMediaWindow(ctx context.Context, entry *db.ScheduleEntry, nowMs int64, profile string) (*mediaWindow, error) {
	w := &mediaWindow{
		MediaID:    entry.MediaID,
		StartMs:    entry.StartMs,
		EndMs:      entry.StartMs + entry.DurationMs,
		DurationMs: entry.DurationMs,
	}
	if entry.StartMs <= nowMs && nowMs < entry.StartMs+entry.DurationMs {
		w.ElapsedMs = nowMs - entry.StartMs
		w.RemainingMs = entry.StartMs + entry.DurationMs - nowMs
	}
	media, err := db.MediaByID(ctx, a.dbConn, entry.MediaID)
	if err != nil {
		return nil, err
	}
	if media != nil {
		w.Path = media.Path
		w.Title = media.Title
		w.SchedulingGroup = media.SchedulingGroup
	}
	if profile != "" {
		pkgs, err := db.MediaPackagesForMedia(ctx, a.dbConn, entry.MediaID)
		if err != nil {
			return nil, err
		}
		for _, pkg := range pkgs {
			if pkg.RenditionProfile != profile {
				continue
			}
			w.PackageStatus = string(pkg.Status)
			if pkg.Error != nil {
				w.PackageError = *pkg.Error
			}
			break
		}
	}
	return w, nil
}
