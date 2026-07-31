package admin

import (
	"context"
	"fmt"
)

type upstreamSummary struct {
	Available           bool   `json:"available"`
	StartedAt           string `json:"startedAt,omitempty"`
	CacheRoot           string `json:"cacheRoot,omitempty"`
	WorkerCount         int    `json:"workerCount,omitempty"`
	CurrentSegmentIndex int64  `json:"currentSegmentIndex,omitempty"`
	Error               string `json:"error,omitempty"`
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

// fetchUpstreamStatus adapts the playback-owned status contract to the admin
// response shape. The provider is in-process, so there is no network cache,
// retry, or stale-response state to maintain.
func (a *App) fetchUpstreamStatus(ctx context.Context) (*upstreamSummary, map[string]cacheStatus) {
	if a.playbackStatus == nil {
		return unavailablePlaybackSummary(fmt.Errorf("playback status provider is not configured")), nil
	}
	status, err := a.playbackStatus.PlaybackStatus(ctx)
	if err != nil {
		return unavailablePlaybackSummary(err), nil
	}
	cacheByChannel := make(map[string]cacheStatus, len(status.Channels))
	for _, ch := range status.Channels {
		cacheByChannel[ch.ID] = cacheStatus{
			HasSchedule: ch.HasSchedule,
		}
	}
	return &upstreamSummary{
		Available: true,
		StartedAt: status.StartedAt,
	}, cacheByChannel
}

func unavailablePlaybackSummary(err error) *upstreamSummary {
	return &upstreamSummary{Available: false, Error: err.Error()}
}
