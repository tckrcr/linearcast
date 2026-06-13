package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packager"
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

func TestPrimaryManifestListsReadyABRVariantsOnly(t *testing.T) {
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
			playback_mode, required_package_profile, abr_ladder_json, package_prefill_ms
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p',
			'["h264-copy-source","h264-main-1080p","h264-main-720p"]', 86400000)`); err != nil {
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
	insertReadyManifestPackage(t, conn, "m1", "h264-copy-source", 1920, 1080, 750_000)
	insertReadyManifestPackage(t, conn, "m1", "h264-main-1080p", 1920, 1080, 500_000)

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-main-1080p",
				ABRLadder:              []string{"h264-copy-source", "h264-main-1080p", "h264-main-720p"},
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
	if got := strings.Count(body, "#EXT-X-STREAM-INF"); got != 2 {
		t.Fatalf("STREAM-INF count=%d, body:\n%s", got, body)
	}
	for _, profile := range []string{"h264-copy-source", "h264-main-1080p"} {
		if !strings.Contains(body, "/channel/ch/packaged/"+profile+"/stream.m3u8") {
			t.Fatalf("missing ready profile %s in body:\n%s", profile, body)
		}
	}
	if strings.Contains(body, "h264-main-720p") {
		t.Fatalf("unready 720p rung should be omitted:\n%s", body)
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

func TestManifestLoadsNewRelayChannelFromDBBeforeRefresh(t *testing.T) {
	conn := newPlaybackTestDB(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('new-relay', 'New Relay', '', 'alphabetical', 1, 0, 'plex_relay', NULL, 'eager')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	a := &app{
		dbConn:          conn,
		packagedProfile: "h264-main-1080p",
		channels:        map[string]*channelRuntime{},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/new-relay/stream.m3u8", nil)
	req.SetPathValue("channelID", "new-relay")
	res := httptest.NewRecorder()

	a.handleManifest(res, req)

	if res.Code != http.StatusFound {
		t.Fatalf("status=%d body=%q, want redirect to plex relay", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Location"); got != "/channel/new-relay/plexrelay.m3u8" {
		t.Fatalf("redirect=%q, want plex relay manifest", got)
	}
	rt := a.channel("new-relay")
	if rt == nil || rt.PlaybackMode != db.PlaybackModePlexRelay {
		t.Fatalf("channel runtime not loaded: %+v", rt)
	}
}

func TestPackagedManifestRejectsPlexRelayChannel(t *testing.T) {
	conn := newPlaybackTestDB(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('relay', 'Relay', '', 'alphabetical', 1, 0, 'plex_relay', NULL, 'eager')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	a := &app{
		dbConn:          conn,
		packagedProfile: "h264-main-1080p",
		channels:        map[string]*channelRuntime{},
	}
	req := httptest.NewRequest(http.MethodGet, "/channel/relay/packaged/stream.m3u8", nil)
	req.SetPathValue("channelID", "relay")
	res := httptest.NewRecorder()

	a.handlePackagedManifest(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q, want packaged route to reject plex relay channel", res.Code, res.Body.String())
	}
}

func TestOnDemandManifestUsesReadyPackageWithoutSpawningSession(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, nowMs, true)

	spawns := 0
	sessions := newPlaybackTestSessions(t, nowMs, func(ctx context.Context, spec packager.LiveSessionSpec) (ondemand.Process, error) {
		spawns++
		return newPlaybackFakeProcess(ctx), nil
	})
	defer sessions.Shutdown()
	a := playbackTestApp(conn, sessions)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-main-1080p", nowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if spawns != 0 {
		t.Fatalf("ready package should suppress session spawn, got %d spawns", spawns)
	}
	if len(items) == 0 || !strings.Contains(items[0].SegmentURI, "/channel/ch/packaged/segments/pkg-m1/") {
		t.Fatalf("ready package item should use packaged URI, got %+v", items)
	}
}

func TestOnDemandManifestEmitsSessionSegmentsWithoutReadyPackage(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, nowMs, false)

	sessions := newPlaybackTestSessions(t, nowMs, func(ctx context.Context, spec packager.LiveSessionSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, []int64{6000, 6000})
		return newPlaybackFakeProcess(ctx), nil
	})
	defer sessions.Shutdown()
	a := playbackTestApp(conn, sessions)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-main-1080p", nowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected session segments")
	}
	if !strings.Contains(items[0].InitURI, "/channel/ch/session/") || !strings.Contains(items[0].SegmentURI, "/channel/ch/session/") {
		t.Fatalf("session item should use session URIs, got %+v", items[0])
	}
	if items[0].Sequence != nowMs/db.ScheduleGridMs {
		t.Fatalf("session sequence=%d want %d", items[0].Sequence, nowMs/db.ScheduleGridMs)
	}
	if !items[0].ProgramDateTimeAlways {
		t.Fatalf("session item should emit PDT on every segment")
	}
}

func TestOnDemandManifestDoesNotRepeatSingleStartupSegment(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	queryNowMs := entryStartMs + 12_000
	sessions := newPlaybackTestSessions(t, queryNowMs, func(ctx context.Context, spec packager.LiveSessionSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, []int64{6000})
		return newPlaybackFakeProcess(ctx), nil
	})
	defer sessions.Shutdown()
	a := playbackTestApp(conn, sessions)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-main-1080p", queryNowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("startup manifest should list one available segment, got %d: %+v", len(items), items)
	}
	if !strings.HasSuffix(items[0].SegmentURI, "/0.m4s") {
		t.Fatalf("unexpected segment URI: %+v", items[0])
	}
}

func TestOnDemandManifestDropsPreviousSourceOnSequenceCollision(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	sessions := newPlaybackTestSessions(t, entryStartMs, func(ctx context.Context, spec packager.LiveSessionSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, []int64{6000})
		return newPlaybackFakeProcess(ctx), nil
	})
	defer sessions.Shutdown()
	a := playbackTestApp(conn, sessions)

	entry := db.ScheduleEntry{
		ID:         "se1",
		ChannelID:  "ch",
		StartMs:    entryStartMs,
		MediaID:    "m1",
		OffsetMs:   0,
		DurationMs: 180_000,
	}
	items := []packagedManifestItem{{
		SourceKey:        "previous-entry/session",
		Sequence:         entryStartMs / db.ScheduleGridMs,
		DurationMs:       6000,
		WallClockStartMs: entryStartMs - 6000,
	}}
	progressed, err := a.appendOnDemandManifestItems(context.Background(), &items, "ch", "h264-main-1080p", entry, entryStartMs, entryStartMs+manifestAheadMs)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !progressed {
		t.Fatalf("append did not progress")
	}
	if len(items) != 1 {
		t.Fatalf("sequence collision should drop previous source, got %+v", items)
	}
	if strings.Contains(items[0].SourceKey, "previous-entry") {
		t.Fatalf("previous source survived sequence collision: %+v", items)
	}
	if items[0].Sequence != entryStartMs/db.ScheduleGridMs {
		t.Fatalf("new segment sequence changed: %+v", items[0])
	}
}

func TestOnDemandManifestTruncatesWhenNextEntrySessionFails(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms, anchor_schedule_entry_id)
		VALUES ('se2', 'ch', ?, 'm1', 0, 180000, 0, 'se1')`, entryStartMs+180_000); err != nil {
		t.Fatalf("insert second entry: %v", err)
	}

	// Query 20s before the entry boundary so the manifest lookahead crosses
	// into se2. The first spawn (se1) serves segments through its entry end;
	// the second spawn (se2) fails — the manifest must truncate to se1's
	// segments rather than erroring.
	queryNowMs := entryStartMs + 160_000
	spawns := 0
	sessions := newPlaybackTestSessions(t, queryNowMs, func(ctx context.Context, spec packager.LiveSessionSpec) (ondemand.Process, error) {
		spawns++
		if spawns > 1 {
			return nil, fmt.Errorf("spawn refused")
		}
		writePlaybackLivePlaylist(t, spec.OutDir, []int64{6000, 6000, 6000, 6000})
		return newPlaybackFakeProcess(ctx), nil
	})
	defer sessions.Shutdown()
	a := playbackTestApp(conn, sessions)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-main-1080p", queryNowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if spawns != 2 {
		t.Fatalf("expected the manifest walk to attempt the second entry, got %d spawns", spawns)
	}
	if len(items) == 0 {
		t.Fatalf("expected current-entry segments despite next-entry session failure")
	}
	for _, item := range items {
		if !strings.HasPrefix(item.SourceKey, "se1/") {
			t.Fatalf("manifest should only contain first-entry segments, got %+v", item)
		}
	}
}

func TestOnDemandManifestNumbersIrregularCopySegmentsContiguously(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, nowMs, false)

	// A copy-mode session splits on the source's existing keyframes, so segment
	// durations are irregular and none is exactly 6s. The HLS media sequence
	// must stay gap-free and contiguous regardless — numbering is driven by
	// session ordinal, not by dividing media time onto the 6s grid.
	sessions := newPlaybackTestSessions(t, nowMs, func(ctx context.Context, spec packager.LiveSessionSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, []int64{9000, 4000, 7000})
		return newPlaybackFakeProcess(ctx), nil
	})
	defer sessions.Shutdown()
	a := playbackTestApp(conn, sessions)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-main-1080p", nowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 session segments, got %d: %+v", len(items), items)
	}
	base := nowMs / db.ScheduleGridMs
	for i, item := range items {
		if want := base + int64(i); item.Sequence != want {
			t.Fatalf("item %d sequence=%d, want %d (irregular durations must not perturb numbering)", i, item.Sequence, want)
		}
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

func insertReadyManifestPackage(t *testing.T, conn *sql.DB, mediaID, profile string, width, height, segmentBytes int64) {
	t.Helper()
	root := t.TempDir()
	initPath := filepath.Join(root, "init.mp4")
	if err := os.WriteFile(initPath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	segPath := filepath.Join(root, "0.m4s")
	if err := os.WriteFile(segPath, make([]byte, segmentBytes), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	pkgDur := int64(18000)
	pkg := db.MediaPackage{
		ID:                 mediaID + "-" + profile,
		MediaID:            mediaID,
		RenditionProfile:   profile,
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath,
		VideoWidth:         &width,
		VideoHeight:        &height,
		PackagedDurationMs: &pkgDur,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package %s: %v", profile, err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: &segPath},
	}); err != nil {
		t.Fatalf("replace segments %s: %v", profile, err)
	}
}

func newPlaybackTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return conn
}

func insertOnDemandPlaybackFixture(t *testing.T, conn *sql.DB, nowMs int64, readyPackage bool) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 'on_demand')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 180000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('se1', 'ch', ?, 'm1', 0, 180000, 0)`, nowMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")
	if !readyPackage {
		return
	}
	initPath := filepath.Join(t.TempDir(), "init.mp4")
	pkgDur := int64(180000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-main-1080p",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	segPath := filepath.Join(t.TempDir(), "seg0.m4s")
	if err := os.WriteFile(segPath, []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: &segPath},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}
}

func playbackTestApp(conn *sql.DB, sessions *ondemand.Manager) *app {
	return &app{
		dbConn:          conn,
		sessions:        sessions,
		packagedProfile: "h264-main-1080p",
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-main-1080p",
				PrefillMode:            "on_demand",
			},
		},
	}
}

func newPlaybackTestSessions(t *testing.T, nowMs int64, spawn ondemand.SpawnFunc) *ondemand.Manager {
	t.Helper()
	m, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:           filepath.Join(t.TempDir(), "sessions"),
		MaxConcurrent:  4,
		TailIntervalMs: 10,
		NowFn:          func() int64 { return nowMs },
		Spawn:          spawn,
	})
	if err != nil {
		t.Fatalf("new sessions: %v", err)
	}
	return m
}

type playbackFakeProcess struct {
	ctx  context.Context
	done chan struct{}
}

func newPlaybackFakeProcess(ctx context.Context) *playbackFakeProcess {
	p := &playbackFakeProcess{ctx: ctx, done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		close(p.done)
	}()
	return p
}

func (p *playbackFakeProcess) Wait() error {
	<-p.done
	return p.ctx.Err()
}

func writePlaybackLivePlaylist(t *testing.T, dir string, durations []int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i, d := range durations {
		name := fmt.Sprintf("seg%06d.m4s", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("segment"), 0o644); err != nil {
			t.Fatalf("write segment: %v", err)
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", float64(d)/1000, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write playlist: %v", err)
	}
}
