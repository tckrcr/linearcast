package db

import (
	"context"
	"testing"
)

// TestLastPrimaryScheduleEntryUsesEntryKind verifies LastPrimaryScheduleEntry
// resolves "latest primary" from entry_kind, not channel_media membership: it
// skips a later filler entry, and treats a filler-asset media recorded as a
// primary entry as primary even though it is not in channel_media.
func TestLastPrimaryScheduleEntryUsesEntryKind(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	ctx := context.Background()
	if err := ApplySchema(ctx, rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('chA', 'A', '/tmp', 'alphabetical', 1, 0), ('chB', 'B', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('bumper', '/tmp/bumper.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	// chA: a primary m1 followed by a later filler bumper. The filler must be skipped.
	if _, err := InsertScheduleEntries(ctx, rw, []ScheduleEntry{
		{ID: "a1", ChannelID: "chA", StartMs: 0, MediaID: "m1", DurationMs: 6000, Kind: "primary"},
		{ID: "a2", ChannelID: "chA", StartMs: 6000, MediaID: "bumper", DurationMs: 6000, Kind: "filler"},
	}); err != nil {
		t.Fatalf("insert chA entries: %v", err)
	}
	got, err := LastPrimaryScheduleEntry(ctx, rw, "chA")
	if err != nil {
		t.Fatalf("LastPrimaryScheduleEntry chA: %v", err)
	}
	if got == nil || got.ID != "a1" {
		t.Fatalf("chA latest primary = %+v, want entry a1 (m1), skipping later filler", got)
	}

	// chB: bumper (a filler-asset media) recorded as a primary entry, not in any
	// channel_media. entry_kind is authoritative, so it counts as primary.
	if _, err := InsertScheduleEntries(ctx, rw, []ScheduleEntry{
		{ID: "b1", ChannelID: "chB", StartMs: 0, MediaID: "bumper", DurationMs: 6000, Kind: "primary"},
	}); err != nil {
		t.Fatalf("insert chB entries: %v", err)
	}
	got, err = LastPrimaryScheduleEntry(ctx, rw, "chB")
	if err != nil {
		t.Fatalf("LastPrimaryScheduleEntry chB: %v", err)
	}
	if got == nil || got.ID != "b1" {
		t.Fatalf("chB latest primary = %+v, want entry b1 (bumper recorded as primary)", got)
	}
}

func TestScheduleCheckRejectsBadAlignment(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
        VALUES ('ch1', 'x', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
        video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
        VALUES ('m1', '/tmp/m1.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	_, err = rw.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
        VALUES (lower(hex(randomblob(16))), 'ch1', 1234, 'm1', 0, 6000, 0)`)
	if err == nil {
		t.Fatalf("expected CHECK to reject misaligned start_ms")
	}
}
