package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

type upstreamSummary struct {
	Available           bool   `json:"available"`
	StartedAt           string `json:"startedAt,omitempty"`
	CacheRoot           string `json:"cacheRoot,omitempty"`
	WorkerCount         int    `json:"workerCount,omitempty"`
	CurrentSegmentIndex int64  `json:"currentSegmentIndex,omitempty"`
	Error               string `json:"error,omitempty"`
}

type upstreamStatus struct {
	CurrentSegmentIndex int64                   `json:"currentSegmentIndex"`
	StartedAt           string                  `json:"startedAt"`
	CacheRoot           string                  `json:"cacheRoot"`
	WorkerCount         int                     `json:"workerCount"`
	Channels            []upstreamChannelStatus `json:"channels"`
}

type upstreamChannelStatus struct {
	ID                     string  `json:"id"`
	Format                 string  `json:"format"`
	HasSchedule            bool    `json:"hasSchedule"`
	CacheSize              int     `json:"cacheSize"`
	CacheMinIndex          *int64  `json:"cacheMinIndex,omitempty"`
	CacheMaxIndex          *int64  `json:"cacheMaxIndex,omitempty"`
	LookaheadDepthSegments *int64  `json:"lookaheadDepthSegments,omitempty"`
	LatestGeneratedIndex   int64   `json:"latestGeneratedIndex"`
	LatestGeneratedSeconds float64 `json:"latestGeneratedSeconds"`
	LatestGeneratedAt      string  `json:"latestGeneratedAt,omitempty"`
}

type cacheStatus struct {
	Format                 string  `json:"format,omitempty"`
	HasSchedule            bool    `json:"hasSchedule"`
	CacheSize              int     `json:"cacheSize"`
	CacheMinIndex          *int64  `json:"cacheMinIndex,omitempty"`
	CacheMaxIndex          *int64  `json:"cacheMaxIndex,omitempty"`
	LookaheadDepthSegments *int64  `json:"lookaheadDepthSegments,omitempty"`
	LookaheadDepthSeconds  *int64  `json:"lookaheadDepthSeconds,omitempty"`
	LatestGeneratedIndex   int64   `json:"latestGeneratedIndex,omitempty"`
	LatestGeneratedSeconds float64 `json:"latestGeneratedSeconds,omitempty"`
	LatestGeneratedAt      string  `json:"latestGeneratedAt,omitempty"`
}

const (
	upstreamStatusSuccessTTL     = 5 * time.Second
	upstreamStatusFailureBase    = 2 * time.Second
	upstreamStatusFailureMax     = 60 * time.Second
	upstreamStatusFailureLogEach = 60 * time.Second
)

type upstreamStatusCache struct {
	mu sync.Mutex

	summary        *upstreamSummary
	cacheByChannel map[string]cacheStatus
	fetchedAt      time.Time
	nextAttempt    time.Time
	failureStreak  int
	lastError      string
	lastLogAt      time.Time
	suppressedLogs int
}

func newUpstreamStatusCache() *upstreamStatusCache {
	return &upstreamStatusCache{}
}

func (a *App) fetchUpstreamStatus(ctx context.Context) (*upstreamSummary, map[string]cacheStatus) {
	now := a.now().UTC()
	cache := a.upstreamCache
	if cache == nil {
		cache = newUpstreamStatusCache()
		a.upstreamCache = cache
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.summary != nil && cache.summary.Available && now.Sub(cache.fetchedAt) < upstreamStatusSuccessTTL {
		return cloneUpstreamSummary(cache.summary), cloneCacheStatusMap(cache.cacheByChannel)
	}
	if now.Before(cache.nextAttempt) {
		return cache.unavailableSnapshot()
	}

	summary, cacheByChannel, err := a.fetchUpstreamStatusOnce(ctx)
	if err != nil {
		cache.recordFailure(now, err.Error())
		cache.logFailure(a.logger, now)
		return cache.unavailableSnapshot()
	}

	cache.summary = summary
	cache.cacheByChannel = cloneCacheStatusMap(cacheByChannel)
	cache.fetchedAt = now
	cache.nextAttempt = time.Time{}
	cache.failureStreak = 0
	cache.lastError = ""
	cache.suppressedLogs = 0
	return cloneUpstreamSummary(summary), cloneCacheStatusMap(cacheByChannel)
}

func (a *App) fetchUpstreamStatusOnce(ctx context.Context) (*upstreamSummary, map[string]cacheStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.upstreamURL+"/status", nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var status upstreamStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, nil, err
	}
	cacheByChannel := map[string]cacheStatus{}
	for _, ch := range status.Channels {
		cache := cacheStatus{
			Format:                 ch.Format,
			HasSchedule:            ch.HasSchedule,
			CacheSize:              ch.CacheSize,
			CacheMinIndex:          ch.CacheMinIndex,
			CacheMaxIndex:          ch.CacheMaxIndex,
			LookaheadDepthSegments: ch.LookaheadDepthSegments,
			LatestGeneratedIndex:   ch.LatestGeneratedIndex,
			LatestGeneratedSeconds: ch.LatestGeneratedSeconds,
			LatestGeneratedAt:      ch.LatestGeneratedAt,
		}
		if ch.LookaheadDepthSegments != nil {
			seconds := *ch.LookaheadDepthSegments * (db.ScheduleGridMs / 1000)
			cache.LookaheadDepthSeconds = &seconds
		}
		cacheByChannel[ch.ID] = cache
	}
	return &upstreamSummary{
		Available:           true,
		StartedAt:           status.StartedAt,
		CacheRoot:           status.CacheRoot,
		WorkerCount:         status.WorkerCount,
		CurrentSegmentIndex: status.CurrentSegmentIndex,
	}, cacheByChannel, nil
}

func (c *upstreamStatusCache) recordFailure(now time.Time, err string) {
	c.failureStreak++
	delay := upstreamStatusFailureBase
	for i := 1; i < c.failureStreak && delay < upstreamStatusFailureMax; i++ {
		delay *= 2
	}
	if delay > upstreamStatusFailureMax {
		delay = upstreamStatusFailureMax
	}
	c.nextAttempt = now.Add(delay)
	c.lastError = err
}

func (c *upstreamStatusCache) logFailure(logger *slog.Logger, now time.Time) {
	if logger == nil {
		return
	}
	if c.lastLogAt.IsZero() || now.Sub(c.lastLogAt) >= upstreamStatusFailureLogEach {
		if c.suppressedLogs > 0 {
			logger.Warn("upstream status unavailable",
				"err", c.lastError,
				"next_retry", c.nextAttempt.Format(time.RFC3339),
				"suppressed", c.suppressedLogs,
			)
		} else {
			logger.Warn("upstream status unavailable",
				"err", c.lastError,
				"next_retry", c.nextAttempt.Format(time.RFC3339),
			)
		}
		c.lastLogAt = now
		c.suppressedLogs = 0
		return
	}
	c.suppressedLogs++
}

func (c *upstreamStatusCache) unavailableSnapshot() (*upstreamSummary, map[string]cacheStatus) {
	return &upstreamSummary{
		Available: false,
		Error:     c.lastError,
	}, cloneCacheStatusMap(c.cacheByChannel)
}

func cloneUpstreamSummary(in *upstreamSummary) *upstreamSummary {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneCacheStatusMap(in map[string]cacheStatus) map[string]cacheStatus {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]cacheStatus, len(in))
	for k, v := range in {
		out[k] = cloneCacheStatus(v)
	}
	return out
}

func cloneCacheStatus(in cacheStatus) cacheStatus {
	out := in
	if in.CacheMinIndex != nil {
		v := *in.CacheMinIndex
		out.CacheMinIndex = &v
	}
	if in.CacheMaxIndex != nil {
		v := *in.CacheMaxIndex
		out.CacheMaxIndex = &v
	}
	if in.LookaheadDepthSegments != nil {
		v := *in.LookaheadDepthSegments
		out.LookaheadDepthSegments = &v
	}
	if in.LookaheadDepthSeconds != nil {
		v := *in.LookaheadDepthSeconds
		out.LookaheadDepthSeconds = &v
	}
	return out
}
