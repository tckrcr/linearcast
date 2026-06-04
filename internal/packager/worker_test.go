package packager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageid"
)

func newWorkerTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return path
}

func seedPackagedChannel(t *testing.T, path string) {
	t.Helper()
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled,
		created_at_ms, playback_mode, required_package_profile, package_prefill_ms)
		VALUES ('ch-pkg', 'Packaged', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 86400000),
		       ('ch-gen', 'Generated', '/tmp', 'alphabetical', 1, 0, 'generated', NULL, NULL)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m2', '/tmp/m2.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-bad', '/tmp/bad.mkv', '/tmp', 1200000, 'mkv', 'hevc', 2160, 'aac', 0, 0),
		       ('m-gen-only', '/tmp/g.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch-pkg', 'm1', NULL, 0),
		       ('ch-pkg', 'm2', 'm1', 0),
		       ('ch-pkg', 'm-bad', 'm2', 0),
		       ('ch-gen', 'm-gen-only', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}
	if _, err := db.EnsureLocalEncoder(context.Background(), conn, "Local Worker", 1); err != nil {
		t.Fatalf("ensure local encoder: %v", err)
	}
}

func TestDiscoverCandidatesForEnabledChannels(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	conn, err := db.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer conn.Close()

	got, err := DiscoverCandidates(context.Background(), conn)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 candidates (m1, m2, m-gen-only), got %+v", got)
	}
	want := map[string]bool{"m1": false, "m2": false, "m-gen-only": false}
	for _, c := range got {
		if c.Profile != "h264-main-1080p" {
			t.Fatalf("candidate = %+v, want h264-main-1080p profile", c)
		}
		if _, ok := want[c.MediaID]; !ok {
			t.Fatalf("unexpected candidate = %+v", c)
		}
		want[c.MediaID] = true
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("missing candidate %s in %+v", id, got)
		}
	}
}

func TestDiscoverSkipsReadyAndProcessing(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('p-m1', 'm1', 'h264-main-1080p', 'ready', 0, 0),
		       ('p-m2', 'm2', 'h264-main-1080p', 'processing', 0, 0)`); err != nil {
		t.Fatalf("seed packages: %v", err)
	}

	got, err := DiscoverCandidates(context.Background(), rw)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1 || got[0].MediaID != "m-gen-only" {
		t.Fatalf("want m-gen-only candidate, got %+v", got)
	}
}

func TestDiscoverIncludesPendingAndSkipsFailed(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('p-m1', 'm1', 'h264-main-1080p', 'pending', 0, 0),
		       ('p-m2', 'm2', 'h264-main-1080p', 'failed', 0, 0)`); err != nil {
		t.Fatalf("seed packages: %v", err)
	}

	got, err := DiscoverCandidates(context.Background(), rw)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %+v", got)
	}
	for _, c := range got {
		if c.MediaID == "m2" {
			t.Fatalf("failed package should wait for explicit retry, got %+v", got)
		}
	}
}

func TestDiscoverIncludesOrphanPendingAfterChannelCandidates(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('orphan-pending', '/tmp/orphan-pending.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('orphan-failed', '/tmp/orphan-failed.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('orphan-ready', '/tmp/orphan-ready.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('orphan-bad', '/tmp/orphan-bad.mkv', '/tmp', 1200000, 'mkv', 'hevc', 2160, 'aac', 0, 0)`); err != nil {
		t.Fatalf("insert orphan media: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('p-orphan-pending', 'orphan-pending', 'h264-main-1080p', 'pending', 10, 10),
		       ('p-orphan-failed', 'orphan-failed', 'h264-main-1080p', 'failed', 20, 20),
		       ('p-orphan-ready', 'orphan-ready', 'h264-main-1080p', 'ready', 30, 30),
		       ('p-orphan-bad', 'orphan-bad', 'h264-main-1080p', 'pending', 40, 40)`); err != nil {
		t.Fatalf("insert orphan packages: %v", err)
	}

	got, err := DiscoverCandidates(context.Background(), rw)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 candidates, got %+v", got)
	}
	channelIDs := map[string]bool{"m1": true, "m2": true, "m-gen-only": true}
	for _, c := range got[:3] {
		if !channelIDs[c.MediaID] {
			t.Fatalf("channel candidates should come first, got %+v", got[:3])
		}
	}
	if got[3].MediaID != "orphan-pending" {
		t.Fatalf("orphan candidates mismatch: %+v", got[3:])
	}
}

func TestDiscoverSkipsOrphanRowsForUnavailableProfiles(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('orphan-typo', '/tmp/orphan-typo.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert orphan media: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('p-orphan-typo', 'orphan-typo', 'h264-maindfdfd-1080p', 'failed', 10, 10)`); err != nil {
		t.Fatalf("insert typo package: %v", err)
	}

	got, err := DiscoverCandidates(context.Background(), rw)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, c := range got {
		if c.MediaID == "orphan-typo" {
			t.Fatalf("typo-profile orphan should not be discovered: %+v", got)
		}
	}
}

func TestDiscoverSkipsMusicMediaOnVideoProfile(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms, media_kind)
		VALUES ('music-1', '/music/album/01.flac', '/music/album', 240000, 'flac', '', 0, 'flac', 1, 0, 'music')`); err != nil {
		t.Fatalf("insert music media: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch-pkg', 'music-1', 'm-bad', 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}

	got, err := DiscoverCandidates(context.Background(), rw)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, c := range got {
		if c.MediaID == "music-1" {
			t.Fatalf("music media should not be packaged with video profile: %+v", got)
		}
	}
}

func TestDiscoverIncludesMusicMediaOnMusicChannel(t *testing.T) {
	path := newWorkerTestDB(t)
	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled,
		created_at_ms, playback_mode, required_package_profile, package_prefill_ms, media_kind)
		VALUES ('music-ch', 'Music', '/music', 'alphabetical', 1, 0, 'packaged', 'music-aac-720p', 86400000, 'music')`); err != nil {
		t.Fatalf("insert music channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms, media_kind)
		VALUES ('music-1', '/music/album/01.flac', '/music/album', 240000, 'flac', '', 0, 'flac', 1, 0, 'music')`); err != nil {
		t.Fatalf("insert music media: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('music-ch', 'music-1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}

	got, err := DiscoverCandidates(context.Background(), rw)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1 || got[0].MediaID != "music-1" || got[0].Profile != "music-aac-720p" {
		t.Fatalf("music candidate mismatch: %+v", got)
	}
}

func TestTryClaimInsertsThenRejectsDouble(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	w := &Worker{DB: rw}
	ctx := context.Background()

	ok, err := w.tryClaim(ctx, "m1", "h264-main-1080p", 100)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	ok, err = w.tryClaim(ctx, "m1", "h264-main-1080p", 200)
	if err != nil {
		t.Fatalf("second claim err: %v", err)
	}
	if ok {
		t.Fatalf("second claim should fail (already processing)")
	}

	pkg, err := db.MediaPackageByID(context.Background(), rw, packageid.For("m1", "h264-main-1080p"))
	if err != nil || pkg == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", pkg, err)
	}
	if pkg.Status != db.PackageStatusProcessing {
		t.Fatalf("status = %q, want processing", pkg.Status)
	}
}

func TestTryClaimTransitionsFailedToProcessing(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, error, created_at_ms, updated_at_ms)
		VALUES ('p-m1', 'm1', 'h264-main-1080p', 'failed', 'old error', 0, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := &Worker{DB: rw}
	ok, err := w.tryClaim(context.Background(), "m1", "h264-main-1080p", 500)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	pkg, err := db.MediaPackageByID(context.Background(), rw, "p-m1")
	if err != nil || pkg == nil {
		t.Fatalf("lookup: %v", err)
	}
	if pkg.Status != db.PackageStatusProcessing {
		t.Fatalf("status = %q, want processing", pkg.Status)
	}
	if pkg.Error != nil {
		t.Fatalf("error should be cleared on retransition, got %q", *pkg.Error)
	}
}

func TestTryClaimSkipsReady(t *testing.T) {
	path := newWorkerTestDB(t)
	seedPackagedChannel(t, path)

	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('p-m1', 'm1', 'h264-main-1080p', 'ready', 0, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := &Worker{DB: rw}
	ok, err := w.tryClaim(context.Background(), "m1", "h264-main-1080p", 500)
	if err != nil {
		t.Fatalf("claim err: %v", err)
	}
	if ok {
		t.Fatalf("claim succeeded on ready row")
	}
}

func TestIntegrityLoopPeriodicallyResetsBrokenReadyPackage(t *testing.T) {
	path := newWorkerTestDB(t)
	rw, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	root := t.TempDir()
	writePackageFiles(t, root, true)
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	initPath := filepath.Join(root, "init.mp4")
	pkgDur := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-ready",
		MediaID:            "m-ready",
		RenditionProfile:   db.DefaultPackageProfile,
		Status:             db.PackageStatusReady,
		PackageRoot:        &root,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
		CreatedAtMs:        1,
		UpdatedAtMs:        1,
	}
	if err := db.UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "seg1.m4s")); err != nil {
		t.Fatalf("remove segment: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &Worker{DB: rw, IntegrityInterval: 10 * time.Millisecond}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.integrityLoop(ctx)
	}()

	deadline := time.After(2 * time.Second)
	for {
		got, err := db.MediaPackageByID(context.Background(), rw, "pkg-ready")
		if err != nil || got == nil {
			t.Fatalf("lookup package: pkg=%v err=%v", got, err)
		}
		if got.Status == db.PackageStatusPending {
			if got.Error == nil || got.PackagedDurationMs != nil {
				t.Fatalf("reset package=%+v, want error reason and cleared duration", got)
			}
			cancel()
			<-done
			return
		}
		select {
		case <-deadline:
			t.Fatalf("package was not reset, final status=%s", got.Status)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
