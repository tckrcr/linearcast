package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestScheduleEntriesOrderedReturnsChainOrder(t *testing.T) {
	conn, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	seedScheduleChainChannel(t, conn, "ch")
	for _, mediaID := range []string{"m1", "m2", "m3"} {
		seedScheduleChainMedia(t, conn, mediaID, 6000)
	}
	if _, err := conn.Exec(`
		INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES
			('se3', 'ch', 12000, 'm3', 0, 6000, 'se2', 0),
			('se1', 'ch', 0, 'm1', 0, 6000, NULL, 0),
			('se2', 'ch', 6000, 'm2', 0, 6000, 'se1', 0)
	`); err != nil {
		t.Fatalf("insert schedule chain: %v", err)
	}

	ordered, err := ScheduleEntriesOrdered(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("ordered schedule: %v", err)
	}
	if got, want := len(ordered), 3; got != want {
		t.Fatalf("len(ordered)=%d, want %d", got, want)
	}
	wantIDs := []string{"se1", "se2", "se3"}
	for i, wantID := range wantIDs {
		if ordered[i].ID != wantID {
			t.Fatalf("ordered[%d].ID=%s, want %s", i, ordered[i].ID, wantID)
		}
	}
	issues, err := ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("validate chains returned issues: %+v", issues)
	}
}

func TestScheduleEntryAnchorIndexesPreventMultipleHeads(t *testing.T) {
	conn, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	seedScheduleChainChannel(t, conn, "ch")
	seedScheduleChainMedia(t, conn, "m1", 6000)
	seedScheduleChainMedia(t, conn, "m2", 6000)
	if _, err := conn.Exec(`
		INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES ('se1', 'ch', 0, 'm1', 0, 6000, NULL, 0)
	`); err != nil {
		t.Fatalf("insert head: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES ('se2', 'ch', 6000, 'm2', 0, 6000, NULL, 0)
	`); err == nil {
		t.Fatal("duplicate head insert succeeded, want unique constraint failure")
	}
	if _, err := conn.Exec(`DROP INDEX IF EXISTS idx_schedule_entries_head`); err != nil {
		t.Fatalf("drop head index: %v", err)
	}
	if _, err := conn.Exec(`DROP INDEX IF EXISTS idx_schedule_entries_anchor`); err != nil {
		t.Fatalf("drop anchor index: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES ('se2', 'ch', 6000, 'm2', 0, 6000, NULL, 0)
	`); err != nil {
		t.Fatalf("insert broken chain after dropping indexes: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES ('se3', 'ch', 12000, 'm2', 0, 6000, NULL, 0)
	`); err != nil {
		t.Fatalf("insert broken chain after dropping indexes: %v", err)
	}

	issues, err := ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if got, want := len(issues), 1; got != want {
		t.Fatalf("len(issues)=%d, want %d: %+v", got, want, issues)
	}
	if issues[0].Kind != ScheduleChainIssueMultipleHeads {
		t.Fatalf("issue kind=%s, want %s", issues[0].Kind, ScheduleChainIssueMultipleHeads)
	}
}

func TestDeleteScheduleEntryByIDStitchesSuccessor(t *testing.T) {
	conn, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	entries := seedScheduleChainFixtures(t, conn)
	found, err := DeleteScheduleEntryByID(context.Background(), conn, entries[1].ID)
	if err != nil {
		t.Fatalf("delete entry: %v", err)
	}
	if !found {
		t.Fatal("delete entry reported not found")
	}

	ordered, err := ScheduleEntriesOrdered(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("ordered schedule: %v", err)
	}
	if got, want := len(ordered), 2; got != want {
		t.Fatalf("len(ordered)=%d, want %d", got, want)
	}
	if ordered[1].ID != entries[2].ID {
		t.Fatalf("tail id=%s, want %s", ordered[1].ID, entries[2].ID)
	}
	if ordered[1].AnchorScheduleEntryID == nil || *ordered[1].AnchorScheduleEntryID != entries[0].ID {
		t.Fatalf("tail anchor=%+v, want %s", ordered[1].AnchorScheduleEntryID, entries[0].ID)
	}
	issues, err := ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("validate chains returned issues: %+v", issues)
	}
}

func TestDeleteScheduleRangeIntersectStitchesFromSurvivingPredecessor(t *testing.T) {
	conn, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	entries := seedScheduleChainFixtures(t, conn)
	deleted, err := DeleteScheduleRangeIntersect(context.Background(), conn, "ch", 12000, 18000)
	if err != nil {
		t.Fatalf("delete range: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted)
	}

	tail, err := ScheduleEntryByID(context.Background(), conn, entries[2].ID)
	if err != nil {
		t.Fatalf("lookup tail: %v", err)
	}
	if tail == nil {
		t.Fatal("tail entry missing after range delete")
	}
	if tail.AnchorScheduleEntryID == nil || *tail.AnchorScheduleEntryID != entries[0].ID {
		t.Fatalf("tail anchor=%+v, want %s", tail.AnchorScheduleEntryID, entries[0].ID)
	}
	issues, err := ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("validate chains returned issues: %+v", issues)
	}
}

func TestScheduleWindowAndLastScheduleEntryUseChainOrder(t *testing.T) {
	conn, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	seedScheduleChainChannel(t, conn, "ch")
	seedScheduleChainMedia(t, conn, "m1", 6000)
	seedScheduleChainMedia(t, conn, "m2", 6000)
	seedScheduleChainMedia(t, conn, "m3", 6000)
	if _, err := conn.Exec(`
		INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES
			('se1', 'ch', 12000, 'm1', 0, 6000, NULL, 0),
			('se3', 'ch', 6000, 'm3', 0, 6000, 'se2', 0),
			('se2', 'ch', 0, 'm2', 0, 6000, 'se1', 0)
	`); err != nil {
		t.Fatalf("insert schedule chain: %v", err)
	}

	window, err := ScheduleWindow(context.Background(), conn, "ch", 0, 18000)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	wantIDs := []string{"se1", "se2", "se3"}
	if got, want := len(window), len(wantIDs); got != want {
		t.Fatalf("len(window)=%d, want %d", got, want)
	}
	for i, wantID := range wantIDs {
		if window[i].ID != wantID {
			t.Fatalf("window[%d].ID=%s, want %s", i, window[i].ID, wantID)
		}
	}

	enriched, err := ScheduleWindowEnriched(context.Background(), conn, "ch", 0, 18000)
	if err != nil {
		t.Fatalf("schedule window enriched: %v", err)
	}
	if got, want := len(enriched), len(wantIDs); got != want {
		t.Fatalf("len(enriched)=%d, want %d", got, want)
	}
	for i, wantID := range wantIDs {
		if enriched[i].ID != wantID {
			t.Fatalf("enriched[%d].ID=%s, want %s", i, enriched[i].ID, wantID)
		}
		if enriched[i].Path != "/tmp/"+enriched[i].MediaID+".mp4" {
			t.Fatalf("enriched[%d].Path=%s, want media path", i, enriched[i].Path)
		}
	}

	last, err := LastScheduleEntry(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("last schedule entry: %v", err)
	}
	if last == nil || last.ID != "se3" {
		t.Fatalf("last schedule entry=%+v, want se3 tail", last)
	}
}

func seedScheduleChainFixtures(t *testing.T, conn *sql.DB) []ScheduleEntry {
	t.Helper()
	seedScheduleChainChannel(t, conn, "ch")
	seedScheduleChainMedia(t, conn, "m1", 6000)
	seedScheduleChainMedia(t, conn, "m2", 12000)
	seedScheduleChainMedia(t, conn, "m3", 6000)
	entries := []ScheduleEntry{
		{ID: "se1", ChannelID: "ch", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ID: "se2", ChannelID: "ch", StartMs: 6000, MediaID: "m2", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
		{ID: "se3", ChannelID: "ch", StartMs: 18000, MediaID: "m3", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
	}
	if _, err := InsertScheduleEntries(context.Background(), conn, entries); err != nil {
		t.Fatalf("insert schedule chain: %v", err)
	}
	return entries
}

func seedScheduleChainChannel(t *testing.T, conn *sql.DB, id string) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES (?, ?, '/tmp', 'alphabetical', 1, 0)`, id, id); err != nil {
		t.Fatalf("insert channel %s: %v", id, err)
	}
}

func seedScheduleChainMedia(t *testing.T, conn *sql.DB, id string, durationMs int64) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES (?, ?, '/tmp', ?, 'mp4', 'h264', 1080, 'aac', 1, 0)`, id, "/tmp/"+id+".mp4", durationMs); err != nil {
		t.Fatalf("insert media %s: %v", id, err)
	}
}
