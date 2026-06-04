package db

import (
	"testing"
)

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
