package packager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestCheckReadyPackageIntegrityResetsMissingFiles(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()

	root := t.TempDir()
	goodRoot := filepath.Join(root, "good")
	badRoot := filepath.Join(root, "bad")
	if err := os.MkdirAll(goodRoot, 0o755); err != nil {
		t.Fatalf("mkdir good: %v", err)
	}
	if err := os.MkdirAll(badRoot, 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	writePackageFiles(t, goodRoot, true)
	writePackageFiles(t, badRoot, false)

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('good', '/tmp/good.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('bad', '/tmp/bad.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	pkgDur := int64(12000)
	for _, pkg := range []db.MediaPackage{
		{
			ID:                 "pkg-good",
			MediaID:            "good",
			RenditionProfile:   db.DefaultPackageProfile,
			Status:             db.PackageStatusReady,
			PackageRoot:        &goodRoot,
			InitSegmentPath:    func() *string { s := filepath.Join(goodRoot, "init.mp4"); return &s }(),
			PackagedDurationMs: &pkgDur,
			CreatedAtMs:        1,
			UpdatedAtMs:        1,
		},
		{
			ID:                 "pkg-bad",
			MediaID:            "bad",
			RenditionProfile:   db.DefaultPackageProfile,
			Status:             db.PackageStatusReady,
			PackageRoot:        &badRoot,
			InitSegmentPath:    func() *string { s := filepath.Join(badRoot, "init.mp4"); return &s }(),
			PackagedDurationMs: &pkgDur,
			CreatedAtMs:        1,
			UpdatedAtMs:        1,
		},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
		if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
			{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr(filepath.Join(*pkg.PackageRoot, "seg0.m4s"))},
			{PackageID: pkg.ID, SegmentNumber: 1, MediaStartMs: 6000, DurationMs: 6000, Path: strptr(filepath.Join(*pkg.PackageRoot, "seg1.m4s"))},
		}); err != nil {
			t.Fatalf("insert segments: %v", err)
		}
	}

	reset, err := CheckReadyPackageIntegrity(context.Background(), conn)
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if reset != 1 {
		t.Fatalf("reset count=%d, want 1", reset)
	}

	good, err := db.MediaPackageByID(context.Background(), conn, "pkg-good")
	if err != nil || good == nil {
		t.Fatalf("lookup good: pkg=%v err=%v", good, err)
	}
	if good.Status != db.PackageStatusReady {
		t.Fatalf("good package status=%s, want ready", good.Status)
	}
	bad, err := db.MediaPackageByID(context.Background(), conn, "pkg-bad")
	if err != nil || bad == nil {
		t.Fatalf("lookup bad: pkg=%v err=%v", bad, err)
	}
	if bad.Status != db.PackageStatusPending || bad.Error == nil || bad.PackagedDurationMs != nil {
		t.Fatalf("bad package after reset=%+v, want pending with reason and no duration", bad)
	}
	segs, err := db.PackagedSegments(context.Background(), conn, "pkg-bad")
	if err != nil {
		t.Fatalf("bad segments: %v", err)
	}
	if len(segs) != 0 {
		t.Fatalf("bad segments should be cleared, got %+v", segs)
	}
}

func writePackageFiles(t *testing.T, root string, includeAllSegments bool) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "init.mp4"), []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	manifest := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6.000,\nseg0.m4s\n#EXTINF:6.000,\nseg1.m4s\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(root, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "seg0.m4s"), []byte("seg0"), 0o644); err != nil {
		t.Fatalf("write seg0: %v", err)
	}
	if includeAllSegments {
		if err := os.WriteFile(filepath.Join(root, "seg1.m4s"), []byte("seg1"), 0o644); err != nil {
			t.Fatalf("write seg1: %v", err)
		}
	}
}

func strptr(s string) *string { return &s }
