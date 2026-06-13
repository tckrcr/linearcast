package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/metrics"
)

type ServiceOptions struct {
	HorizonHours         int
	LowWaterHours        int
	RequireReadyPackages bool
	RenditionProfile     string
	ClearAfterMs         sql.NullInt64
	NowMs                int64
	InTransaction        bool
	// BootstrapRequireAllReady delays first schedule creation until every
	// codec-passing channel_media item has a ready package for the channel's
	// active rendition profile. Existing schedules still use normal low-water
	// tail extension.
	BootstrapRequireAllReady bool
	// ResumeAfterMediaID, if set, overrides the last-scheduled media ID used to
	// position the starting cursor in BuildEntries. Use this when the caller has
	// already removed an entry and wants the extend to skip that media item and
	// continue with the one after it.
	ResumeAfterMediaID string
	ScheduleMode       string
	SlotDurationMs     int64
}

type ExtendResult struct {
	ChannelID            string
	DisplayName          string
	Ordering             string
	Inserted             int
	ExistingEndMs        int64
	LastEndMs            int64
	RemainingMs          int64
	Cleared              int64
	SkippedLowWater      bool
	BootstrapDelayed     bool
	BootstrapReady       int64
	BootstrapTotal       int64
	RequireReadyPackages bool
	RenditionProfile     string
	ScheduleMode         string
	SlotDurationMs       int64
	Error                string
}

type ExtendAllResult struct {
	Channels []ExtendResult
}

// ExtendChannel is the scheduling service entrypoint shared by the one-shot
// CLI and the extender daemon. It owns channel loading, optional schedule
// clearing, packaged eligibility decisions, tail continuation, and insertion.
func ExtendChannel(ctx context.Context, conn db.Execer, channelID string, opts ServiceOptions) (ExtendResult, error) {
	if err := ctx.Err(); err != nil {
		return ExtendResult{}, err
	}
	ch, err := db.ChannelByID(ctx, conn, channelID)
	if err != nil {
		return ExtendResult{}, fmt.Errorf("lookup channel: %w", err)
	}
	if ch == nil {
		return ExtendResult{}, fmt.Errorf("channel %q not found", channelID)
	}

	nowMs := opts.NowMs
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
	}

	// External HLS channels proxy a live stream and have no packaged media to
	// schedule. Skip silently — the extender should not touch them.
	if ch.UpstreamHLSURL != nil {
		return ExtendResult{
			ChannelID:       ch.ID,
			DisplayName:     ch.DisplayName,
			Ordering:        ch.Ordering,
			SkippedLowWater: true,
		}, nil
	}

	var cleared int64
	if opts.ClearAfterMs.Valid {
		cleared, err = db.ClearScheduleAfter(ctx, conn, channelID, opts.ClearAfterMs.Int64)
		if err != nil {
			return ExtendResult{}, fmt.Errorf("clear schedule: %w", err)
		}
	}

	last, err := db.LastScheduleEntry(ctx, conn, channelID)
	if err != nil {
		return ExtendResult{}, fmt.Errorf("last schedule entry: %w", err)
	}
	var existingEndMs int64
	if last != nil {
		existingEndMs = last.StartMs + last.DurationMs
	}
	remainingMs := existingEndMs - nowMs
	if remainingMs < 0 {
		remainingMs = 0
	}

	result := ExtendResult{
		ChannelID:     ch.ID,
		DisplayName:   ch.DisplayName,
		Ordering:      ch.Ordering,
		ExistingEndMs: existingEndMs,
		LastEndMs:     existingEndMs,
		RemainingMs:   remainingMs,
		Cleared:       cleared,
	}

	base := Options{
		RequireReadyPackages: opts.RequireReadyPackages,
		RenditionProfile:     opts.RenditionProfile,
		InTransaction:        opts.InTransaction,
		ResumeAfterMediaID:   opts.ResumeAfterMediaID,
		ScheduleMode:         opts.ScheduleMode,
		SlotDurationMs:       opts.SlotDurationMs,
	}
	effective := OptionsForChannel(*ch, base)
	result.RequireReadyPackages = effective.RequireReadyPackages
	result.RenditionProfile = effective.RenditionProfile
	result.ScheduleMode = effective.ScheduleMode
	result.SlotDurationMs = effective.SlotDurationMs

	if opts.BootstrapRequireAllReady && last == nil && effective.RequireReadyPackages {
		readiness, rerr := db.ChannelProfileReadiness(ctx, conn, channelID, effective.RenditionProfile)
		if rerr != nil {
			return result, fmt.Errorf("channel package readiness: %w", rerr)
		}
		result.BootstrapReady = readiness.Ready
		result.BootstrapTotal = readiness.Total
		if readiness.Total > 0 && readiness.Ready < readiness.Total {
			result.BootstrapDelayed = true
			return result, nil
		}
	}

	if opts.LowWaterHours > 0 && last != nil {
		lowWaterEnd := nowMs + int64(opts.LowWaterHours)*3600*1000
		if existingEndMs >= lowWaterEnd {
			result.SkippedLowWater = true
			return result, nil
		}
	}

	inserted, lastEnd, err := extendChannelTail(ctx, conn, ch.ID, ch.Ordering, opts.HorizonHours, nowMs, effective)
	if err != nil {
		return result, err
	}
	result.Inserted = inserted
	result.LastEndMs = lastEnd
	return result, nil
}

func ExtendAllEnabled(ctx context.Context, conn *sql.DB, opts ServiceOptions) (ExtendAllResult, error) {
	channels, err := db.EnabledChannels(ctx, conn)
	if err != nil {
		return ExtendAllResult{}, fmt.Errorf("list channels: %w", err)
	}
	out := ExtendAllResult{Channels: make([]ExtendResult, 0, len(channels))}
	for _, ch := range channels {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := ExtendChannel(ctx, conn, ch.ID, opts)
		if err != nil {
			out.Channels = append(out.Channels, ExtendResult{
				ChannelID:   ch.ID,
				DisplayName: ch.DisplayName,
				Ordering:    ch.Ordering,
				Error:       err.Error(),
			})
			continue
		}
		out.Channels = append(out.Channels, res)
		RecordChannelMetrics(ctx, conn, ch.ID, res.RenditionProfile)
	}
	return out, nil
}

func RecordChannelMetrics(ctx context.Context, conn db.Execer, channelID, renditionProfile string) {
	pkgMs, _ := db.ChannelPackageCoverageMs(ctx, conn, channelID, renditionProfile)
	metrics.PackageReadyDurationMs.WithLabelValues(channelID, renditionProfile).Set(float64(pkgMs))

	nowMs := time.Now().UTC().UnixMilli()
	gaps, _ := db.ScheduleGaps(ctx, conn, channelID, nowMs, nowMs+int64(48*3600*1000))
	metrics.ScheduleGapCount.WithLabelValues(channelID).Set(float64(len(gaps)))
	active := 0
	for _, gap := range gaps {
		if gap.StartMs <= nowMs && nowMs < gap.EndMs {
			active = 1
			break
		}
	}
	metrics.ScheduleGapActive.WithLabelValues(channelID).Set(float64(active))

	last, _ := db.LastScheduleEntry(ctx, conn, channelID)
	if last != nil {
		runway := float64(last.StartMs+last.DurationMs-nowMs) / 1000
		if runway < 0 {
			runway = 0
		}
		metrics.ScheduleRunwaySeconds.Set(runway)
		metrics.ScheduleRunwayByChannelSeconds.WithLabelValues(channelID).Set(runway)
	}
}
