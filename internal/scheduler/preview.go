package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

var ErrChannelNotFound = errors.New("channel not found")

type PreviewOptions struct {
	FromMs           int64
	DurationMs       int64
	RenditionProfile string
	NowMs            int64
}

type PreviewWarning struct {
	Code    string
	Message string
}

type PreviewResult struct {
	ChannelID            string
	DisplayName          string
	Ordering             string
	FromMs               int64
	ToMs                 int64
	GeneratedEndMs       int64
	RequireReadyPackages bool
	RenditionProfile     string
	EligibleMedia        int
	EligibleReadyMedia   int
	Entries              []db.ScheduleEntry
	Warnings             []PreviewWarning
}

// PreviewChannel builds the schedule entries that would be generated for a
// window without writing them. It treats FromMs as a regeneration boundary:
// schedule history before FromMs can influence continuation, but rows at or
// after FromMs are ignored by the planner and left untouched.
func PreviewChannel(ctx context.Context, conn *sql.DB, channelID string, opts PreviewOptions) (PreviewResult, error) {
	if err := ctx.Err(); err != nil {
		return PreviewResult{}, err
	}
	ch, err := db.ChannelByID(ctx, conn, channelID)
	if err != nil {
		return PreviewResult{}, fmt.Errorf("lookup channel: %w", err)
	}
	if ch == nil {
		return PreviewResult{}, fmt.Errorf("%w: %s", ErrChannelNotFound, channelID)
	}

	nowMs := opts.NowMs
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
	}
	fromMs := opts.FromMs
	if fromMs == 0 {
		fromMs = nowMs
	}
	alignedFrom := Align6s(fromMs)
	durationMs := opts.DurationMs
	if durationMs <= 0 {
		durationMs = int64(24 * time.Hour / time.Millisecond)
	}
	toMs := alignedFrom + durationMs

	effective := OptionsForChannel(*ch, Options{
		RequireReadyPackages: true,
		RenditionProfile:     opts.RenditionProfile,
	})
	result := PreviewResult{
		ChannelID:            ch.ID,
		DisplayName:          ch.DisplayName,
		Ordering:             ch.Ordering,
		FromMs:               alignedFrom,
		ToMs:                 toMs,
		GeneratedEndMs:       alignedFrom,
		RequireReadyPackages: effective.RequireReadyPackages,
		RenditionProfile:     effective.RenditionProfile,
		Entries:              []db.ScheduleEntry{},
	}
	if alignedFrom != fromMs {
		result.Warnings = append(result.Warnings, PreviewWarning{
			Code:    "start_aligned",
			Message: fmt.Sprintf("start time was aligned from %d to %d", fromMs, alignedFrom),
		})
	}

	allEligible, err := db.EligibleChannelMedia(ctx, conn, channelID)
	if err != nil {
		return result, fmt.Errorf("eligible channel media: %w", err)
	}
	result.EligibleMedia = len(allEligible)

	media, err := db.EligibleReadyPackagedChannelMedia(ctx, conn, channelID, effective.RenditionProfile)
	if err != nil {
		return result, fmt.Errorf("eligible ready packaged media: %w", err)
	}
	result.EligibleReadyMedia = len(media)
	if len(media) == 0 {
		if len(allEligible) == 0 {
			result.Warnings = append(result.Warnings, PreviewWarning{
				Code:    "no_eligible_media",
				Message: "channel has no codec-passing media",
			})
			return result, nil
		}
		result.Warnings = append(result.Warnings, PreviewWarning{
			Code:    "no_ready_packages",
			Message: fmt.Sprintf("channel has no ready packages for profile %s", effective.RenditionProfile),
		})
		return result, nil
	}
	if len(media) < len(allEligible) {
		result.Warnings = append(result.Warnings, PreviewWarning{
			Code:    "partial_ready_packages",
			Message: fmt.Sprintf("%d of %d eligible media have ready packages for profile %s", len(media), len(allEligible), effective.RenditionProfile),
		})
	}

	var entries []db.ScheduleEntry
	switch ch.Ordering {
	case "block":
		cursors, recentGroup, lerr := db.LoadGroupHistoryBefore(ctx, conn, channelID, alignedFrom)
		if lerr != nil {
			return result, fmt.Errorf("load group history: %w", lerr)
		}
		entries, err = BuildEntriesBlock(channelID, media, cursors, recentGroup, alignedFrom, toMs)
	case "", "alphabetical":
		last, lerr := db.LastScheduleEntryBefore(ctx, conn, channelID, alignedFrom)
		if lerr != nil {
			return result, fmt.Errorf("last schedule entry before preview: %w", lerr)
		}
		var resumeAfter string
		if last != nil {
			resumeAfter = last.MediaID
		}
		entries, err = BuildEntries(channelID, ch.Ordering, media, alignedFrom, toMs, resumeAfter)
	default:
		return result, fmt.Errorf("unknown ordering %q (want alphabetical|block)", ch.Ordering)
	}
	if err != nil {
		if errors.Is(err, ErrNoReadyPackages) {
			result.Warnings = append(result.Warnings, PreviewWarning{Code: "no_ready_packages", Message: err.Error()})
			return result, nil
		}
		return result, err
	}
	result.Entries = entries
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		result.GeneratedEndMs = last.StartMs + last.DurationMs
	}
	if result.GeneratedEndMs < toMs {
		result.Warnings = append(result.Warnings, PreviewWarning{
			Code:    "unfilled_window",
			Message: fmt.Sprintf("preview generated through %d, before requested end %d", result.GeneratedEndMs, toMs),
		})
	}
	return result, nil
}
