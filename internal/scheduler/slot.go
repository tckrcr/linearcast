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
	return BuildEntriesSlotGridFilled(channelID, media, nil, startMs, wantEndMs, slotMs, resumeAfterMediaID...)
}

// BuildEntriesSlotGridFilled builds a gap-free slot-grid schedule window:
// primary entries start on fixed wall-clock boundaries at their real packaged
// duration, and every gap — from startMs to the first boundary, between a
// primary's end and the next boundary, and after the final primary — is tiled
// with filler entries so the returned window is contiguous and ends exactly on
// a slot boundary.
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
func BuildEntriesSlotGridFilled(channelID string, media []db.Media, filler []SlotFiller, startMs, wantEndMs, slotMs int64, resumeAfterMediaID ...string) ([]db.ScheduleEntry, error) {
	if len(media) == 0 {
		return nil, nil
	}
	if slotMs <= 0 || slotMs%TargetSegmentMs != 0 {
		return nil, fmt.Errorf("slot_ms=%d must be a positive multiple of %dms", slotMs, TargetSegmentMs)
	}
	if startMs%TargetSegmentMs != 0 {
		return nil, fmt.Errorf("start_ms=%d not aligned to %dms", startMs, TargetSegmentMs)
	}

	fillers := make([]SlotFiller, 0, len(filler))
	for _, f := range filler {
		if f.DurationMs <= 0 || f.DurationMs%TargetSegmentMs != 0 {
			return nil, fmt.Errorf("filler %s duration_ms=%d must be a positive multiple of %dms", f.MediaID, f.DurationMs, TargetSegmentMs)
		}
		if f.CursorMs%TargetSegmentMs != 0 {
			return nil, fmt.Errorf("filler %s cursor_ms=%d not aligned to %dms", f.MediaID, f.CursorMs, TargetSegmentMs)
		}
		f.CursorMs = ((f.CursorMs % f.DurationMs) + f.DurationMs) % f.DurationMs
		fillers = append(fillers, f)
	}

	createdAt := time.Now().UTC().UnixMilli()
	var out []db.ScheduleEntry
	fillerIdx := 0

	tileGap := func(gapStart, gapEnd int64) {
		if len(fillers) == 0 {
			return
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
	}

	mediaIdx := 0
	if len(resumeAfterMediaID) > 0 && resumeAfterMediaID[0] != "" {
		for i, m := range media {
			if m.ID == resumeAfterMediaID[0] {
				mediaIdx = i + 1
				if mediaIdx >= len(media) {
					mediaIdx = 0
				}
				break
			}
		}
	}

	cur := startMs
	skipped := 0
	for {
		if mediaIdx >= len(media) {
			mediaIdx = 0
		}
		m := media[mediaIdx]

		dur := ClipTo6s(m.DurationMs)
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

// AlignToSlot returns ms when already on the slot boundary, otherwise the next
// slot boundary after ms. slotMs must be validated by the caller.
func AlignToSlot(ms, slotMs int64) int64 {
	rem := ms % slotMs
	if rem == 0 {
		return ms
	}
	return ms + (slotMs - rem)
}
