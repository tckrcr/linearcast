package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestPackageBytesFromTrackedFiles(t *testing.T) {
	conn, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	dir := t.TempDir()
	initPath := filepath.Join(dir, "init.mp4")
	seg0Path := filepath.Join(dir, "seg0.m4s")
	if err := os.WriteFile(initPath, make([]byte, 11), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if err := os.WriteFile(seg0Path, make([]byte, 17), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 8000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, init_segment_path, created_at_ms, updated_at_ms)
		VALUES ('pkg-1', 'm1', 'p', 'ready', ?, 0, 0)`, initPath); err != nil {
		t.Fatalf("insert package: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO packaged_segments (package_id, segment_number, media_start_ms, duration_ms, path)
		VALUES ('pkg-1', 0, 0, 4000, ?)`, seg0Path); err != nil {
		t.Fatalf("insert segment 0: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO packaged_segments (package_id, segment_number, media_start_ms, duration_ms, path, byte_range_length)
		VALUES ('pkg-1', 1, 4000, 4000, ?, 23)`, filepath.Join(dir, "byte-range.m4s")); err != nil {
		t.Fatalf("insert segment 1: %v", err)
	}

	got, err := packageBytesFromTrackedFiles(context.Background(), conn, "pkg-1", initPath)
	if err != nil {
		t.Fatalf("packageBytesFromTrackedFiles: %v", err)
	}
	if got != 51 {
		t.Fatalf("package bytes = %d, want 51", got)
	}
}
