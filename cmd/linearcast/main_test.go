package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func backfillScheduleAnchors(t *testing.T, conn *sql.DB, channelID string) {
	t.Helper()
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, channelID); err != nil {
		t.Fatalf("backfill schedule anchors for %s: %v", channelID, err)
	}
}

func TestPackagedManifestItemsResolveFromSchedulePosition(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', 12000, 'm1', 0, 18000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")

	initPath1 := "/tmp/init.mp4"
	pkgDur1 := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath1,
		PackagedDurationMs: &pkgDur1,
		CreatedAtMs:        0,
		UpdatedAtMs:        0,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6006, Path: strptr("/tmp/0.m4s")},
		{PackageID: pkg.ID, SegmentNumber: 1, MediaStartMs: 6006, DurationMs: 6006, Path: strptr("/tmp/1.m4s")},
		{PackageID: pkg.ID, SegmentNumber: 2, MediaStartMs: 12012, DurationMs: 5988, Path: strptr("/tmp/2.m4s")},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{dbConn: conn, packagedProfile: "h264-main-1080p"}
	items, err := a.packagedManifestItems(context.Background(), "ch", "h264-main-1080p", 19000)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	if items[0].Segment.SegmentNumber != 1 || items[1].Segment.SegmentNumber != 2 {
		t.Fatalf("wrong segment resolution: %+v", items)
	}
	if items[0].Sequence != 1 || items[1].Sequence != 2 {
		t.Fatalf("wrong media sequence resolution: %+v", items)
	}
}

func TestPackagedManifestItemsRecordsCurrentPlayHistoryOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries
		(id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('se1', 'ch', 12000, 'm1', 0, 12000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")
	initPath2 := "/tmp/init.mp4"
	pkgDur2 := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath2,
		PackagedDurationMs: &pkgDur2,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr("/tmp/0.m4s")},
		{PackageID: pkg.ID, SegmentNumber: 1, MediaStartMs: 6000, DurationMs: 6000, Path: strptr("/tmp/1.m4s")},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{dbConn: conn, packagedProfile: "h264-main-1080p"}
	if _, err := a.packagedManifestItems(context.Background(), "ch", "h264-main-1080p", 18000); err != nil {
		t.Fatalf("first manifest items: %v", err)
	}
	if _, err := a.packagedManifestItems(context.Background(), "ch", "h264-main-1080p", 19000); err != nil {
		t.Fatalf("second manifest items: %v", err)
	}

	rows, err := db.PlayHistorySince(context.Background(), conn, "ch", 0)
	if err != nil {
		t.Fatalf("history since: %v", err)
	}
	if len(rows) != 1 || rows[0].ScheduleEntryID != "se1" {
		t.Fatalf("unexpected history rows: %+v", rows)
	}
}

func TestPackagedManifestMediaSequenceFollowsActualSegments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 30000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', 12000, 'm1', 0, 30000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")

	initPath3 := "/tmp/init.mp4"
	pkgDur3 := int64(30000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath3,
		PackagedDurationMs: &pkgDur3,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 9000, Path: strptr("/tmp/0.m4s")},
		{PackageID: pkg.ID, SegmentNumber: 1, MediaStartMs: 9000, DurationMs: 3000, Path: strptr("/tmp/1.m4s")},
		{PackageID: pkg.ID, SegmentNumber: 2, MediaStartMs: 12000, DurationMs: 8000, Path: strptr("/tmp/2.m4s")},
		{PackageID: pkg.ID, SegmentNumber: 3, MediaStartMs: 20000, DurationMs: 10000, Path: strptr("/tmp/3.m4s")},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{dbConn: conn, packagedProfile: "h264-main-1080p"}
	first, err := a.packagedManifestItems(context.Background(), "ch", "h264-main-1080p", 21000)
	if err != nil {
		t.Fatalf("first manifest items: %v", err)
	}
	next, err := a.packagedManifestItems(context.Background(), "ch", "h264-main-1080p", 24000)
	if err != nil {
		t.Fatalf("next manifest items: %v", err)
	}
	if first[0].Segment.SegmentNumber != 1 || first[0].Sequence != 1 {
		t.Fatalf("first manifest starts at segment/sequence %+v", first[0])
	}
	if next[0].Segment.SegmentNumber != 2 || next[0].Sequence != 2 {
		t.Fatalf("next manifest starts at segment/sequence %+v", next[0])
	}
}

func TestPackagedManifestSequenceClipsScheduleBoundary(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	for _, mediaID := range []string{"m1", "m2"} {
		if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', 24000, 'mkv', 'h264', 1080, 'aac', 1, 0)`, mediaID, "/tmp/"+mediaID+".mkv"); err != nil {
			t.Fatalf("insert media %s: %v", mediaID, err)
		}
	}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, []db.ScheduleEntry{
		{ChannelID: "ch", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 18000, CreatedAtMs: 0},
		{ChannelID: "ch", StartMs: 18000, MediaID: "m2", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	init1 := "/tmp/init1.mp4"
	init2 := "/tmp/init2.mp4"
	dur1 := int64(24000)
	dur2 := int64(12000)
	for _, pkg := range []db.MediaPackage{
		{ID: "pkg-m1", MediaID: "m1", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, InitSegmentPath: &init1, PackagedDurationMs: &dur1},
		{ID: "pkg-m2", MediaID: "m2", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, InitSegmentPath: &init2, PackagedDurationMs: &dur2},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, "pkg-m1", []db.PackagedSegment{
		{PackageID: "pkg-m1", SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr("/tmp/m1-0.m4s")},
		{PackageID: "pkg-m1", SegmentNumber: 1, MediaStartMs: 6000, DurationMs: 6000, Path: strptr("/tmp/m1-1.m4s")},
		{PackageID: "pkg-m1", SegmentNumber: 2, MediaStartMs: 12000, DurationMs: 6000, Path: strptr("/tmp/m1-2.m4s")},
		{PackageID: "pkg-m1", SegmentNumber: 3, MediaStartMs: 18000, DurationMs: 6000, Path: strptr("/tmp/m1-3.m4s")},
	}); err != nil {
		t.Fatalf("replace m1 segments: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, "pkg-m2", []db.PackagedSegment{
		{PackageID: "pkg-m2", SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr("/tmp/m2-0.m4s")},
		{PackageID: "pkg-m2", SegmentNumber: 1, MediaStartMs: 6000, DurationMs: 6000, Path: strptr("/tmp/m2-1.m4s")},
	}); err != nil {
		t.Fatalf("replace m2 segments: %v", err)
	}

	a := &app{dbConn: conn, packagedProfile: "h264-main-1080p"}
	crossing, err := a.packagedManifestItems(context.Background(), "ch", "h264-main-1080p", 17000)
	if err != nil {
		t.Fatalf("crossing manifest items: %v", err)
	}
	if len(crossing) < 2 {
		t.Fatalf("expected boundary items, got %+v", crossing)
	}
	if crossing[0].Package.ID != "pkg-m1" || crossing[0].Segment.SegmentNumber != 2 || crossing[0].Sequence != 2 || crossing[0].DiscontinuitySequence != 0 {
		t.Fatalf("wrong first boundary item: %+v", crossing[0])
	}
	if crossing[1].Package.ID != "pkg-m2" || crossing[1].Segment.SegmentNumber != 0 || crossing[1].Sequence != 3 || crossing[1].DiscontinuitySequence != 1 {
		t.Fatalf("wrong second boundary item: %+v", crossing[1])
	}

	afterBoundary, err := a.packagedManifestItems(context.Background(), "ch", "h264-main-1080p", 19000)
	if err != nil {
		t.Fatalf("after-boundary manifest items: %v", err)
	}
	if afterBoundary[0].Package.ID != "pkg-m2" || afterBoundary[0].Segment.SegmentNumber != 0 || afterBoundary[0].Sequence != 3 || afterBoundary[0].DiscontinuitySequence != 1 {
		t.Fatalf("wrong post-boundary item: %+v", afterBoundary[0])
	}
}

func TestRefreshChannelsPrunesDisabledChannels(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	a := &app{
		dbConn:          conn,
		packagedProfile: "h264-main-1080p",
		channels:        map[string]*channelRuntime{},
	}
	if err := a.refreshChannels(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if a.channel("ch") == nil {
		t.Fatalf("expected channel runtime after initial refresh")
	}

	if _, err := db.SetChannelEnabled(context.Background(), conn, "ch", false); err != nil {
		t.Fatalf("disable channel: %v", err)
	}
	if err := a.refreshChannels(context.Background()); err != nil {
		t.Fatalf("refresh after disable: %v", err)
	}
	if a.channel("ch") != nil {
		t.Fatalf("expected disabled channel runtime to be pruned")
	}
}

func TestPrimaryManifestUsesPackagedResolverForPackagedChannel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, package_prefill_ms
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 86400000)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / scheduler.TargetSegmentMs * scheduler.TargetSegmentMs) - scheduler.TargetSegmentMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', ?, 'm1', 0, 18000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")

	initPath4 := "/tmp/init.mp4"
	pkgDur4 := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath4,
		PackagedDurationMs: &pkgDur4,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	// Write real temp segment files so PeakSegmentBps can stat them.
	seg0 := filepath.Join(t.TempDir(), "0.m4s")
	seg1 := filepath.Join(t.TempDir(), "1.m4s")
	if err := os.WriteFile(seg0, make([]byte, 500_000), 0o644); err != nil {
		t.Fatalf("write seg0: %v", err)
	}
	if err := os.WriteFile(seg1, make([]byte, 400_000), 0o644); err != nil {
		t.Fatalf("write seg1: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6006, Path: strptr(seg0)},
		{PackageID: pkg.ID, SegmentNumber: 1, MediaStartMs: 6006, DurationMs: 6006, Path: strptr(seg1)},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-main-1080p",
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/ch/stream.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	a.handleManifest(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	// Master playlist: single STREAM-INF for the channel's required profile.
	if !strings.Contains(body, "#EXT-X-STREAM-INF") {
		t.Fatalf("primary manifest missing STREAM-INF:\n%s", body)
	}
	if !strings.Contains(body, "/channel/ch/packaged/h264-main-1080p/stream.m3u8") {
		t.Fatalf("primary manifest missing per-profile variant URL:\n%s", body)
	}
	if strings.Contains(body, "BANDWIDTH=0") {
		t.Fatalf("BANDWIDTH=0 means peak bitrate measurement failed:\n%s", body)
	}
}

// TestPackagedManifestEmitsCodecAttribute characterizes the CODECS attribute
// the master manifest derives via codecStringForInit. The init path points at a
// file that is never created, so the codec probe falls back to the H.264 Main
// default and the manifest must carry CODECS="avc1.4d401f,mp4a.40.2". Locks the
// codec path now that codecCache is owned by *app (R6); the assertion goes
// through handleManifest, whose signature the refactor left unchanged.
func TestPackagedManifestEmitsCodecAttribute(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, package_prefill_ms
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 86400000)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / scheduler.TargetSegmentMs * scheduler.TargetSegmentMs) - scheduler.TargetSegmentMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', ?, 'm1', 0, 18000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")

	// Init path under TempDir that is never created, so the codec probe (and
	// the stream.m3u8 fallback beside it) fail and codecStringForInit returns
	// the H.264 Main default deterministically, regardless of ffprobe presence.
	initPath := filepath.Join(t.TempDir(), "init.mp4")
	pkgDur5 := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur5,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	seg0 := filepath.Join(t.TempDir(), "0.m4s")
	if err := os.WriteFile(seg0, make([]byte, 500_000), 0o644); err != nil {
		t.Fatalf("write seg0: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6006, Path: strptr(seg0)},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-main-1080p",
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/ch/stream.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	a.handleManifest(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `CODECS="avc1.4d401f,mp4a.40.2"`) {
		t.Fatalf("manifest missing expected CODECS fallback attribute:\n%s", res.Body.String())
	}
}

func TestPrimaryManifestDoesNotFallbackWithoutReadyPackage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'generated', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / scheduler.TargetSegmentMs * scheduler.TargetSegmentMs) - scheduler.TargetSegmentMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', ?, 'm1', 0, 18000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModeGenerated,
				RequiredPackageProfile: "h264-main-1080p",
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/ch/stream.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	a.handleManifest(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want package-not-ready response", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "/segments/") {
		t.Fatalf("manifest fell back to generated segments:\n%s", res.Body.String())
	}
}

func TestMissingPackagedSegmentRequeuesReadyPackage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	missingPath := filepath.Join(t.TempDir(), "missing.m4s")
	initPath6 := "/tmp/init.mp4"
	pkgDur6 := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath6,
		PackagedDurationMs: &pkgDur6,
		CreatedAtMs:        1,
		UpdatedAtMs:        1,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr(missingPath)},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-main-1080p"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/ch/packaged/segments/pkg-m1/0.m4s", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("packageID", "pkg-m1")
	req.SetPathValue("name", "0.m4s")
	res := httptest.NewRecorder()

	a.handlePackagedSegment(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
	got, err := db.MediaPackageByID(context.Background(), conn, "pkg-m1")
	if err != nil || got == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", got, err)
	}
	if got.Status != db.PackageStatusPending || got.PackagedDurationMs != nil || got.Error == nil {
		t.Fatalf("package after missing segment=%+v, want pending with error and cleared duration", got)
	}
	segs, err := db.PackagedSegments(context.Background(), conn, "pkg-m1")
	if err != nil {
		t.Fatalf("segments: %v", err)
	}
	if len(segs) != 0 {
		t.Fatalf("segments should be cleared, got %+v", segs)
	}
}

func TestMissingPackagedInitRequeuesReadyPackage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	initPath := filepath.Join(t.TempDir(), "init.mp4")
	pkgDur7 := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur7,
		CreatedAtMs:        1,
		UpdatedAtMs:        1,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr("/tmp/0.m4s")},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-main-1080p"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/ch/packaged/init/pkg-m1/init.mp4", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("packageID", "pkg-m1")
	res := httptest.NewRecorder()

	a.handlePackagedInit(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
	got, err := db.MediaPackageByID(context.Background(), conn, "pkg-m1")
	if err != nil || got == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", got, err)
	}
	if got.Status != db.PackageStatusPending || got.PackagedDurationMs != nil || got.Error == nil {
		t.Fatalf("package after missing init=%+v, want pending with error and cleared duration", got)
	}
}

func TestExistingPackagedSegmentDoesNotRequeue(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	segmentPath := filepath.Join(t.TempDir(), "0.m4s")
	if err := os.WriteFile(segmentPath, []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	initPath8 := "/tmp/init.mp4"
	pkgDur8 := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath8,
		PackagedDurationMs: &pkgDur8,
		CreatedAtMs:        1,
		UpdatedAtMs:        1,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr(segmentPath)},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-main-1080p"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/ch/packaged/segments/pkg-m1/0.m4s", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("packageID", "pkg-m1")
	req.SetPathValue("name", "0.m4s")
	res := httptest.NewRecorder()

	a.handlePackagedSegment(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", res.Code, res.Body.String())
	}
	got, err := db.MediaPackageByID(context.Background(), conn, "pkg-m1")
	if err != nil || got == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", got, err)
	}
	if got.Status != db.PackageStatusReady {
		t.Fatalf("package status=%s, want ready", got.Status)
	}
}

func TestExternalHLSProxyServesManifestAndRelativeSegment(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/stream.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:4.000,\nseg_00000.ts\n"))
		case "/hls/seg_00000.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write([]byte("segment"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, media_kind, upstream_hls_url
		)
		VALUES ('spotify', 'Spotify', '', 'alphabetical', 1, 0, 'packaged', 'music', ?)`,
		upstream.URL+"/hls/stream.m3u8"); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	a := &app{dbConn: conn, httpClient: upstream.Client()}
	mux := a.routes()

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/external/spotify/stream.m3u8", nil)
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", res.Code, res.Body.String())
	}
	if res.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("manifest content-type=%q", res.Header().Get("Content-Type"))
	}
	if !strings.Contains(res.Body.String(), "seg_00000.ts") {
		t.Fatalf("manifest did not preserve relative segment URI:\n%s", res.Body.String())
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/external/spotify/seg_00000.ts", nil)
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("segment status=%d body=%s", res.Code, res.Body.String())
	}
	if res.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatalf("segment content-type=%q", res.Header().Get("Content-Type"))
	}
	if res.Body.String() != "segment" {
		t.Fatalf("segment body=%q", res.Body.String())
	}
}

func TestExternalHLSProxyFailsFastDuringUpstreamCooldown(t *testing.T) {
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "offline", http.StatusBadGateway)
	}))
	defer upstream.Close()

	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, media_kind, upstream_hls_url
		)
		VALUES ('spotify', 'Spotify', '', 'alphabetical', 1, 0, 'packaged', 'music', ?)`,
		upstream.URL+"/hls/stream.m3u8"); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	a := &app{dbConn: conn, httpClient: upstream.Client()}
	mux := a.routes()

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/external/spotify/stream.m3u8", nil)
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusBadGateway {
		t.Fatalf("first status=%d body=%s, want 502", res.Code, res.Body.String())
	}
	if requests != 1 {
		t.Fatalf("requests=%d, want first upstream request", requests)
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/external/spotify/stream.m3u8", nil)
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("cooldown status=%d body=%s, want 503", res.Code, res.Body.String())
	}
	if requests != 1 {
		t.Fatalf("requests=%d, want no upstream request during cooldown", requests)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Fatalf("Retry-After missing")
	}
}

func strptr(s string) *string { return &s }
