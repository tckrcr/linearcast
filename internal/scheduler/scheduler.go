// Package scheduler holds the shared schedule-writer logic used by the
// linearcast-extender daemon and admin API schedule operations.
//
// All entries are aligned to db.ScheduleGridMs. Codec policy is enforced at
// write time per channel-design.md §5.
package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/tckrcr/linearcast/internal/codec"
	"github.com/tckrcr/linearcast/internal/db"
)

// ErrNoReadyPackages is returned by extendChannelTail when a channel requires
// ready packages and none of its channel_media entries currently have one.
// Callers (e.g. Plex import) treat this as an expected pre-packaging state
// rather than a hard failure.
var ErrNoReadyPackages = errors.New("no eligible ready packaged media")

type Options struct {
	RequireReadyPackages bool
	RenditionProfile     string
	InTransaction        bool
	ResumeAfterMediaID   string
	ScheduleMode         string
	SlotDurationMs       int64
}

func OptionsForChannel(ch db.Channel, fallback Options) Options {
	profile := ch.RequiredPackageProfile
	if profile == "" {
		profile = fallback.RenditionProfile
	}
	if profile == "" {
		profile = db.DefaultPackageProfile
	}
	scheduleMode := ch.ScheduleMode
	if scheduleMode == "" {
		scheduleMode = fallback.ScheduleMode
	}
	if scheduleMode == "" {
		scheduleMode = "back_to_back"
	}
	slotDurationMs := fallback.SlotDurationMs
	if ch.SlotDurationMs != nil {
		slotDurationMs = *ch.SlotDurationMs
	}
	return Options{
		// On-demand and Plex relay channels schedule from eligible media without
		// requiring ready linearcast packages. Eager packaged channels keep the
		// ready-package requirement.
		RequireReadyPackages: ch.PrefillMode != "on_demand" && ch.PlaybackMode != db.PlaybackModePlexRelay,
		RenditionProfile:     profile,
		InTransaction:        fallback.InTransaction,
		ResumeAfterMediaID:   fallback.ResumeAfterMediaID,
		ScheduleMode:         scheduleMode,
		SlotDurationMs:       slotDurationMs,
	}
}

// ExtendChannelTail extends a channel from its current schedule tail.
// Callers that need channel loading, low-water checks, or playback-mode policy
// should use the service entrypoints in service.go instead.
//
// Returns (inserted, lastEnd, err) where lastEnd is the new end_ms of
// the schedule (or the existing end if nothing was inserted).
func ExtendChannelTail(ctx context.Context, conn *sql.DB, channelID, ordering string, hours int) (inserted int, lastEnd int64, err error) {
	return ExtendChannelTailWithOptions(ctx, conn, channelID, ordering, hours, Options{})
}

func ExtendChannelTailWithOptions(ctx context.Context, conn *sql.DB, channelID, ordering string, hours int, opts Options) (inserted int, lastEnd int64, err error) {
	nowMs := time.Now().UTC().UnixMilli()
	return extendChannelTail(ctx, conn, channelID, ordering, hours, nowMs, opts)
}

func extendChannelTail(ctx context.Context, conn db.Execer, channelID, ordering string, hours int, nowMs int64, opts Options) (inserted int, lastEnd int64, err error) {
	wantEndMs := nowMs + int64(hours)*3600*1000

	startMs, existingEnd, tailID, err := scheduleStart(ctx, conn, channelID, nowMs)
	if err != nil {
		return 0, 0, fmt.Errorf("schedule start: %w", err)
	}

	if startMs >= wantEndMs {
		return 0, existingEnd, nil
	}

	var media []db.Media
	if opts.RequireReadyPackages {
		if opts.RenditionProfile == "" {
			return 0, existingEnd, fmt.Errorf("rendition profile is required when RequireReadyPackages is true")
		}
		media, err = db.EligibleReadyPackagedChannelMedia(ctx, conn, channelID, opts.RenditionProfile)
	} else {
		media, err = db.EligibleChannelMedia(ctx, conn, channelID)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("eligible channel_media: %w", err)
	}
	if len(media) == 0 {
		if opts.RequireReadyPackages {
			return 0, existingEnd, fmt.Errorf("%w for channel %s profile %s", ErrNoReadyPackages, channelID, opts.RenditionProfile)
		}
		return 0, existingEnd, fmt.Errorf("no eligible media for channel %s — populate channel_media", channelID)
	}

	var entries []db.ScheduleEntry
	lastMediaID := opts.ResumeAfterMediaID
	if lastMediaID == "" {
		var tail *db.ScheduleEntry
		var tailErr error
		if opts.ScheduleMode == "slot_grid" {
			// Filler entries persist at the tail of a gap-free slot-grid
			// schedule; resuming after the literal last row would miss the
			// primary media list and reset episode rotation to the top.
			tail, tailErr = db.LastPrimaryScheduleEntry(ctx, conn, channelID)
		} else {
			tail, tailErr = db.LastScheduleEntry(ctx, conn, channelID)
		}
		if tailErr != nil {
			return 0, existingEnd, fmt.Errorf("last schedule entry: %w", tailErr)
		}
		if tail != nil {
			lastMediaID = tail.MediaID
		}
	}

	if opts.ScheduleMode == "slot_grid" {
		slotMs := opts.SlotDurationMs
		if slotMs == 0 {
			slotMs = 30 * 60 * 1000
		}
		filler, ferr := loadSlotGridFiller(ctx, conn, channelID, opts.RenditionProfile, startMs)
		if ferr != nil {
			return 0, existingEnd, fmt.Errorf("load slot-grid filler: %w", ferr)
		}
		if len(filler) == 0 {
			log.Printf("WARN channel=%s slot_grid extension found no enabled filler assets with a ready %q package; extending with gaps", channelID, opts.RenditionProfile)
		}
		entries, err = BuildEntriesSlotGridFilled(channelID, media, filler, startMs, wantEndMs, slotMs, lastMediaID)
	} else {
		switch ordering {
		case "block":
			cursors, recentGroup, lerr := db.LoadGroupHistory(ctx, conn, channelID)
			if lerr != nil {
				return 0, existingEnd, fmt.Errorf("load group history: %w", lerr)
			}
			entries, err = BuildEntriesBlock(channelID, media, cursors, recentGroup, startMs, wantEndMs)
		case "", "alphabetical":
			entries, err = BuildEntries(channelID, ordering, media, startMs, wantEndMs, lastMediaID)
		default:
			return 0, existingEnd, fmt.Errorf("unknown ordering %q (want alphabetical|block)", ordering)
		}
	}
	if err != nil {
		return 0, existingEnd, err
	}
	if len(entries) == 0 {
		return 0, existingEnd, nil
	}
	if tailID != "" {
		entries[0].AnchorScheduleEntryID = &tailID
	}

	var n int
	if opts.InTransaction {
		n, err = db.InsertScheduleEntries(ctx, conn, entries)
	} else {
		sqlDB, ok := conn.(*sql.DB)
		if !ok {
			return 0, existingEnd, fmt.Errorf("transaction is required for schedule insertion")
		}
		n, err = InsertEntries(ctx, sqlDB, entries)
	}
	if err != nil {
		return 0, existingEnd, err
	}
	last := entries[len(entries)-1]
	return n, last.StartMs + last.DurationMs, nil
}

// loadSlotGridFiller assembles the filler set for slot-grid gap tiling:
// channel-attached, enabled, with a ready package for the rendition profile.
// Each asset's rotation cursor seeds from its most recent persisted placement
// before beforeMs (the extension start), matching the "sequential" offset mode
// of the interactive gap fill, so extension continues the rotation instead of
// replaying each asset's opening seconds.
func loadSlotGridFiller(ctx context.Context, conn db.Execer, channelID, profile string, beforeMs int64) ([]SlotFiller, error) {
	assets, err := db.ChannelFillerAssets(ctx, conn, channelID, profile)
	if err != nil {
		return nil, fmt.Errorf("channel filler assets: %w", err)
	}
	var out []SlotFiller
	for _, a := range assets {
		if !a.Enabled || !a.ChannelEnabled {
			continue
		}
		if a.PackageStatus == nil || *a.PackageStatus != string(db.PackageStatusReady) || a.PackagedDurationMs == nil {
			continue
		}
		dur := ClipTo6s(*a.PackagedDurationMs)
		if dur <= 0 {
			continue
		}
		var cursor int64
		prev, perr := db.LastEntryWithMediaBefore(ctx, conn, channelID, a.MediaID, beforeMs)
		if perr != nil {
			return nil, fmt.Errorf("last filler placement: %w", perr)
		}
		if prev != nil {
			cursor = (prev.OffsetMs + prev.DurationMs) % dur
		}
		out = append(out, SlotFiller{MediaID: a.MediaID, DurationMs: dur, CursorMs: cursor})
	}
	return out, nil
}

// scheduleStart returns (nextStartMs, currentEndMs, tailID).
// nextStartMs is where new entries should begin (existing end, aligned forward to now if stale).
// currentEndMs is the end of the existing schedule, or align6s(now) if empty.
func scheduleStart(ctx context.Context, conn db.Execer, channelID string, nowMs int64) (int64, int64, string, error) {
	last, err := db.LastScheduleEntry(ctx, conn, channelID)
	if err != nil {
		return 0, 0, "", err
	}
	if last == nil {
		// Use the segment grid as the source of truth for schedule starts.
		// This floors to the current 6s boundary so the first playable window
		// includes "now" instead of opening a short gap before the next boundary.
		aligned := Align6s(nowMs)
		return aligned, aligned, "", nil
	}
	end := last.StartMs + last.DurationMs
	if end%db.ScheduleGridMs != 0 {
		return 0, 0, "", fmt.Errorf("last entry end_ms=%d not aligned to %dms", end, db.ScheduleGridMs)
	}
	if end < nowMs {
		// Existing schedule has fully drained; resume on the current segment
		// grid boundary. The old end is still returned for observability so
		// callers can see how far the schedule had fallen behind.
		return Align6s(nowMs), end, last.ID, nil
	}
	return end, end, last.ID, nil
}

// BuildEntries walks media in order from startMs, clipping each entry's
// duration to a 6s boundary, until it hits wantEndMs or runs out of room.
// BuildEntries builds schedule entries by cycling through media until
// wantEndMs. Refuses any media row that fails the codec allowlist.
// If only one media item is eligible, it will not loop indefinitely -
// returns entries until that item is exhausted, then stops.
//
// resumeAfterMediaID, if non-empty, positions the starting cursor immediately
// after the named media item so the new entries continue the existing sequence
// rather than restarting from the top of the list.
func BuildEntries(channelID, ordering string, media []db.Media, startMs, wantEndMs int64, resumeAfterMediaID ...string) ([]db.ScheduleEntry, error) {
	_ = ordering // v1: only "alphabetical"; channel_media.sort_key drives order upstream
	if len(media) == 0 {
		return nil, nil
	}
	var out []db.ScheduleEntry
	cur := startMs
	mediaIdx := 0
	offsetInMedia := int64(0)

	// If the caller knows which media item was last scheduled, advance past it
	// so we don't replay it at the start of the new extension window.
	if len(resumeAfterMediaID) > 0 && resumeAfterMediaID[0] != "" {
		for i, m := range media {
			if m.ID == resumeAfterMediaID[0] {
				mediaIdx = i + 1
				if mediaIdx >= len(media) {
					mediaIdx = 0 // wrapped to next cycle
				}
				break
			}
		}
	}

	for cur < wantEndMs {
		if mediaIdx >= len(media) {
			mediaIdx = 0
			offsetInMedia = 0
		}
		m := media[mediaIdx]

		remainingInMedia := m.DurationMs - offsetInMedia
		if remainingInMedia <= 0 {
			mediaIdx++
			offsetInMedia = 0
			continue
		}

		dur := ClipTo6s(remainingInMedia)
		if dur <= 0 {
			mediaIdx++
			offsetInMedia = 0
			continue
		}
		if cur+dur > wantEndMs {
			// Do not write a short tail entry: schedule_entries.duration_ms is
			// constrained to 6s multiples, and playback assumes segment-sized
			// windows when mapping wall-clock time back into media offsets.
			break
		}

		if err := enforceCodecAllowlist(m); err != nil {
			return nil, fmt.Errorf("codec policy violated for %s: %w", m.ID, err)
		}

		out = append(out, db.ScheduleEntry{
			ChannelID:   channelID,
			StartMs:     cur,
			MediaID:     m.ID,
			OffsetMs:    offsetInMedia,
			DurationMs:  dur,
			CreatedAtMs: time.Now().UTC().UnixMilli(),
		})

		cur += dur
		offsetInMedia += dur
		if offsetInMedia >= m.DurationMs {
			mediaIdx++
			offsetInMedia = 0
		}
	}
	return out, nil
}

func enforceCodecAllowlist(m db.Media) error {
	if db.NormalizeMediaKind(m.MediaKind) == db.MediaKindMusic {
		return nil
	}
	reason, ok := codec.Check(codec.Probe{
		Container:       m.Container,
		VideoCodec:      m.VideoCodec,
		VideoHeight:     m.VideoHeight,
		ColorTransfer:   m.ColorTransfer,
		ColorPrimaries:  m.ColorPrimaries,
		AudioCodec:      m.AudioCodec,
	})
	if !ok {
		return fmt.Errorf("%s", reason)
	}
	return nil
}

// InsertEntries writes entries in a single transaction. Misaligned entries
// and duplicate schedule keys fail the transaction before commit.
func InsertEntries(ctx context.Context, conn *sql.DB, entries []db.ScheduleEntry) (int, error) {
	var inserted int
	err := db.WithTx(ctx, conn, func(tx db.Execer) error {
		var err error
		inserted, err = db.InsertScheduleEntries(ctx, tx, entries)
		return err
	})
	return inserted, err
}

// TargetSegmentMs is the canonical packaged-segment duration used by the
// packager, encoder, scheduler, and HLS manifest generator. All schedule
// entry start_ms and duration_ms must be divisible by this value.
const TargetSegmentMs = db.ScheduleGridMs

func ClipTo6s(ms int64) int64 { return ms - (ms % TargetSegmentMs) }
func Align6s(ms int64) int64  { return ms - (ms % TargetSegmentMs) }
