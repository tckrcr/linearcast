package db

import (
	"context"
	"testing"
)

func TestLoadGroupHistory(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
        VALUES ('ch1', 'x', '/tmp', 'block', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, scheduling_group, duration_ms, container,
        video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
        VALUES ('m1', '/tmp/m1.mkv', '/tmp', 'GroupA', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
               ('m2', '/tmp/m2.mkv', '/tmp', 'GroupA', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
               ('m3', '/tmp/m3.mkv', '/tmp', 'GroupB', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
               ('m4', '/tmp/m4.mkv', '/tmp', NULL,     6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := InsertScheduleEntries(context.Background(), rw, []ScheduleEntry{
		{ChannelID: "ch1", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ChannelID: "ch1", StartMs: 6000, MediaID: "m2", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ChannelID: "ch1", StartMs: 12000, MediaID: "m3", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ChannelID: "ch1", StartMs: 18000, MediaID: "m4", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	cursors, recent, err := LoadGroupHistory(context.Background(), rw, "ch1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if recent != "" {
		t.Errorf("recent group should be \"\" (NULL group of m4), got %q", recent)
	}
	if cursors["GroupA"].LastMediaID != "m2" || cursors["GroupA"].LastEndMs != 12000 {
		t.Errorf("GroupA cursor wrong: %+v (want m2, 12000)", cursors["GroupA"])
	}
	if cursors["GroupB"].LastMediaID != "m3" || cursors["GroupB"].LastEndMs != 18000 {
		t.Errorf("GroupB cursor wrong: %+v (want m3, 18000)", cursors["GroupB"])
	}
}
