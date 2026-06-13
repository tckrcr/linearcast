package scheduler

import (
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestBuildEntriesSlotGridSnapsPrimaryStartsAndLeavesGaps(t *testing.T) {
	media := []db.Media{
		mediaRow("e04", "", 19*60*1000+36*1000),
		mediaRow("e05", "", 22*60*1000+30*1000),
		mediaRow("e06", "", 28*60*1000+6*1000),
	}

	entries, err := BuildEntriesSlotGrid("ch", media, 0, 2*60*60*1000, 30*60*1000)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGrid: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	want := []struct {
		mediaID  string
		startMs  int64
		duration int64
	}{
		{"e04", 0, 19*60*1000 + 36*1000},
		{"e05", 30 * 60 * 1000, 22*60*1000 + 30*1000},
		{"e06", 60 * 60 * 1000, 28*60*1000 + 6*1000},
		{"e04", 90 * 60 * 1000, 19*60*1000 + 36*1000},
	}
	for i, w := range want {
		if entries[i].MediaID != w.mediaID || entries[i].StartMs != w.startMs || entries[i].DurationMs != w.duration {
			t.Fatalf("entry %d = media=%s start=%d dur=%d, want media=%s start=%d dur=%d",
				i, entries[i].MediaID, entries[i].StartMs, entries[i].DurationMs, w.mediaID, w.startMs, w.duration)
		}
	}
}

func TestBuildEntriesSlotGridAlignsInitialStartForward(t *testing.T) {
	media := []db.Media{mediaRow("m1", "", 12*60*1000)}

	entries, err := BuildEntriesSlotGrid("ch", media, 6*60*1000, 50*60*1000, 30*60*1000)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGrid: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].StartMs != 30*60*1000 {
		t.Fatalf("start_ms=%d, want 30m", entries[0].StartMs)
	}
}

func TestBuildEntriesSlotGridRejectsNonSegmentAlignedSlot(t *testing.T) {
	media := []db.Media{mediaRow("m1", "", 12*60*1000)}

	_, err := BuildEntriesSlotGrid("ch", media, 0, 60*60*1000, 5*60*1000+1000)
	if err == nil {
		t.Fatal("expected non-aligned slot error")
	}
}

func assertContiguous(t *testing.T, entries []db.ScheduleEntry, startMs, endMs int64) {
	t.Helper()
	cur := startMs
	for i, e := range entries {
		if e.StartMs != cur {
			t.Fatalf("entry %d (media=%s) start=%d, want %d (gap or overlap)", i, e.MediaID, e.StartMs, cur)
		}
		cur = e.StartMs + e.DurationMs
	}
	if cur != endMs {
		t.Fatalf("window ends at %d, want %d", cur, endMs)
	}
}

func TestBuildEntriesSlotGridFilledTilesEveryGapAndEndsOnBoundary(t *testing.T) {
	media := []db.Media{
		mediaRow("e04", "", 19*60*1000+36*1000),
		mediaRow("e05", "", 22*60*1000+30*1000),
	}
	filler := []SlotFiller{{MediaID: "f1", DurationMs: 5 * 60 * 1000}}
	slotMs := int64(30 * 60 * 1000)

	entries, err := BuildEntriesSlotGridFilled("ch", media, filler, 0, 90*60*1000, slotMs)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGridFilled: %v", err)
	}
	// Gap-free across the whole window, tail landing exactly on a boundary.
	assertContiguous(t, entries, 0, 90*60*1000)

	var primaries []db.ScheduleEntry
	for _, e := range entries {
		if e.MediaID == "f1" {
			if e.OffsetMs%TargetSegmentMs != 0 || e.DurationMs <= 0 ||
				e.OffsetMs+e.DurationMs > filler[0].DurationMs {
				t.Fatalf("filler entry out of asset bounds: %+v", e)
			}
			continue
		}
		if e.StartMs%slotMs != 0 {
			t.Fatalf("primary %s start=%d not on slot boundary", e.MediaID, e.StartMs)
		}
		primaries = append(primaries, e)
	}
	wantPrimaries := []struct {
		mediaID string
		startMs int64
	}{
		{"e04", 0},
		{"e05", 30 * 60 * 1000},
		{"e04", 60 * 60 * 1000},
	}
	if len(primaries) != len(wantPrimaries) {
		t.Fatalf("got %d primaries, want %d: %+v", len(primaries), len(wantPrimaries), primaries)
	}
	for i, w := range wantPrimaries {
		if primaries[i].MediaID != w.mediaID || primaries[i].StartMs != w.startMs {
			t.Fatalf("primary %d = %s@%d, want %s@%d",
				i, primaries[i].MediaID, primaries[i].StartMs, w.mediaID, w.startMs)
		}
	}
}

func TestBuildEntriesSlotGridFilledTagsEntryKind(t *testing.T) {
	media := []db.Media{
		mediaRow("e04", "", 19*60*1000+36*1000),
		mediaRow("e05", "", 22*60*1000+30*1000),
	}
	filler := []SlotFiller{{MediaID: "f1", DurationMs: 5 * 60 * 1000}}
	slotMs := int64(30 * 60 * 1000)

	entries, err := BuildEntriesSlotGridFilled("ch", media, filler, 0, 90*60*1000, slotMs)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGridFilled: %v", err)
	}
	for _, e := range entries {
		wantKind := "primary"
		if e.MediaID == "f1" {
			wantKind = "filler"
		}
		if e.Kind != wantKind {
			t.Fatalf("entry %s@%d Kind=%q, want %q", e.MediaID, e.StartMs, e.Kind, wantKind)
		}
	}
}

func TestBuildEntriesSlotGridFilledRotatesFillerSequentially(t *testing.T) {
	// One primary leaves a 4-minute trailing gap; two assets alternate and the
	// first wraps back to its start when it is exhausted.
	media := []db.Media{mediaRow("e1", "", 26*60*1000)}
	filler := []SlotFiller{
		{MediaID: "f1", DurationMs: 60 * 1000},
		{MediaID: "f2", DurationMs: 120 * 1000},
	}

	entries, err := BuildEntriesSlotGridFilled("ch", media, filler, 0, 30*60*1000, 30*60*1000)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGridFilled: %v", err)
	}
	assertContiguous(t, entries, 0, 30*60*1000)

	want := []struct {
		mediaID  string
		offsetMs int64
		duration int64
	}{
		{"e1", 0, 26 * 60 * 1000},
		{"f1", 0, 60 * 1000},
		{"f2", 0, 120 * 1000},
		{"f1", 0, 60 * 1000},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].MediaID != w.mediaID || entries[i].OffsetMs != w.offsetMs || entries[i].DurationMs != w.duration {
			t.Fatalf("entry %d = media=%s off=%d dur=%d, want media=%s off=%d dur=%d",
				i, entries[i].MediaID, entries[i].OffsetMs, entries[i].DurationMs, w.mediaID, w.offsetMs, w.duration)
		}
	}
}

func TestBuildEntriesSlotGridFilledContinuesFromCursorAndSplitsAcrossWrap(t *testing.T) {
	// 2-minute gap, cursor 60s from the asset end: the fill continues from the
	// cursor and splits across the wrap instead of restarting at zero.
	media := []db.Media{mediaRow("e1", "", 28*60*1000)}
	filler := []SlotFiller{{MediaID: "f1", DurationMs: 5 * 60 * 1000, CursorMs: 4 * 60 * 1000}}

	entries, err := BuildEntriesSlotGridFilled("ch", media, filler, 0, 30*60*1000, 30*60*1000)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGridFilled: %v", err)
	}
	assertContiguous(t, entries, 0, 30*60*1000)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	if entries[1].MediaID != "f1" || entries[1].OffsetMs != 4*60*1000 || entries[1].DurationMs != 60*1000 {
		t.Fatalf("entry 1 = %+v, want f1 off=4m dur=1m", entries[1])
	}
	if entries[2].MediaID != "f1" || entries[2].OffsetMs != 0 || entries[2].DurationMs != 60*1000 {
		t.Fatalf("entry 2 = %+v, want f1 off=0 dur=1m", entries[2])
	}
}

func TestBuildEntriesSlotGridFilledTilesLeadingGap(t *testing.T) {
	// Extension resuming mid-slot (e.g. after a drained schedule) tiles the
	// stretch up to the first primary boundary instead of leaving dead air.
	media := []db.Media{mediaRow("e1", "", 24*60*1000)}
	filler := []SlotFiller{{MediaID: "f1", DurationMs: 10 * 60 * 1000}}

	entries, err := BuildEntriesSlotGridFilled("ch", media, filler, 12*60*1000, 60*60*1000, 30*60*1000)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGridFilled: %v", err)
	}
	assertContiguous(t, entries, 12*60*1000, 60*60*1000)
	if entries[0].MediaID != "f1" || entries[0].StartMs != 12*60*1000 {
		t.Fatalf("entry 0 = %+v, want leading filler at 12m", entries[0])
	}
	var sawPrimary bool
	for _, e := range entries {
		if e.MediaID == "e1" {
			sawPrimary = true
			if e.StartMs != 30*60*1000 {
				t.Fatalf("primary start=%d, want 30m", e.StartMs)
			}
		}
	}
	if !sawPrimary {
		t.Fatal("no primary placed")
	}
}

func TestBuildEntriesSlotGridFilledNeverEmitsFillerOnlyWindow(t *testing.T) {
	media := []db.Media{mediaRow("e1", "", 20*60*1000)}
	filler := []SlotFiller{{MediaID: "f1", DurationMs: 60 * 1000}}

	// No primary fits a 10-minute window on a 30-minute grid.
	entries, err := BuildEntriesSlotGridFilled("ch", media, filler, 0, 10*60*1000, 30*60*1000)
	if err != nil {
		t.Fatalf("BuildEntriesSlotGridFilled: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries, want none: %+v", len(entries), entries)
	}
}

func TestBuildEntriesSlotGridFilledRejectsMisalignedFiller(t *testing.T) {
	media := []db.Media{mediaRow("e1", "", 24*60*1000)}

	_, err := BuildEntriesSlotGridFilled("ch", media, []SlotFiller{{MediaID: "f1", DurationMs: 5000}}, 0, 60*60*1000, 30*60*1000)
	if err == nil {
		t.Fatal("expected misaligned filler duration error")
	}
	_, err = BuildEntriesSlotGridFilled("ch", media, []SlotFiller{{MediaID: "f1", DurationMs: 60 * 1000, CursorMs: 3000}}, 0, 60*60*1000, 30*60*1000)
	if err == nil {
		t.Fatal("expected misaligned filler cursor error")
	}
}
