package scheduler

import (
	"fmt"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// SlotFiller is one channel filler asset available for tiling slot-grid gaps.
type SlotFiller struct {
	MediaID string
	// DurationMs is the packaged playable duration, clipped to the 6s grid.
	DurationMs int64
	// CursorMs is the rotation offset to resume from, seeded from the most
	// recent persisted placement of this asset so extension continues the
	// rotation instead of replaying the asset's opening seconds in every gap.
	CursorMs int64
}

// BuildEntriesSlotGrid builds primary schedule entries on fixed wall-clock
// boundaries and leaves explicit schedule gaps between primary programming.
// It is the no-filler form of BuildEntriesSlotGridFilled, kept for callers
// and tests that exercise primary placement alone.
func BuildEntriesSlotGrid(channelID string, media []db.Media, startMs, wantEndMs, slotMs int64, resumeAfterMediaID ...string) ([]db.ScheduleEntry, error) {
	return BuildEntriesSlotGridFilled(channelID, media, nil, startMs, wantEndMs, slotMs, false, resumeAfterMediaID...)
}

// BuildEntriesSlotGridFilled builds a gap-free slot-grid schedule window:
// primary entries start on fixed wall-clock boundaries at their real packaged
// duration, and every gap — from startMs to the first boundary, between a
// primary's end and the next boundary, and after the final primary — is tiled
// with filler entries so the returned window is contiguous and ends exactly on
// a slot boundary.
//
// When allowLeadingPrimary is true and startMs is not on a slot boundary, the
// longest codec-eligible primary that fits between startMs and the first slot
// boundary is placed first. This avoids dead air when a slot-grid channel is
// first created at a non-boundary time. The first slot-boundary primary then
// resumes rotation after the leading episode.
//
// Filler rotates round-robin across assets, and each asset plays forward from
// its cursor, wrapping to offset 0 at its packaged end; a gap longer than the
// remaining asset is split across entries rather than restarted. All gaps are
// 6s-aligned multiples, as are filler durations and cursors, so tiling is
// always exact.
//
// With no filler the gaps are left as absent entries (the pre-gap-free
// behavior); callers that require the gap-free invariant must pass filler.
// No entries are returned when no primary fits the window — extension never
// appends a filler-only tail.
func BuildEntriesSlotGridFilled(channelID string, media []db.Media, filler []SlotFiller, startMs, wantEndMs, slotMs int64, allowLeadingPrimary bool, resumeAfterMediaID ...string) ([]db.ScheduleEntry, error) {
	if len(media) == 0 {
		return nil, nil
	}
	if slotMs <= 0 || slotMs%db.ScheduleGridMs != 0 {
		return nil, fmt.Errorf("slot_ms=%d must be a positive multiple of %dms", slotMs, db.ScheduleGridMs)
	}
	if startMs%db.ScheduleGridMs != 0 {
		return nil, fmt.Errorf("start_ms=%d not aligned to %dms", startMs, db.ScheduleGridMs)
	}

	fillers := make([]SlotFiller, 0, len(filler))
	for _, f := range filler {
		if f.DurationMs <= 0 || f.DurationMs%db.ScheduleGridMs != 0 {
			return nil, fmt.Errorf("filler %s duration_ms=%d must be a positive multiple of %dms", f.MediaID, f.DurationMs, db.ScheduleGridMs)
		}
		if f.CursorMs%db.ScheduleGridMs != 0 {
			return nil, fmt.Errorf("filler %s cursor_ms=%d not aligned to %dms", f.MediaID, f.CursorMs, db.ScheduleGridMs)
		}
		f.CursorMs = ((f.CursorMs % f.DurationMs) + f.DurationMs) % f.DurationMs
		fillers = append(fillers, f)
	}

	createdAt := time.Now().UTC().UnixMilli()
	var out []db.ScheduleEntry
	fillerIdx := 0

	tileGap := func(gapStart, gapEnd int64) {
		out, fillerIdx = tileFillerGap(out, channelID, fillers, fillerIdx, gapStart, gapEnd, createdAt)
	}

	mediaIdx := 0
	resumeID := ""
	if len(resumeAfterMediaID) > 0 && resumeAfterMediaID[0] != "" {
		resumeID = resumeAfterMediaID[0]
	}

	cur := startMs
	// On a fresh slot-grid channel, place the longest primary that fits in the
	// leading gap so tune-in is not dead air / filler-only.
	if allowLeadingPrimary {
		firstBoundary := AlignToSlot(startMs, slotMs)
		if firstBoundary > startMs {
			if leading, leadingResumeID := bestFitLeadingPrimary(channelID, media, startMs, slotMs, createdAt); leading != nil {
				out = append(out, *leading)
				tileGap(startMs+leading.DurationMs, firstBoundary)
				cur = firstBoundary
				resumeID = leadingResumeID
			}
		}
	}
	if resumeID != "" {
		for i, m := range media {
			if m.ID == resumeID {
				mediaIdx = i + 1
				if mediaIdx >= len(media) {
					mediaIdx = 0
				}
				break
			}
		}
	}

	skipped := 0
	for {
		if mediaIdx >= len(media) {
			mediaIdx = 0
		}
		m := media[mediaIdx]

		dur := ClipToGrid(m.DurationMs)
		if dur <= 0 {
			mediaIdx++
			skipped++
			if skipped >= len(media) {
				break
			}
			continue
		}
		skipped = 0

		boundary := AlignToSlot(cur, slotMs)
		if boundary+dur > wantEndMs {
			break
		}

		if err := enforceCodecAllowlist(m); err != nil {
			return nil, fmt.Errorf("codec policy violated for %s: %w", m.ID, err)
		}

		tileGap(cur, boundary)
		out = append(out, db.ScheduleEntry{
			ChannelID:   channelID,
			StartMs:     boundary,
			MediaID:     m.ID,
			OffsetMs:    0,
			DurationMs:  dur,
			CreatedAtMs: createdAt,
			Kind:        "primary",
		})

		mediaIdx++
		cur = boundary + dur
	}

	// Land the tail on a slot boundary so the next extension starts on the
	// grid with no leading gap to explain.
	if len(out) > 0 {
		tileGap(cur, AlignToSlot(cur, slotMs))
	}
	return out, nil
}

// bestFitLeadingPrimary picks the longest codec-eligible primary media whose
// clipped duration fits inside the gap from startMs to the next slot boundary.
// It returns the schedule entry for that media and the media ID so the caller
// can resume rotation immediately after it. Returns (nil, "") when no media
// fits.
func bestFitLeadingPrimary(channelID string, media []db.Media, startMs, slotMs, createdAt int64) (*db.ScheduleEntry, string) {
	firstBoundary := AlignToSlot(startMs, slotMs)
	gapMs := firstBoundary - startMs
	if gapMs <= 0 {
		return nil, ""
	}
	var best *db.Media
	var bestDur int64
	for i := range media {
		m := &media[i]
		dur := ClipToGrid(m.DurationMs)
		if dur <= 0 || dur > gapMs {
			continue
		}
		if err := enforceCodecAllowlist(*m); err != nil {
			continue
		}
		if dur > bestDur {
			best = m
			bestDur = dur
		}
	}
	if best == nil {
		return nil, ""
	}
	return &db.ScheduleEntry{
		ChannelID:   channelID,
		StartMs:     startMs,
		MediaID:     best.ID,
		OffsetMs:    0,
		DurationMs:  bestDur,
		CreatedAtMs: createdAt,
		Kind:        "primary",
	}, best.ID
}

// tileFillerGap appends round-robin filler entries covering [gapStart, gapEnd)
// to out. Each filler plays forward from its cursor, wrapping at its packaged
// end; a gap longer than the remaining asset is split across entries rather
// than restarted. fillerIdx is the rotating asset index threaded in and out so
// rotation continues across calls; filler cursors are advanced in place. It is
// a no-op when fillers is empty (callers that require gap-free output must pass
// filler). Caller guarantees gapStart/gapEnd and all filler durations/cursors
// are 6s-aligned, so tiling is always exact.
func tileFillerGap(out []db.ScheduleEntry, channelID string, fillers []SlotFiller, fillerIdx int, gapStart, gapEnd, createdAt int64) ([]db.ScheduleEntry, int) {
	if len(fillers) == 0 {
		return out, fillerIdx
	}
	for cur := gapStart; cur < gapEnd; {
		f := &fillers[fillerIdx%len(fillers)]
		fillerIdx++
		avail := f.DurationMs - f.CursorMs
		need := gapEnd - cur
		take := avail
		if need < take {
			take = need
		}
		out = append(out, db.ScheduleEntry{
			ChannelID:   channelID,
			StartMs:     cur,
			MediaID:     f.MediaID,
			OffsetMs:    f.CursorMs,
			DurationMs:  take,
			CreatedAtMs: createdAt,
			Kind:        "filler",
		})
		f.CursorMs = (f.CursorMs + take) % f.DurationMs
		cur += take
	}
	return out, fillerIdx
}

// AlignToSlot returns ms when already on the slot boundary, otherwise the next
// slot boundary after ms. slotMs must be validated by the caller.
func AlignToSlot(ms, slotMs int64) int64 {
	rem := ms % slotMs
	if rem == 0 {
		return ms
	}
	return ms + (slotMs - rem)
}
