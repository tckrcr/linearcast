package db

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestMediaPackageAndPackagedSegments(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
        video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
        VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12013, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	vw := int64(1920)
	vh := int64(1080)
	ts := int64(90000)
	pkgRoot := "/cache/packages/m1/h264-main-1080p"
	initPath := "/cache/packages/m1/h264-main-1080p/init.mp4"
	pkgDur := int64(12012)
	pkg := MediaPackage{
		ID:                 "pkg-m1-h264-main",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             PackageStatusReady,
		PackageRoot:        &pkgRoot,
		InitSegmentPath:    &initPath,
		SegmentBasePath:    "/cache/packages/m1/h264-main-1080p/segments",
		Container:          "fmp4",
		VideoCodec:         "h264",
		VideoProfile:       "main",
		VideoWidth:         &vw,
		VideoHeight:        &vh,
		AudioCodec:         "aac",
		Timescale:          &ts,
		PackagedDurationMs: &pkgDur,
		CreatedAtMs:        100,
		UpdatedAtMs:        200,
	}
	if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}

	got, err := MediaPackageByID(context.Background(), rw, pkg.ID)
	if err != nil {
		t.Fatalf("package by id: %v", err)
	}
	if got == nil || got.Status != PackageStatusReady || got.Timescale == nil || *got.Timescale != 90000 || *got.PackagedDurationMs != 12012 {
		t.Fatalf("package mismatch: %+v", got)
	}

	pkgs, err := MediaPackagesForMedia(context.Background(), rw, "m1")
	if err != nil {
		t.Fatalf("packages for media: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].ID != pkg.ID {
		t.Fatalf("packages for media mismatch: %+v", pkgs)
	}

	seg0Path := "/cache/packages/m1/h264-main-1080p/segments/0.m4s"
	brStart := int64(1024)
	brLength := int64(2048)
	segments := []PackagedSegment{
		{
			PackageID:     pkg.ID,
			SegmentNumber: 0,
			MediaStartMs:  0,
			DurationMs:    6006,
			Path:          &seg0Path,
		},
		{
			PackageID:       pkg.ID,
			SegmentNumber:   1,
			MediaStartMs:    6006,
			DurationMs:      6006,
			ByteRangeStart:  &brStart,
			ByteRangeLength: &brLength,
		},
	}
	if err := ReplacePackagedSegments(context.Background(), rw, pkg.ID, segments); err != nil {
		t.Fatalf("replace packaged segments: %v", err)
	}

	gotSegments, err := PackagedSegments(context.Background(), rw, pkg.ID)
	if err != nil {
		t.Fatalf("packaged segments: %v", err)
	}
	if len(gotSegments) != 2 {
		t.Fatalf("expected 2 segments, got %+v", gotSegments)
	}
	if gotSegments[0].DurationMs != 6006 || gotSegments[0].Path == nil {
		t.Fatalf("segment 0 mismatch: %+v", gotSegments[0])
	}
	if gotSegments[1].MediaStartMs != 6006 || gotSegments[1].ByteRangeStart == nil || *gotSegments[1].ByteRangeStart != 1024 || gotSegments[1].ByteRangeLength == nil || *gotSegments[1].ByteRangeLength != 2048 {
		t.Fatalf("segment 1 mismatch: %+v", gotSegments[1])
	}

	ready, err := ReadyMediaPackage(context.Background(), rw, "m1", "h264-main-1080p")
	if err != nil {
		t.Fatalf("ready package: %v", err)
	}
	if ready == nil || ready.ID != pkg.ID {
		t.Fatalf("ready package mismatch: %+v", ready)
	}

	covered, err := PackagedSegmentAt(context.Background(), rw, pkg.ID, 7000)
	if err != nil {
		t.Fatalf("segment at: %v", err)
	}
	if covered == nil || covered.SegmentNumber != 1 {
		t.Fatalf("segment at 7000 mismatch: %+v", covered)
	}

	from, err := PackagedSegmentsFrom(context.Background(), rw, pkg.ID, 2000, 5)
	if err != nil {
		t.Fatalf("segments from: %v", err)
	}
	if len(from) != 2 || from[0].SegmentNumber != 0 || from[1].SegmentNumber != 1 {
		t.Fatalf("segments from mismatch: %+v", from)
	}

	byNumber, err := PackagedSegmentByNumber(context.Background(), rw, pkg.ID, 1)
	if err != nil {
		t.Fatalf("segment by number: %v", err)
	}
	if byNumber == nil || byNumber.MediaStartMs != 6006 {
		t.Fatalf("segment by number mismatch: %+v", byNumber)
	}
}

func TestChannelPackageNeedSummariesCountsMissingAndInFlight(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch1', 'Channel One', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p'),
		       ('disabled', 'Disabled', '/tmp', 'alphabetical', 0, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-ready', '/tmp/ready.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-processing', '/tmp/processing.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-pending', '/tmp/pending.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-failed', '/tmp/failed.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-missing', '/tmp/missing.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-codec-fail', '/tmp/codec-fail.mkv', '/tmp', 6000, 'mkv', 'hevc', 1080, 'aac', 0, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm-ready', NULL, 0),
		       ('ch1', 'm-processing', 'm-ready', 0),
		       ('ch1', 'm-pending', 'm-processing', 0),
		       ('ch1', 'm-failed', 'm-pending', 0),
		       ('ch1', 'm-missing', 'm-failed', 0),
		       ('ch1', 'm-codec-fail', 'm-missing', 0),
		       ('disabled', 'm-ready', NULL, 0)`); err != nil {
		t.Fatalf("insert channel media: %v", err)
	}
	pkgDur6k := int64(6000)
	for _, pkg := range []MediaPackage{
		{ID: "pkg-ready", MediaID: "m-ready", RenditionProfile: "h264-main-1080p", Status: PackageStatusReady, PackagedDurationMs: &pkgDur6k},
		{ID: "pkg-processing", MediaID: "m-processing", RenditionProfile: "h264-main-1080p", Status: PackageStatusProcessing},
		{ID: "pkg-pending", MediaID: "m-pending", RenditionProfile: "h264-main-1080p", Status: PackageStatusPending},
		{ID: "pkg-failed", MediaID: "m-failed", RenditionProfile: "h264-main-1080p", Status: PackageStatusFailed},
		{ID: "pkg-other-profile", MediaID: "m-missing", RenditionProfile: "custom-main-720p", Status: PackageStatusReady, PackagedDurationMs: &pkgDur6k},
	} {
		pkg.CreatedAtMs = 100
		pkg.UpdatedAtMs = 200
		if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}

	rows, err := ChannelPackageNeedSummaries(context.Background(), rw)
	if err != nil {
		t.Fatalf("need summaries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one enabled channel summary, got %+v", rows)
	}
	got := rows[0]
	if got.ChannelID != "ch1" || got.RenditionProfile != "h264-main-1080p" {
		t.Fatalf("unexpected summary identity: %+v", got)
	}
	if got.NeededCount != 5 || got.ReadyCount != 1 || got.ProcessingCount != 1 ||
		got.PendingCount != 1 || got.FailedCount != 1 || got.MissingCount != 1 {
		t.Fatalf("unexpected summary counts: %+v", got)
	}
}

func TestClaimPackageStateTransitions(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	ctx := context.Background()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := EnsureLocalEncoder(ctx, rw, "Local Worker", 1); err != nil {
		t.Fatalf("ensure local encoder: %v", err)
	}

	claim := func(nowMs int64) (bool, error) {
		return ClaimPackage(ctx, rw, ClaimRequest{
			MediaID: "m1", Profile: "h264-main-1080p", PackageID: "pkg-m1", NowMs: nowMs,
		})
	}
	ok, err := claim(100)
	if err != nil || !ok {
		t.Fatalf("claim missing: ok=%v err=%v", ok, err)
	}
	ok, err = claim(200)
	if err != nil {
		t.Fatalf("double claim err: %v", err)
	}
	if ok {
		t.Fatalf("double claim succeeded, want no-op")
	}

	if err := MarkPackageFailedByMediaProfile(ctx, rw, "m1", "h264-main-1080p", assertErr("encode failed"), 300); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	ok, err = claim(400)
	if err != nil || !ok {
		t.Fatalf("claim failed row: ok=%v err=%v", ok, err)
	}
	pkg, err := MediaPackageByID(context.Background(), rw, "pkg-m1")
	if err != nil || pkg == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", pkg, err)
	}
	if pkg.Status != PackageStatusProcessing || pkg.Error != nil {
		t.Fatalf("after retry package=%+v, want processing with cleared error", pkg)
	}
}

func TestClaimPackageSkipsReady(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if err := UpsertMediaPackage(context.Background(), rw, MediaPackage{
		ID:               "pkg-ready",
		MediaID:          "m-ready",
		RenditionProfile: "h264-main-1080p",
		Status:           PackageStatusReady,
		CreatedAtMs:      100,
		UpdatedAtMs:      100,
	}); err != nil {
		t.Fatalf("seed ready package: %v", err)
	}
	ok, err := ClaimPackage(context.Background(), rw, ClaimRequest{
		MediaID: "m-ready", Profile: "h264-main-1080p", PackageID: "pkg-ready", NowMs: 200,
	})
	if err != nil {
		t.Fatalf("claim ready err: %v", err)
	}
	if ok {
		t.Fatalf("ready package was claimed")
	}
}

func TestMarkPackageLifecycleHelpersPersistExpectedState(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-life', '/tmp/life.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	pkgRoot := "/cache/packages/m-life/h264-main-1080p"
	initPath := "/cache/packages/m-life/h264-main-1080p/init.mp4"
	pkg := MediaPackage{
		ID:               "pkg-life",
		MediaID:          "m-life",
		RenditionProfile: "h264-main-1080p",
		PackageRoot:      &pkgRoot,
		InitSegmentPath:  &initPath,
		SegmentBasePath:  "/cache/packages/m-life/h264-main-1080p/segments",
		UpdatedAtMs:      100,
	}
	if err := MarkPackageProcessing(context.Background(), rw, pkg); err != nil {
		t.Fatalf("mark processing: %v", err)
	}
	got, err := MediaPackageByID(context.Background(), rw, "pkg-life")
	if err != nil || got == nil {
		t.Fatalf("lookup processing package: pkg=%v err=%v", got, err)
	}
	if got.Status != PackageStatusProcessing || got.CreatedAtMs != 100 || got.UpdatedAtMs != 100 {
		t.Fatalf("processing package=%+v, want processing timestamps from update time", got)
	}
	if got.PackageRoot == nil || got.InitSegmentPath == nil || got.SegmentBasePath == "" {
		t.Fatalf("processing package lost filesystem paths: %+v", got)
	}

	oldError := "old transient error"
	oldPkgDur := int64(12000)
	got.Status = PackageStatusPending
	got.Error = &oldError
	got.PackagedDurationMs = &oldPkgDur
	got.UpdatedAtMs = 200
	if err := MarkPackageReady(context.Background(), rw, *got); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	ready, err := MediaPackageByID(context.Background(), rw, "pkg-life")
	if err != nil || ready == nil {
		t.Fatalf("lookup ready package: pkg=%v err=%v", ready, err)
	}
	if ready.Status != PackageStatusReady || ready.Error != nil || ready.PackagedDurationMs == nil || *ready.PackagedDurationMs != 12000 {
		t.Fatalf("ready package=%+v, want ready with cleared error and duration", ready)
	}
}

func TestCancelMediaPackagesMarksPendingAndProcessingFailed(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer rw.Close()
	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('pending', '/tmp/pending.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('processing', '/tmp/processing.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	for _, pkg := range []MediaPackage{
		{ID: "pkg-pending", MediaID: "pending", RenditionProfile: DefaultPackageProfile, Status: PackageStatusPending, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-processing", MediaID: "processing", RenditionProfile: DefaultPackageProfile, Status: PackageStatusProcessing, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-ready", MediaID: "ready", RenditionProfile: DefaultPackageProfile, Status: PackageStatusReady, CreatedAtMs: 1, UpdatedAtMs: 1},
	} {
		if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
			t.Fatalf("insert package %s: %v", pkg.ID, err)
		}
	}

	res, err := CancelMediaPackages(context.Background(), rw, []string{"pending", "processing", "ready", "missing"}, DefaultPackageProfile, 500, "cancelled by operator")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if res.CanceledPending != 1 || res.CanceledProcessing != 1 || res.SkippedReady != 1 || res.SkippedMissing != 1 {
		t.Fatalf("cancel result=%+v, want pending=1 processing=1 ready=1 missing=1", res)
	}
	for _, id := range []string{"pkg-pending", "pkg-processing"} {
		pkg, err := MediaPackageByID(context.Background(), rw, id)
		if err != nil || pkg == nil {
			t.Fatalf("lookup %s: pkg=%v err=%v", id, pkg, err)
		}
		if pkg.Status != PackageStatusFailed || pkg.Error == nil || *pkg.Error != "cancelled by operator" || pkg.UpdatedAtMs != 500 {
			t.Fatalf("%s=%+v, want failed cancellation", id, pkg)
		}
	}
}

func TestMarkPackageReadyRequiresProcessingState(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer rw.Close()
	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	cancelPkgDur := int64(12000)
	cancelErr := "cancelled by operator"
	pkg := MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   DefaultPackageProfile,
		Status:             PackageStatusFailed,
		PackagedDurationMs: &cancelPkgDur,
		Error:              &cancelErr,
		CreatedAtMs:        1,
		UpdatedAtMs:        1,
	}
	if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
		t.Fatalf("insert failed package: %v", err)
	}
	pkg.Status = PackageStatusReady
	pkg.UpdatedAtMs = 2
	if err := MarkPackageReady(context.Background(), rw, pkg); err == nil {
		t.Fatal("MarkPackageReady succeeded for failed package")
	}
	got, err := MediaPackageByID(context.Background(), rw, "pkg-m1")
	if err != nil || got == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", got, err)
	}
	if got.Status != PackageStatusFailed || *got.Error != "cancelled by operator" {
		t.Fatalf("package=%+v, want cancellation preserved", got)
	}
}

func TestMarkPackageFailedRecordsCauseAndTimestamp(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-fail', '/tmp/fail.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	pkg := MediaPackage{
		ID:               "pkg-fail",
		MediaID:          "m-fail",
		RenditionProfile: "h264-main-1080p",
		Status:           PackageStatusProcessing,
		CreatedAtMs:      100,
		UpdatedAtMs:      100,
	}
	if err := MarkPackageFailed(context.Background(), rw, pkg, assertErr("encode failed"), 300); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	got, err := MediaPackageByID(context.Background(), rw, "pkg-fail")
	if err != nil || got == nil {
		t.Fatalf("lookup failed package: pkg=%v err=%v", got, err)
	}
	if got.Status != PackageStatusFailed || got.Error == nil || *got.Error != "encode failed" || got.UpdatedAtMs != 300 {
		t.Fatalf("failed package=%+v, want failed with cause and timestamp", got)
	}
}

func TestMarkPackageFailedWithKind_TransientGoesToPending(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-transient', '/tmp/m-transient.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	pkg := MediaPackage{
		ID: "pkg-transient", MediaID: "m-transient", RenditionProfile: DefaultPackageProfile,
		Status: PackageStatusProcessing, CreatedAtMs: 100, UpdatedAtMs: 100,
	}
	if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	status, err := MarkPackageFailedWithKind(context.Background(), rw, "pkg-transient", "transient", "ffmpeg crashed", 5, 300)
	if err != nil {
		t.Fatalf("mark failed with kind: %v", err)
	}
	if status != PackageStatusPending {
		t.Fatalf("status=%s, want pending", status)
	}
	got, err := MediaPackageByID(context.Background(), rw, "pkg-transient")
	if err != nil || got == nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Status != PackageStatusPending {
		t.Fatalf("status=%s, want pending", got.Status)
	}
	if got.LastAttemptError == nil || *got.LastAttemptError != "ffmpeg crashed" {
		t.Fatalf("last_attempt_error=%+v, want 'ffmpeg crashed'", got.LastAttemptError)
	}
	if got.Error != nil {
		t.Fatalf("error should be NULL on transient fail")
	}
}

func TestMarkPackageFailedWithKind_TerminalGoesToFailed(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-terminal', '/tmp/m-terminal.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	pkg := MediaPackage{
		ID: "pkg-terminal", MediaID: "m-terminal", RenditionProfile: DefaultPackageProfile,
		Status: PackageStatusProcessing, CreatedAtMs: 100, UpdatedAtMs: 100,
	}
	if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	status, err := MarkPackageFailedWithKind(context.Background(), rw, "pkg-terminal", "terminal", "source missing", 5, 300)
	if err != nil {
		t.Fatalf("mark failed with kind: %v", err)
	}
	if status != PackageStatusFailed {
		t.Fatalf("status=%s, want failed", status)
	}
	got, err := MediaPackageByID(context.Background(), rw, "pkg-terminal")
	if err != nil || got == nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Status != PackageStatusFailed {
		t.Fatalf("status=%s, want failed", got.Status)
	}
	if got.Error == nil || *got.Error != "source missing" {
		t.Fatalf("error=%+v, want 'source missing'", got.Error)
	}
}

func TestFailStaleProcessingPackagesOnlyFailsOldProcessingRows(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('old-processing', '/tmp/old-processing.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('cutoff-processing', '/tmp/cutoff-processing.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('fresh-processing', '/tmp/fresh-processing.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('old-pending', '/tmp/old-pending.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('old-failed', '/tmp/old-failed.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	for _, pkg := range []MediaPackage{
		{ID: "pkg-old-processing", MediaID: "old-processing", RenditionProfile: DefaultPackageProfile, Status: PackageStatusProcessing, CreatedAtMs: 1, UpdatedAtMs: 100},
		{ID: "pkg-cutoff-processing", MediaID: "cutoff-processing", RenditionProfile: DefaultPackageProfile, Status: PackageStatusProcessing, CreatedAtMs: 1, UpdatedAtMs: 200},
		{ID: "pkg-fresh-processing", MediaID: "fresh-processing", RenditionProfile: DefaultPackageProfile, Status: PackageStatusProcessing, CreatedAtMs: 1, UpdatedAtMs: 300},
		{ID: "pkg-old-pending", MediaID: "old-pending", RenditionProfile: DefaultPackageProfile, Status: PackageStatusPending, CreatedAtMs: 1, UpdatedAtMs: 100},
		{ID: "pkg-old-failed", MediaID: "old-failed", RenditionProfile: DefaultPackageProfile, Status: PackageStatusFailed, CreatedAtMs: 1, UpdatedAtMs: 100},
	} {
		if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}

	n, err := FailStaleProcessingPackages(context.Background(), rw, 200, 500, 5, "stale processing reset")
	if err != nil {
		t.Fatalf("fail stale: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows affected=%d, want 1", n)
	}

	want := map[string]PackageStatus{
		"pkg-old-processing":    PackageStatusPending,
		"pkg-cutoff-processing": PackageStatusProcessing,
		"pkg-fresh-processing":  PackageStatusProcessing,
		"pkg-old-pending":       PackageStatusPending,
		"pkg-old-failed":        PackageStatusFailed,
	}
	for id, status := range want {
		got, err := MediaPackageByID(context.Background(), rw, id)
		if err != nil || got == nil {
			t.Fatalf("lookup package %s: pkg=%v err=%v", id, got, err)
		}
		if got.Status != status {
			t.Fatalf("package %s status=%s, want %s", id, got.Status, status)
		}
	}
	got, err := MediaPackageByID(context.Background(), rw, "pkg-old-processing")
	if err != nil || got == nil {
		t.Fatalf("lookup stale package: pkg=%v err=%v", got, err)
	}
	if got.Error != nil {
		t.Fatalf("stale package should have error=NULL on transient reset, got %+v", got.Error)
	}
	if got.LastAttemptError == nil || *got.LastAttemptError != "stale processing reset" || got.UpdatedAtMs != 500 {
		t.Fatalf("stale package=%+v, want last_attempt_error='stale processing reset' and recovery timestamp", got)
	}
}

func TestMarkReadyPackagePendingForReencode(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('failed', '/tmp/failed.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	pkgDur := int64(12000)
	for _, pkg := range []MediaPackage{
		{ID: "pkg-ready", MediaID: "ready", RenditionProfile: DefaultPackageProfile, Status: PackageStatusReady, PackagedDurationMs: &pkgDur, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-failed", MediaID: "failed", RenditionProfile: DefaultPackageProfile, Status: PackageStatusFailed, CreatedAtMs: 1, UpdatedAtMs: 1},
	} {
		if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}
	readySegPath := "/tmp/seg0.m4s"
	if err := ReplacePackagedSegments(context.Background(), rw, "pkg-ready", []PackagedSegment{
		{PackageID: "pkg-ready", SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: &readySegPath},
	}); err != nil {
		t.Fatalf("insert segments: %v", err)
	}

	changed, err := MarkReadyPackagePendingForReencode(context.Background(), rw, "pkg-ready", 500, "missing segment")
	if err != nil || !changed {
		t.Fatalf("mark ready pending: changed=%v err=%v", changed, err)
	}
	changed, err = MarkReadyPackagePendingForReencode(context.Background(), rw, "pkg-failed", 600, "ignored")
	if err != nil {
		t.Fatalf("mark failed package err: %v", err)
	}
	if changed {
		t.Fatalf("failed package should not be changed")
	}

	pkg, err := MediaPackageByID(context.Background(), rw, "pkg-ready")
	if err != nil || pkg == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", pkg, err)
	}
	if pkg.Status != PackageStatusPending || pkg.Error == nil || *pkg.Error != "missing segment" || pkg.PackagedDurationMs != nil || pkg.UpdatedAtMs != 500 {
		t.Fatalf("package after reset=%+v, want pending with reason and cleared duration", pkg)
	}
	segs, err := PackagedSegments(context.Background(), rw, "pkg-ready")
	if err != nil {
		t.Fatalf("segments: %v", err)
	}
	if len(segs) != 0 {
		t.Fatalf("segments should be deleted, got %+v", segs)
	}
}

func TestRequestMediaPackagesClassifiesAndRetries(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, codec_check_reason, ingested_at_ms)
		VALUES ('missing-pkg', '/tmp/missing-pkg.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('pending', '/tmp/pending.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('processing', '/tmp/processing.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('failed', '/tmp/failed.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('bad-codec', '/tmp/bad-codec.mkv', '/tmp', 12000, 'mkv', 'hevc', 2160, 'aac', 0, 'unsupported video codec', 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	oldErr := "old error"
	for _, pkg := range []MediaPackage{
		{ID: "pkg-pending", MediaID: "pending", RenditionProfile: DefaultPackageProfile, Status: PackageStatusPending, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-processing", MediaID: "processing", RenditionProfile: DefaultPackageProfile, Status: PackageStatusProcessing, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-ready", MediaID: "ready", RenditionProfile: DefaultPackageProfile, Status: PackageStatusReady, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-failed", MediaID: "failed", RenditionProfile: DefaultPackageProfile, Status: PackageStatusFailed, Error: &oldErr, CreatedAtMs: 1, UpdatedAtMs: 1},
	} {
		if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}

	got, err := RequestMediaPackages(context.Background(), rw, []string{"missing-pkg", "pending", "processing", "ready", "failed", "bad-codec", "no-such", "missing-pkg"}, "")
	if err != nil {
		t.Fatalf("request packages: %v", err)
	}
	if got.Profile != DefaultPackageProfile {
		t.Fatalf("profile=%q, want default", got.Profile)
	}
	if strings.Join(got.Queued, ",") != "missing-pkg,failed" {
		t.Fatalf("queued=%v, want missing-pkg,failed", got.Queued)
	}
	if strings.Join(got.AlreadyPending, ",") != "pending,processing" {
		t.Fatalf("alreadyPending=%v, want pending,processing", got.AlreadyPending)
	}
	if strings.Join(got.AlreadyReady, ",") != "ready" {
		t.Fatalf("alreadyReady=%v, want ready", got.AlreadyReady)
	}
	if len(got.Failed) != 2 || got.Failed[0].Code != "codec_check_failed" || got.Failed[1].Code != "not_found" {
		t.Fatalf("failed=%+v, want codec_check_failed then not_found", got.Failed)
	}
	pkg, err := MediaPackageByID(context.Background(), rw, "pkg-failed")
	if err != nil || pkg == nil {
		t.Fatalf("lookup failed package: pkg=%v err=%v", pkg, err)
	}
	if pkg.Status != PackageStatusPending || pkg.Error != nil {
		t.Fatalf("failed package after request=%+v, want pending with cleared error", pkg)
	}
}

func TestMediaPackageCandidatesReturnsNonReadyRows(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, codec_check_reason, ingested_at_ms)
		VALUES ('missing', '/tmp/missing.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('failed', '/tmp/failed.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('pending', '/tmp/pending.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('processing', '/tmp/processing.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('bad-codec', '/tmp/bad-codec.mkv', '/tmp', 12000, 'mkv', 'hevc', 2160, 'aac', 0, 'unsupported video codec', 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	oldErr2 := "old error"
	for _, pkg := range []MediaPackage{
		{ID: "pkg-failed", MediaID: "failed", RenditionProfile: DefaultPackageProfile, Status: PackageStatusFailed, Error: &oldErr2, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-pending", MediaID: "pending", RenditionProfile: DefaultPackageProfile, Status: PackageStatusPending, CreatedAtMs: 2, UpdatedAtMs: 2},
		{ID: "pkg-processing", MediaID: "processing", RenditionProfile: DefaultPackageProfile, Status: PackageStatusProcessing, CreatedAtMs: 3, UpdatedAtMs: 3},
		{ID: "pkg-ready", MediaID: "ready", RenditionProfile: DefaultPackageProfile, Status: PackageStatusReady, CreatedAtMs: 4, UpdatedAtMs: 4},
	} {
		if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}

	got, err := MediaPackageCandidates(context.Background(), rw, "", 100, 0, CandidateFilter{})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("candidates=%+v, want 4 non-ready codec-passing rows", got)
	}
	statuses := map[string]string{}
	for _, row := range got {
		status := "missing"
		if row.PackageStatus != nil {
			status = *row.PackageStatus
		}
		statuses[row.MediaID] = status
	}
	for mediaID, want := range map[string]string{
		"missing":    "missing",
		"failed":     string(PackageStatusFailed),
		"pending":    string(PackageStatusPending),
		"processing": string(PackageStatusProcessing),
	} {
		if statuses[mediaID] != want {
			t.Fatalf("status %s=%q, want %q in %+v", mediaID, statuses[mediaID], want, statuses)
		}
	}
}

func TestMediaPackageCandidateStatusCountsIncludesReadyTotals(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, codec_check_reason, ingested_at_ms)
		VALUES ('missing-a', '/tmp/missing-a.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('missing-b', '/tmp/missing-b.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('pending', '/tmp/pending.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0),
		       ('ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, NULL, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	for _, pkg := range []MediaPackage{
		{ID: "pkg-pending", MediaID: "pending", RenditionProfile: DefaultPackageProfile, Status: PackageStatusPending, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-ready", MediaID: "ready", RenditionProfile: DefaultPackageProfile, Status: PackageStatusReady, CreatedAtMs: 2, UpdatedAtMs: 2},
	} {
		if err := UpsertMediaPackage(context.Background(), rw, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}

	got, err := MediaPackageCandidateStatusCounts(context.Background(), rw, "")
	if err != nil {
		t.Fatalf("candidate status counts: %v", err)
	}
	counts := map[string]int64{}
	for _, row := range got {
		counts[row.Status] = row.Count
	}
	if counts["missing"] != 2 || counts[string(PackageStatusPending)] != 1 {
		t.Fatalf("counts=%+v, want 2 missing and 1 pending", counts)
	}
	if counts[string(PackageStatusReady)] != 1 {
		t.Fatalf("ready count=%d, want 1 in %+v", counts[string(PackageStatusReady)], counts)
	}
}

func TestPackageProfilesComesFromActiveRegistry(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('default', 'Default', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p'),
		       ('alt', 'Alt', '/tmp', 'alphabetical', 1, 0, 'packaged', 'custom-main-720p')`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if err := UpsertMediaPackage(context.Background(), rw, MediaPackage{
		ID:               "typo-package",
		MediaID:          "m1",
		RenditionProfile: "h264-maindfdfd-1080p",
		Status:           PackageStatusPending,
		CreatedAtMs:      1,
		UpdatedAtMs:      1,
	}); err != nil {
		t.Fatalf("insert typo package: %v", err)
	}

	got, err := PackageProfiles(context.Background(), rw)
	if err != nil {
		t.Fatalf("profiles: %v", err)
	}
	wantProfiles := DefaultPackageProfile + "," + packageprofile.H264CopySourceName + "," + packageprofile.HEVCCopySourceName + "," + packageprofile.H264Main720pName + "," + packageprofile.H264Main480pName + "," + packageprofile.MusicName + "," + packageprofile.H264NVENC1080pName + "," + packageprofile.H264NVENCCopySrcName + "," + packageprofile.H264NVENC720pName + "," + packageprofile.H264NVENC480pName
	if strings.Join(got, ",") != wantProfiles {
		t.Fatalf("profiles=%v, want active registry profiles only", got)
	}
}

func TestClaimPackageOnlyOneConcurrentWinner(t *testing.T) {
	path := newTestDB(t)
	seed, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if _, err := seed.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-race', '/tmp/race.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := EnsureLocalEncoder(context.Background(), seed, "Local Worker", 1); err != nil {
		t.Fatalf("ensure local encoder: %v", err)
	}
	seed.Close()

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := OpenReadWrite(path)
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			ok, err := ClaimPackage(context.Background(), conn, ClaimRequest{
				MediaID: "m-race", Profile: "h264-main-1080p", PackageID: "pkg-race", NowMs: 100,
			})
			if err != nil {
				if strings.Contains(err.Error(), "database is locked") {
					results <- false
					return
				}
				errs <- err
				return
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("claim error: %v", err)
	}
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners=%d, want 1", winners)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
