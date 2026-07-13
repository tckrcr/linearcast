package db

import (
	"context"
	"testing"
)

func TestProfileRealizedTotalBitrate(t *testing.T) {
	conn, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	for _, id := range []string{"m1", "m2", "m3"} {
		if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', 8000, 'mkv', 'h264', 1080, 'aac', 1, 0)`, id, "/tmp/"+id+".mkv"); err != nil {
			t.Fatalf("insert media %s: %v", id, err)
		}
	}

	insertPackage := func(id, mediaID, profile string, status PackageStatus, durationMs int64, bytes *int64) {
		t.Helper()
		pkg := MediaPackage{
			ID:                 id,
			MediaID:            mediaID,
			RenditionProfile:   profile,
			Status:             status,
			PackagedDurationMs: &durationMs,
			PackageBytes:       bytes,
			CreatedAtMs:        0,
			UpdatedAtMs:        0,
		}
		if err := UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("insert package %s: %v", id, err)
		}
	}
	b1 := int64(5_000_000)
	b2 := int64(7_000_000)
	insertPackage("pkg-1", "m1", "p", PackageStatusReady, 8000, &b1)
	insertPackage("pkg-2", "m2", "p", PackageStatusReady, 8000, &b2)
	insertPackage("pkg-processing", "m3", "p", PackageStatusProcessing, 8000, &b2)
	insertPackage("pkg-nosize", "m3", "other", PackageStatusReady, 8000, nil)

	gotBps, gotN, err := ProfileRealizedTotalBitrate(context.Background(), conn, "p")
	if err != nil {
		t.Fatalf("ProfileRealizedTotalBitrate: %v", err)
	}
	if gotBps != 6_000_000 || gotN != 2 {
		t.Fatalf("ProfileRealizedTotalBitrate = %d/%d, want 6000000/2", gotBps, gotN)
	}

	gotBps, gotN, err = ProfileRealizedTotalBitrate(context.Background(), conn, "other")
	if err != nil {
		t.Fatalf("ProfileRealizedTotalBitrate other: %v", err)
	}
	if gotBps != 0 || gotN != 0 {
		t.Fatalf("empty aggregate = %d/%d, want 0/0", gotBps, gotN)
	}
}
