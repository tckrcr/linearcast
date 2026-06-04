package db

import (
	"context"
	"testing"
)

func TestRecordPlayHistoryIsIdempotentAndQueryable(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, title, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 'Episode 1', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m2', '/tmp/m2.mkv', '/tmp', 'Episode 2', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	entries := []ScheduleEntry{
		{ID: "se1", ChannelID: "ch", StartMs: 6000, MediaID: "m1", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
		{ID: "se2", ChannelID: "ch", StartMs: 18000, MediaID: "m2", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
	}
	if _, err := InsertScheduleEntries(context.Background(), rw, entries); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	inserted, err := RecordPlayHistory(context.Background(), rw, entries[0])
	if err != nil || !inserted {
		t.Fatalf("record first inserted=%v err=%v", inserted, err)
	}
	inserted, err = RecordPlayHistory(context.Background(), rw, entries[0])
	if err != nil || inserted {
		t.Fatalf("record duplicate inserted=%v err=%v", inserted, err)
	}
	if inserted, err := RecordPlayHistory(context.Background(), rw, entries[1]); err != nil || !inserted {
		t.Fatalf("record second inserted=%v err=%v", inserted, err)
	}

	rows, err := PlayHistorySince(context.Background(), rw, "ch", 10000)
	if err != nil {
		t.Fatalf("history since: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].ScheduleEntryID != "se2" || rows[0].MediaTitle != "Episode 2" ||
		rows[0].StartedAtMs != 18000 || rows[0].EndedAtMs != 30000 || rows[0].DurationMs != 12000 {
		t.Fatalf("unexpected history row: %+v", rows[0])
	}

	if _, err := rw.Exec(`DELETE FROM schedule_entries WHERE id = 'se2'`); err != nil {
		t.Fatalf("delete schedule entry: %v", err)
	}
	rows, err = PlayHistorySince(context.Background(), rw, "ch", 10000)
	if err != nil {
		t.Fatalf("history after schedule delete: %v", err)
	}
	if len(rows) != 1 || rows[0].ScheduleEntryID != "se2" {
		t.Fatalf("history should survive schedule churn: %+v", rows)
	}
}
