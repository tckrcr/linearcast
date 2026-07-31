package scheduler

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestBuildEntriesProperties(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		mediaCount := 1 + rng.Intn(6)
		media := make([]db.Media, mediaCount)
		mediaDuration := make(map[string]int64, mediaCount)
		for i := range media {
			durationMs := int64(1+rng.Intn(20))*db.ScheduleGridMs + int64(rng.Intn(int(db.ScheduleGridMs)))
			media[i] = mediaRow(fmt.Sprintf("m%d", i), "", durationMs)
			mediaDuration[media[i].ID] = durationMs
		}
		startMs := int64(rng.Intn(20)) * db.ScheduleGridMs
		wantEndMs := startMs + int64(10+rng.Intn(100))*db.ScheduleGridMs
		resumeIndex := rng.Intn(mediaCount)
		entries, err := BuildEntries("ch", "alphabetical", media, startMs, wantEndMs, media[resumeIndex].ID)
		if err != nil {
			t.Fatalf("seed %d: build entries: %v", seed, err)
		}
		assertGeneratedScheduleProperties(t, seed, entries, mediaDuration, false)
		if len(entries) > 0 {
			wantFirst := media[(resumeIndex+1)%mediaCount].ID
			if entries[0].MediaID != wantFirst {
				t.Fatalf("seed %d: first media=%s, want successor %s after resume media %s", seed, entries[0].MediaID, wantFirst, media[resumeIndex].ID)
			}
			if entries[0].StartMs != startMs {
				t.Fatalf("seed %d: first start=%d, want %d", seed, entries[0].StartMs, startMs)
			}
			for i := 1; i < len(entries); i++ {
				previousEnd := entries[i-1].StartMs + entries[i-1].DurationMs
				if entries[i].StartMs != previousEnd {
					t.Fatalf("seed %d: back-to-back gap or overlap between entries %d and %d: %d != %d", seed, i-1, i, entries[i].StartMs, previousEnd)
				}
			}
			last := entries[len(entries)-1]
			if last.StartMs+last.DurationMs > wantEndMs {
				t.Fatalf("seed %d: schedule ends at %d past horizon %d", seed, last.StartMs+last.DurationMs, wantEndMs)
			}
		}
	}
}

func TestBuildEntriesSlotGridProperties(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		slotMs := int64(5+rng.Intn(26)) * db.ScheduleGridMs
		startMs := int64(rng.Intn(60)) * db.ScheduleGridMs
		wantEndMs := startMs + 6*slotMs
		allowLeadingPrimary := rng.Intn(2) == 0

		mediaCount := 1 + rng.Intn(5)
		media := make([]db.Media, mediaCount)
		mediaDuration := make(map[string]int64, mediaCount+3)
		for i := range media {
			durationMs := int64(1+rng.Intn(int(slotMs/db.ScheduleGridMs))) * db.ScheduleGridMs
			media[i] = mediaRow(fmt.Sprintf("primary-%d", i), "", durationMs)
			mediaDuration[media[i].ID] = durationMs
		}

		fillerCount := 1 + rng.Intn(3)
		fillers := make([]SlotFiller, fillerCount)
		for i := range fillers {
			durationMs := int64(1+rng.Intn(int(slotMs/db.ScheduleGridMs))) * db.ScheduleGridMs
			cursorUnits := rng.Intn(int(durationMs / db.ScheduleGridMs))
			fillers[i] = SlotFiller{
				MediaID: fmt.Sprintf("filler-%d", i), DurationMs: durationMs,
				CursorMs: int64(cursorUnits) * db.ScheduleGridMs,
			}
			mediaDuration[fillers[i].MediaID] = durationMs
		}

		filled, err := BuildEntriesSlotGridFilled("ch", media, fillers, startMs, wantEndMs, slotMs, allowLeadingPrimary)
		if err != nil {
			t.Fatalf("seed %d: build filled slot grid: %v", seed, err)
		}
		if len(filled) == 0 {
			t.Fatalf("seed %d: filled slot grid unexpectedly empty", seed)
		}
		assertGeneratedScheduleProperties(t, seed, filled, mediaDuration, true)
		if filled[0].StartMs != startMs {
			t.Fatalf("seed %d: filled schedule starts at %d, want %d", seed, filled[0].StartMs, startMs)
		}
		for i, entry := range filled {
			if i > 0 {
				previousEnd := filled[i-1].StartMs + filled[i-1].DurationMs
				if entry.StartMs != previousEnd {
					t.Fatalf("seed %d: filler failed to tile [%d,%d)", seed, previousEnd, entry.StartMs)
				}
			}
			if entry.Kind == "primary" && entry.StartMs%slotMs != 0 {
				isLegalLeadingPrimary := allowLeadingPrimary && entry.StartMs == startMs
				if !isLegalLeadingPrimary {
					t.Fatalf("seed %d: primary %s starts at %d off slot grid %d", seed, entry.MediaID, entry.StartMs, slotMs)
				}
			}
		}

		gappy, err := BuildEntriesSlotGrid("ch", media, startMs, wantEndMs, slotMs)
		if err != nil {
			t.Fatalf("seed %d: build unfilled slot grid: %v", seed, err)
		}
		assertGeneratedScheduleProperties(t, seed, gappy, mediaDuration, true)
		for i, entry := range gappy {
			if entry.Kind != "primary" {
				t.Fatalf("seed %d: unfilled schedule entry %d has kind %q", seed, i, entry.Kind)
			}
			if entry.StartMs%slotMs != 0 {
				t.Fatalf("seed %d: unfilled primary starts at %d off slot grid %d", seed, entry.StartMs, slotMs)
			}
			if i > 0 {
				previousEnd := gappy[i-1].StartMs + gappy[i-1].DurationMs
				if entry.StartMs < previousEnd {
					t.Fatalf("seed %d: unfilled slot entries overlap: %d < %d", seed, entry.StartMs, previousEnd)
				}
			}
		}
	}
}

func assertGeneratedScheduleProperties(t *testing.T, seed int64, entries []db.ScheduleEntry, mediaDuration map[string]int64, requireKind bool) {
	t.Helper()
	for i, entry := range entries {
		durationMs, ok := mediaDuration[entry.MediaID]
		if !ok {
			t.Fatalf("seed %d: entry %d references unknown media %s", seed, i, entry.MediaID)
		}
		if entry.StartMs%db.ScheduleGridMs != 0 || entry.DurationMs%db.ScheduleGridMs != 0 || entry.OffsetMs%db.ScheduleGridMs != 0 {
			t.Fatalf("seed %d: entry %d is off grid: start=%d duration=%d offset=%d", seed, i, entry.StartMs, entry.DurationMs, entry.OffsetMs)
		}
		if entry.DurationMs <= 0 || entry.OffsetMs < 0 || entry.OffsetMs+entry.DurationMs > durationMs {
			t.Fatalf("seed %d: entry %d exceeds media bounds: offset=%d duration=%d media=%d", seed, i, entry.OffsetMs, entry.DurationMs, durationMs)
		}
		if requireKind && entry.Kind != "primary" && entry.Kind != "filler" {
			t.Fatalf("seed %d: entry %d has invalid kind %q", seed, i, entry.Kind)
		}
	}
}
