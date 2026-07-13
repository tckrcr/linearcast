package packager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/layout"
)

const insertMediaCols = `INSERT INTO media (id, path, directory, duration_ms, container,
	video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
	VALUES (?, ?, '/tmp', ?, 'mkv', 'h264', 1080, 'aac', 1, 0)`

// writePackageAt lays down a stub finalized package at
// outputRoot/<mediaID>/<dirName> for the hermetic (no-ffmpeg) import tests.
func writePackageAt(t *testing.T, outputRoot, mediaID, dirName string) string {
	t.Helper()
	pkgRoot := filepath.Join(outputRoot, mediaID, dirName)
	if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir package root: %v", err)
	}
	writePackageFiles(t, pkgRoot, true)
	return pkgRoot
}

func TestResolvePackageIdentity(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	profiles, err := db.AllPackageProfileNames(context.Background(), conn)
	if err != nil {
		t.Fatalf("profiles: %v", err)
	}
	profileSet := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		profileSet[p] = true
	}

	// A directory named after an active profile resolves to that profile.
	profile, ok := resolvePackageIdentity(db.DefaultPackageProfile, profileSet)
	if !ok {
		t.Fatalf("bare profile dir did not resolve")
	}
	if profile != db.DefaultPackageProfile {
		t.Fatalf("resolved %s, want %s", profile, db.DefaultPackageProfile)
	}

	// An unrecognized directory name resolves to nothing.
	if _, ok := resolvePackageIdentity("not-a-package-dir", profileSet); ok {
		t.Fatalf("unknown dir name unexpectedly resolved")
	}
}

func TestImportPackagesSkipsLegacyIdentityNamedDirs(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(insertMediaCols, "m1", "/tmp/m1.mkv", 12000); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	outputRoot := t.TempDir()
	legacyRoot := writePackageAt(t, outputRoot, "m1", "m1-h264-1080p-8mbps-burn-forced-disposition-s2-eng")
	rep, err := ImportPackages(context.Background(), conn, ImportOptions{OutputRoot: outputRoot})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Scanned != 1 || len(rep.Imported) != 0 || rep.AlreadyReady != 0 || len(rep.NeedsMedia) != 0 {
		t.Fatalf("rep = %+v, want one skipped legacy identity dir", rep)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].Path != legacyRoot || rep.Skipped[0].Reason != "no active profile matches directory name" {
		t.Fatalf("skipped = %+v, want legacy identity dir skipped", rep.Skipped)
	}
	var rows int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM media_packages`).Scan(&rows); err != nil {
		t.Fatalf("count media packages: %v", err)
	}
	if rows != 0 {
		t.Fatalf("media_packages rows = %d, want 0", rows)
	}
}

func TestImportPackagesNeedsMediaWhenRowMissing(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	outputRoot := t.TempDir()
	writePackageAt(t, outputRoot, "ghost", db.DefaultPackageProfile)

	rep, err := ImportPackages(context.Background(), conn, ImportOptions{OutputRoot: outputRoot})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(rep.Imported) != 0 {
		t.Fatalf("imported=%d, want 0", len(rep.Imported))
	}
	if len(rep.NeedsMedia) != 1 || rep.NeedsMedia[0] != "ghost" {
		t.Fatalf("needs_media=%v, want [ghost]", rep.NeedsMedia)
	}
}

func TestImportPackagesSkipsAlreadyReady(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(insertMediaCols, "m1", "/tmp/m1.mkv", 12000); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	outputRoot := t.TempDir()
	pkgRoot := writePackageAt(t, outputRoot, "m1", db.DefaultPackageProfile)
	initPath := filepath.Join(pkgRoot, "init.mp4")
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:               layout.ID("m1", db.DefaultPackageProfile),
		MediaID:          "m1",
		RenditionProfile: db.DefaultPackageProfile,
		Status:           db.PackageStatusReady,
		PackageRoot:      &pkgRoot,
		InitSegmentPath:  &initPath,
		SegmentBasePath:  pkgRoot,
		Container:        "fmp4",
		CreatedAtMs:      1,
		UpdatedAtMs:      1,
	}); err != nil {
		t.Fatalf("seed ready package: %v", err)
	}

	rep, err := ImportPackages(context.Background(), conn, ImportOptions{OutputRoot: outputRoot})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.AlreadyReady != 1 || len(rep.Imported) != 0 {
		t.Fatalf("rep = %+v, want already_ready=1 imported=0", rep)
	}
}

func TestImportPackagesIgnoresNonPackageDirs(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(insertMediaCols, "m1", "/tmp/m1.mkv", 12000); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	outputRoot := t.TempDir()
	// A directory with no stream.m3u8 is not a package.
	if err := os.MkdirAll(filepath.Join(outputRoot, "m1", "leftover"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A legacy per-media subtitle sidecar dir is not a package profile, even if
	// a playlist-named file somehow ends up inside it.
	subtitleDir := filepath.Join(outputRoot, "m1", layout.SubtitlesDirName)
	if err := os.MkdirAll(subtitleDir, 0o755); err != nil {
		t.Fatalf("mkdir subtitles: %v", err)
	}
	if err := os.WriteFile(layout.PlaylistPath(subtitleDir), []byte("#EXTM3U"), 0o644); err != nil {
		t.Fatalf("write playlist: %v", err)
	}

	rep, err := ImportPackages(context.Background(), conn, ImportOptions{OutputRoot: outputRoot})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Scanned != 1 || len(rep.Imported) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("rep = %+v, want scanned=1 imported=0 skipped=1", rep)
	}
}

// TestImportPackagesRebuildsFromDiskEndToEnd builds a real fMP4 HLS package with
// ffmpeg, then imports it into a fresh DB with only a media row present —
// rebuilding the package + segment rows from the files, no encode. Skips where
// ffmpeg/ffprobe are unavailable.
func TestImportPackagesRebuildsFromDiskEndToEnd(t *testing.T) {
	ffmpeg, err := ffmpegexec.Resolve("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	if _, err := ffmpegexec.Resolve("ffprobe"); err != nil {
		t.Skipf("ffprobe unavailable: %v", err)
	}

	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(insertMediaCols, "m-e2e", "/tmp/m-e2e.mkv", 4000); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	outputRoot := t.TempDir()
	pkgRoot := filepath.Join(outputRoot, "m-e2e", db.DefaultPackageProfile)
	if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir package root: %v", err)
	}
	cmd := exec.Command(ffmpeg, "-v", "error",
		"-f", "lavfi", "-i", "testsrc=duration=4:size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		"-hls_time", "2", "-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(pkgRoot, "seg%06d.m4s"),
		"-hls_playlist_type", "vod", filepath.Join(pkgRoot, "stream.m3u8"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg package: %v: %s", err, out)
	}

	rep, err := ImportPackages(context.Background(), conn, ImportOptions{OutputRoot: outputRoot})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(rep.Imported) != 1 {
		t.Fatalf("imported=%d skipped=%+v, want 1", len(rep.Imported), rep.Skipped)
	}
	if rep.Imported[0].SegmentCount == 0 {
		t.Fatalf("imported with 0 segments: %+v", rep.Imported[0])
	}

	packageID := layout.ID("m-e2e", db.DefaultPackageProfile)
	ready, err := db.ReadyMediaPackage(context.Background(), conn, "m-e2e", db.DefaultPackageProfile)
	if err != nil {
		t.Fatalf("lookup ready: %v", err)
	}
	if ready == nil {
		t.Fatalf("package not ready after import")
	}
	var segCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM packaged_segments WHERE package_id = ?`, packageID).Scan(&segCount); err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if segCount != rep.Imported[0].SegmentCount {
		t.Fatalf("packaged_segments rows=%d, want %d", segCount, rep.Imported[0].SegmentCount)
	}

	// Re-running is a clean no-op.
	rep2, err := ImportPackages(context.Background(), conn, ImportOptions{OutputRoot: outputRoot})
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if rep2.AlreadyReady != 1 || len(rep2.Imported) != 0 {
		t.Fatalf("rerun rep = %+v, want already_ready=1 imported=0", rep2)
	}
}
