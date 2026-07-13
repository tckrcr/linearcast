package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packageprofile"
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
		RenditionProfile:   "h264-1080p-8mbps",
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

	a := &app{dbConn: conn, packagedProfile: "h264-1080p-8mbps"}
	items, err := a.packagedManifestItems(context.Background(), "ch", "h264-1080p-8mbps", 19000)
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
		RenditionProfile:   "h264-1080p-8mbps",
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

	a := &app{dbConn: conn, packagedProfile: "h264-1080p-8mbps"}
	if _, err := a.packagedManifestItems(context.Background(), "ch", "h264-1080p-8mbps", 18000); err != nil {
		t.Fatalf("first manifest items: %v", err)
	}
	if _, err := a.packagedManifestItems(context.Background(), "ch", "h264-1080p-8mbps", 19000); err != nil {
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
		RenditionProfile:   "h264-1080p-8mbps",
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

	a := &app{dbConn: conn, packagedProfile: "h264-1080p-8mbps"}
	first, err := a.packagedManifestItems(context.Background(), "ch", "h264-1080p-8mbps", 21000)
	if err != nil {
		t.Fatalf("first manifest items: %v", err)
	}
	next, err := a.packagedManifestItems(context.Background(), "ch", "h264-1080p-8mbps", 24000)
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
		{ID: "pkg-m1", MediaID: "m1", RenditionProfile: "h264-1080p-8mbps", Status: db.PackageStatusReady, InitSegmentPath: &init1, PackagedDurationMs: &dur1},
		{ID: "pkg-m2", MediaID: "m2", RenditionProfile: "h264-1080p-8mbps", Status: db.PackageStatusReady, InitSegmentPath: &init2, PackagedDurationMs: &dur2},
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

	a := &app{dbConn: conn, packagedProfile: "h264-1080p-8mbps"}
	crossing, err := a.packagedManifestItems(context.Background(), "ch", "h264-1080p-8mbps", 17000)
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

	afterBoundary, err := a.packagedManifestItems(context.Background(), "ch", "h264-1080p-8mbps", 19000)
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	a := &app{
		dbConn:          conn,
		packagedProfile: "h264-1080p-8mbps",
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

func TestRefreshChannelsFallsBackWhenProfileRenamedAway(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	// Reproduces the renamed-profile regression: the channel names a profile
	// that was renamed out of the built-in set (h264-main-1080p -> the current
	// default), so the row dangles. Resolution must fall back to a valid profile
	// rather than leave the channel pointing at a profile that 503s the encoder.
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 'on_demand')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	a := &app{
		dbConn:          conn,
		packagedProfile: "h264-1080p-8mbps",
		channels:        map[string]*channelRuntime{},
	}
	if err := a.refreshChannels(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	rt := a.channel("ch")
	if rt == nil {
		t.Fatalf("expected channel runtime")
	}
	if rt.RequiredPackageProfile != "h264-1080p-8mbps" {
		t.Fatalf("expected fallback to default profile, got %q", rt.RequiredPackageProfile)
	}
	// A valid profile must still resolve as-is (no spurious fallback).
	if got := a.resolveChannelProfile("ch", "h264-1080p-8mbps", nil); got != "h264-1080p-8mbps" {
		t.Fatalf("valid profile should resolve unchanged, got %q", got)
	}

	// The configured default itself can dangle (default_packaged_profile is not
	// validated on write). When it does, the channel must land on the canonical
	// built-in default, never on a second missing profile.
	bad := &app{
		dbConn:          conn,
		packagedProfile: "h264-2160p", // not an active profile
		channels:        map[string]*channelRuntime{},
	}
	if err := bad.refreshChannels(context.Background()); err != nil {
		t.Fatalf("refresh with bad default: %v", err)
	}
	if got := bad.channel("ch").RequiredPackageProfile; got != packageprofile.DefaultName {
		t.Fatalf("expected built-in default %q when configured default dangles, got %q", packageprofile.DefaultName, got)
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 86400000)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / db.ScheduleGridMs * db.ScheduleGridMs) - db.ScheduleGridMs
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
		RenditionProfile:   "h264-1080p-8mbps",
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
				RequiredPackageProfile: "h264-1080p-8mbps",
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/stream.m3u8", nil)
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
	if !strings.Contains(body, "streams/h264-1080p-8mbps/stream.m3u8") {
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps',
			'["h264-1080p-8mbps","h264-1080p-20mbps","hevc-2160p-40mbps-hdr"]', 86400000)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / db.ScheduleGridMs * db.ScheduleGridMs) - db.ScheduleGridMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', ?, 'm1', 0, 18000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")
	insertReadyManifestPackage(t, conn, "m1", "h264-1080p-20mbps", 1920, 1080, 750_000)
	insertReadyManifestPackage(t, conn, "m1", "h264-1080p-8mbps", 1920, 1080, 500_000)

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-1080p-8mbps",
				ABRLadder:              []string{"h264-1080p-8mbps", "h264-1080p-20mbps", "hevc-2160p-40mbps-hdr"},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/stream.m3u8", nil)
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
	for _, profile := range []string{"h264-1080p-20mbps", "h264-1080p-8mbps"} {
		if !strings.Contains(body, "streams/"+profile+"/stream.m3u8") {
			t.Fatalf("missing ready profile %s in body:\n%s", profile, body)
		}
	}
	if strings.Contains(body, "hevc-2160p-40mbps-hdr") {
		t.Fatalf("unready 2160p rung should be omitted:\n%s", body)
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 86400000)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / db.ScheduleGridMs * db.ScheduleGridMs) - db.ScheduleGridMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', ?, 'm1', 0, 18000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")

	// Init path under TempDir that is never created, so the codec probe (and
	// the stream.m3u8 fallback beside it) fail and codecStringForInit returns
	// the H.264 Main default deterministically, regardless of ffprobe presence.
	packageRoot := t.TempDir()
	initPath := filepath.Join(packageRoot, "init.mp4")
	pkgDur5 := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
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
				RequiredPackageProfile: "h264-1080p-8mbps",
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/stream.m3u8", nil)
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'generated', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / db.ScheduleGridMs * db.ScheduleGridMs) - db.ScheduleGridMs
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
				RequiredPackageProfile: "h264-1080p-8mbps",
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/stream.m3u8", nil)
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

func TestPackagedManifestFailsVisiblyWhenScheduledPackageNotReady(t *testing.T) {
	conn := newPlaybackTestDB(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 'eager')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := time.Now().UTC().UnixMilli() - 6000
	startMs -= startMs % 6000
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('se1', 'ch', ?, 'm1', 0, 18000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:               "pkg-m1",
		MediaID:          "m1",
		RenditionProfile: "h264-1080p-8mbps",
		Status:           db.PackageStatusPending,
		CreatedAtMs:      1,
		UpdatedAtMs:      1,
	}); err != nil {
		t.Fatalf("upsert package: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-1080p-8mbps", PrefillMode: "eager"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/stream.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
	res := httptest.NewRecorder()

	a.handleRenditionManifest(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", res.Code, res.Body.String())
	}
	if want := "package not ready media=m1 profile=h264-1080p-8mbps"; !strings.Contains(res.Body.String(), want) {
		t.Fatalf("body=%q, want visible package-not-ready message %q", res.Body.String(), want)
	}
	if strings.Contains(res.Body.String(), "#EXTM3U") {
		t.Fatalf("package-not-ready response should not emit an HLS manifest:\n%s", res.Body.String())
	}
}

func TestOnDemandManifestUsesReadyPackageWithoutSpawningEncoding(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, nowMs, true)

	spawns := 0
	encodings := newPlaybackTestEncodings(t, nowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		spawns++
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", nowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if spawns != 0 {
		t.Fatalf("ready package should suppress encoding spawn, got %d spawns", spawns)
	}
	if len(items) == 0 || !strings.Contains(items[0].SegmentURI, "segments/pkg-m1/") {
		t.Fatalf("ready package item should use packaged URI, got %+v", items)
	}
}

func TestOnDemandManifestEmitsEncodingSegmentsWithoutReadyPackage(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, nowMs, false)

	encodings := newPlaybackTestEncodings(t, nowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", nowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected encoding segments")
	}
	if !strings.Contains(items[0].InitURI, "../../encoding/") || !strings.Contains(items[0].SegmentURI, "../../encoding/") {
		t.Fatalf("encoding item should use encoding URIs, got %+v", items[0])
	}
	if items[0].Sequence != nowMs/db.ScheduleGridMs {
		t.Fatalf("encoding sequence=%d want %d", items[0].Sequence, nowMs/db.ScheduleGridMs)
	}
	if !items[0].ProgramDateTimeAlways {
		t.Fatalf("encoding item should emit PDT on every segment")
	}
}

func TestOnDemandMasterManifestAdvertisesHDRVideoRange(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := time.Now().UTC().UnixMilli()
	nowMs -= nowMs % db.ScheduleGridMs
	insertOnDemandPlaybackFixture(t, conn, nowMs, false)
	if _, err := conn.Exec(`UPDATE media
		SET video_codec='hevc', video_width=3840, video_height=2160, color_transfer='smpte2084', color_primaries='bt2020'
		WHERE id='m1'`); err != nil {
		t.Fatalf("update media hdr metadata: %v", err)
	}

	var a *app
	encodings := newPlaybackTestEncodings(t, nowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(3))
		a.codecCache.Store(filepath.Join(spec.OutDir, "init.mp4"), "hvc1.1.6.L153.B0,mp4a.40.2")
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a = playbackTestApp(conn, encodings)
	a.channels["ch"].RequiredPackageProfile = packageprofile.HEVCCopySourceName

	req := httptest.NewRequest(http.MethodGet, "/channels/ch/stream.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	a.handleManifest(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, "streams/hevc-copy-source/stream.m3u8") {
		t.Fatalf("master missing copy-source variant:\n%s", body)
	}
	if !strings.Contains(body, "RESOLUTION=3840x2160") {
		t.Fatalf("HDR on-demand master missing resolution:\n%s", body)
	}
	if !strings.Contains(body, "VIDEO-RANGE=PQ") {
		t.Fatalf("HDR on-demand master missing VIDEO-RANGE=PQ:\n%s", body)
	}
}

func TestOnDemandManifestStartsNewEncodingAtPlaybackLag(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	queryNowMs := entryStartMs + 30_000
	var gotSeekMs int64 = -1
	encodings := newPlaybackTestEncodings(t, queryNowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		gotSeekMs = spec.SeekMs
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", queryNowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if gotSeekMs != 12_000 {
		t.Fatalf("encoding seek = %d, want 12000 (query time minus playback lag)", gotSeekMs)
	}
	if len(items) == 0 {
		t.Fatalf("expected encoding segments")
	}
	if items[0].WallClockStartMs != entryStartMs+12_000 {
		t.Fatalf("first item wall clock = %d, want %d", items[0].WallClockStartMs, entryStartMs+12_000)
	}
	if items[0].Sequence != entryStartMs/db.ScheduleGridMs+2 {
		t.Fatalf("first item sequence = %d, want %d", items[0].Sequence, entryStartMs/db.ScheduleGridMs+2)
	}
}

func TestOnDemandManifestUsesConfiguredPlaybackLag(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	queryNowMs := entryStartMs + 30_000
	var gotSeekMs int64 = -1
	encodings := newPlaybackTestEncodings(t, queryNowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		gotSeekMs = spec.SeekMs
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)
	a.onDemandPlaybackLagMs = 10_000
	a.onDemandWarmupMs = 6_000

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", queryNowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if gotSeekMs != 20_000 {
		t.Fatalf("encoding seek = %d, want 20000 (query time minus configured playback lag)", gotSeekMs)
	}
	if len(items) == 0 {
		t.Fatalf("expected encoding segments")
	}
	if items[0].WallClockStartMs != entryStartMs+20_000 {
		t.Fatalf("first item wall clock = %d, want %d", items[0].WallClockStartMs, entryStartMs+20_000)
	}
}

func TestOnDemandManifestFailsWhenPlaybackTimingMissing(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	queryNowMs := entryStartMs + 30_000
	spawned := false
	encodings := newPlaybackTestEncodings(t, queryNowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		spawned = true
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)
	a.onDemandPlaybackLagMs = 0

	_, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", queryNowMs)
	if err == nil || !strings.Contains(err.Error(), "on-demand playback timing not configured") {
		t.Fatalf("expected timing config error, got %v", err)
	}
	if spawned {
		t.Fatal("spawned on-demand encoding with missing timing config")
	}
}

func TestHLSPlaylistsEmitRelativeChildURIsForProxyMounts(t *testing.T) {
	assertNoRootChannelURI := func(t *testing.T, body string) {
		t.Helper()
		if strings.Contains(body, "/channels/") {
			t.Fatalf("playlist leaked root-absolute channel URI:\n%s", body)
		}
	}

	t.Run("master and packaged rendition", func(t *testing.T) {
		conn := newPlaybackTestDB(t)
		nowMs := time.Now().UTC().UnixMilli()
		nowMs -= nowMs % db.ScheduleGridMs
		insertOnDemandPlaybackFixture(t, conn, nowMs, true)
		a := playbackTestApp(conn, nil)

		req := httptest.NewRequest(http.MethodGet, "/channels/ch/stream.m3u8", nil)
		req.SetPathValue("channelID", "ch")
		res := httptest.NewRecorder()
		a.handleManifest(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("master status=%d body=%s", res.Code, res.Body.String())
		}
		assertNoRootChannelURI(t, res.Body.String())

		req = httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/stream.m3u8", nil)
		req.SetPathValue("channelID", "ch")
		req.SetPathValue("profile", "h264-1080p-8mbps")
		res = httptest.NewRecorder()
		a.handleRenditionManifest(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("rendition status=%d body=%s", res.Code, res.Body.String())
		}
		assertNoRootChannelURI(t, res.Body.String())
	})

	t.Run("on-demand encoding rendition", func(t *testing.T) {
		conn := newPlaybackTestDB(t)
		nowMs := time.Now().UTC().UnixMilli()
		nowMs -= nowMs % db.ScheduleGridMs
		insertOnDemandPlaybackFixture(t, conn, nowMs, false)
		encodings := newPlaybackTestEncodings(t, nowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
			writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
			return newPlaybackFakeProcess(ctx), nil
		})
		defer encodings.Shutdown()
		a := playbackTestApp(conn, encodings)

		req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/stream.m3u8", nil)
		req.SetPathValue("channelID", "ch")
		req.SetPathValue("profile", "h264-1080p-8mbps")
		res := httptest.NewRecorder()
		a.handleRenditionManifest(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("encoding rendition status=%d body=%s", res.Code, res.Body.String())
		}
		assertNoRootChannelURI(t, res.Body.String())
	})
}

func TestOnDemandManifestRestartBudgetSetsRetryAfter(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := time.Now().UTC().UnixMilli()
	nowMs -= nowMs % db.ScheduleGridMs
	insertOnDemandPlaybackFixture(t, conn, nowMs, false)

	var proc *playbackFakeProcess
	encodings, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:              filepath.Join(t.TempDir(), "encodings"),
		MaxConcurrent:     4,
		TailIntervalMs:    10,
		RestartBudget:     1,
		RestartCooldownMs: 30_000,
		NowFn:             func() int64 { return time.Now().UTC().UnixMilli() },
		Spawn: func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
			writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
			proc = newPlaybackFakeProcess(ctx)
			return proc, nil
		},
	})
	if err != nil {
		t.Fatalf("new encodings: %v", err)
	}
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/stream.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
	res := httptest.NewRecorder()
	a.handleRenditionManifest(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s, want 200", res.Code, res.Body.String())
	}
	if proc == nil {
		t.Fatalf("encoding did not spawn")
	}
	proc.finish(errors.New("ffmpeg exited"))
	<-proc.waitDone

	req = httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/stream.m3u8", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
	res = httptest.NewRecorder()
	a.handleRenditionManifest(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status=%d body=%s, want 503", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Fatalf("Retry-After=%q, want positive cooldown", got)
	}
}

func TestOnDemandEncodingHandlerServesRetainedSegmentAfterRestart(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, nowMs, false)

	encodings := newPlaybackTestEncodings(t, nowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", nowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected encoding segment")
	}
	parts := strings.Split(items[0].SegmentURI, "/")
	if len(parts) < 2 {
		t.Fatalf("unexpected encoding URI: %s", items[0].SegmentURI)
	}
	encodingID := parts[len(parts)-2]
	name := parts[len(parts)-1]

	encodings.RestartChannel("ch")

	req := httptest.NewRequest(http.MethodGet, "/channels/ch/"+strings.TrimPrefix(items[0].SegmentURI, "../../"), nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("encodingID", encodingID)
	req.SetPathValue("name", name)
	res := httptest.NewRecorder()

	a.handleEncodingSegment(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want retained stale encoding segment", res.Code, res.Body.String())
	}
	if got := res.Body.String(); got != "segment" {
		t.Fatalf("body=%q, want retained segment bytes", got)
	}
}

func TestOnDemandRestartEndpointStopsActiveEncoding(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, nowMs, false)

	var proc *playbackFakeProcess
	encodings := newPlaybackTestEncodings(t, nowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
		proc = newPlaybackFakeProcess(ctx)
		return proc, nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	if _, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", nowMs); err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if proc == nil {
		t.Fatal("expected spawned process")
	}

	req := httptest.NewRequest(http.MethodPost, "/channels/ch/ondemand/restart", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	a.handleOnDemandRestart(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	select {
	case <-proc.waitDone:
	case <-time.After(time.Second):
		t.Fatal("restart did not stop active encoding process")
	}
}

func TestOnDemandManifestDoesNotRepeatSingleStartupSegment(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	queryNowMs := entryStartMs + 12_000
	encodings := newPlaybackTestEncodings(t, queryNowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(1))
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", queryNowMs)
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

func TestOnDemandManifestServesLatestTailWhenEncodingBehind(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	nowMs := entryStartMs + 30_000
	encodings, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:           filepath.Join(t.TempDir(), "encodings"),
		MaxConcurrent:  4,
		TailIntervalMs: 10,
		NowFn:          func() int64 { return nowMs },
		Spawn: func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
			writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(2))
			return newPlaybackFakeProcess(ctx), nil
		},
	})
	if err != nil {
		t.Fatalf("new encodings: %v", err)
	}
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	if _, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", nowMs); err != nil {
		t.Fatalf("initial manifest items: %v", err)
	}

	nowMs = entryStartMs + 90_000
	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", nowMs)
	if err != nil {
		t.Fatalf("behind encoding should serve latest tail, got error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("tail item count=%d, want 2: %+v", len(items), items)
	}
	if !strings.HasSuffix(items[0].SegmentURI, "/0.m4s") || !strings.HasSuffix(items[1].SegmentURI, "/1.m4s") {
		t.Fatalf("unexpected tail URIs: %+v", items)
	}
}

func TestOnDemandManifestDropsPreviousSourceOnSequenceCollision(t *testing.T) {
	conn := newPlaybackTestDB(t)
	entryStartMs := int64(120_000)
	insertOnDemandPlaybackFixture(t, conn, entryStartMs, false)

	encodings := newPlaybackTestEncodings(t, entryStartMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(1))
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	entry := db.ScheduleEntry{
		ID:         "se1",
		ChannelID:  "ch",
		StartMs:    entryStartMs,
		MediaID:    "m1",
		OffsetMs:   0,
		DurationMs: 180_000,
	}
	items := []packagedManifestItem{{
		SourceKey:        "previous-entry/encoding",
		Sequence:         entryStartMs / db.ScheduleGridMs,
		DurationMs:       6000,
		WallClockStartMs: entryStartMs - 6000,
	}}
	progressed, err := a.appendOnDemandManifestItems(context.Background(), &items, "ch", "h264-1080p-8mbps", entry, entryStartMs, entryStartMs+manifestAheadMs)
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

func TestOnDemandManifestTruncatesWhenNextEntryEncodingFails(t *testing.T) {
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
	encodings := newPlaybackTestEncodings(t, queryNowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		spawns++
		if spawns > 1 {
			return nil, fmt.Errorf("spawn refused")
		}
		writePlaybackLivePlaylist(t, spec.OutDir, makeOnDemandDurations(80))
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", queryNowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if spawns != 2 {
		t.Fatalf("expected the manifest walk to attempt the second entry, got %d spawns", spawns)
	}
	if len(items) == 0 {
		t.Fatalf("expected current-entry segments despite next-entry encoding failure")
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

	// A copy-mode encoding splits on the source's existing keyframes, so segment
	// durations are irregular and none is exactly 6s. The HLS media sequence
	// must stay gap-free and contiguous regardless — numbering is driven by
	// encoding ordinal, not by dividing media time onto the 6s grid.
	encodings := newPlaybackTestEncodings(t, nowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
		writePlaybackLivePlaylist(t, spec.OutDir, []int64{9000, 4000, 7000})
		return newPlaybackFakeProcess(ctx), nil
	})
	defer encodings.Shutdown()
	a := playbackTestApp(conn, encodings)

	items, err := a.packagedManifestItemsForPlayback(context.Background(), "ch", "h264-1080p-8mbps", nowMs)
	if err != nil {
		t.Fatalf("manifest items: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 encoding segments, got %d: %+v", len(items), items)
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	packageRoot := t.TempDir()
	missingPath := filepath.Join(packageRoot, "missing.m4s")
	initPath6 := filepath.Join(packageRoot, "init.mp4")
	pkgDur6 := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		PackageRoot:        &packageRoot,
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
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-1080p-8mbps"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/segments/pkg-m1/0.m4s", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	packageRoot := t.TempDir()
	initPath := filepath.Join(packageRoot, "init.mp4")
	pkgDur7 := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		PackageRoot:        &packageRoot,
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
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-1080p-8mbps"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/init/pkg-m1/init.mp4", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	packageRoot := t.TempDir()
	segmentPath := filepath.Join(packageRoot, "0.m4s")
	if err := os.WriteFile(segmentPath, []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	initPath8 := filepath.Join(packageRoot, "init.mp4")
	pkgDur8 := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		PackageRoot:        &packageRoot,
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
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-1080p-8mbps"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/segments/pkg-m1/0.m4s", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
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

func TestPackagedSegmentRejectsPathOutsidePackageRoot(t *testing.T) {
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	packageRoot := t.TempDir()
	outsideRoot := t.TempDir()
	segmentPath := filepath.Join(outsideRoot, "0.m4s")
	if err := os.WriteFile(segmentPath, []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	initPath := filepath.Join(packageRoot, "init.mp4")
	pkgDur := int64(12000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		PackageRoot:        &packageRoot,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
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
			"ch": {ID: "ch", DisplayName: "Channel", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-1080p-8mbps"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/segments/pkg-m1/0.m4s", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
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
	if got.Status != db.PackageStatusReady {
		t.Fatalf("package status=%s, want ready for rejected out-of-root path", got.Status)
	}
}

func packagedSegmentRequest() (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/segments/pkg-m1/0.m4s", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("profile", "h264-1080p-8mbps")
	req.SetPathValue("packageID", "pkg-m1")
	req.SetPathValue("name", "0.m4s")
	return httptest.NewRecorder(), req
}

// seedRepairDB writes a packaged channel, one media row, and one ready package
// whose only segment points at missingSegPath, into an open writable conn.
func seedRepairDB(t *testing.T, conn *sql.DB, missingSegPath string) {
	t.Helper()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	packageRoot := filepath.Dir(missingSegPath)
	initPath := filepath.Join(packageRoot, "init.mp4")
	pkgDur := int64(12000)
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		PackageRoot:        &packageRoot,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
		CreatedAtMs:        1,
		UpdatedAtMs:        1,
	}); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, "pkg-m1", []db.PackagedSegment{
		{PackageID: "pkg-m1", SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr(missingSegPath)},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}
}

func repairTestApp(conn *sql.DB) *app {
	return &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {ID: "ch", PlaybackMode: db.PlaybackModePackaged, RequiredPackageProfile: "h264-1080p-8mbps"},
		},
	}
}

// TestRepairRequeueIdempotentWhilePending verifies a second missing-artifact
// request while the package is already pending is a no-op: it still 404s but
// does not re-mutate package state (no repeated ready->pending churn).
func TestRepairRequeueIdempotentWhilePending(t *testing.T) {
	conn, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	seedRepairDB(t, conn, filepath.Join(t.TempDir(), "missing.m4s"))
	a := repairTestApp(conn)

	res, req := packagedSegmentRequest()
	a.handlePackagedSegment(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("first request status=%d, want 404", res.Code)
	}
	after1, err := db.MediaPackageByID(context.Background(), conn, "pkg-m1")
	if err != nil || after1 == nil || after1.Status != db.PackageStatusPending {
		t.Fatalf("after first request pkg=%+v err=%v, want pending", after1, err)
	}

	res2, req2 := packagedSegmentRequest()
	a.handlePackagedSegment(res2, req2)
	if res2.Code != http.StatusNotFound {
		t.Fatalf("second request status=%d, want 404", res2.Code)
	}
	after2, err := db.MediaPackageByID(context.Background(), conn, "pkg-m1")
	if err != nil || after2 == nil || after2.Status != db.PackageStatusPending {
		t.Fatalf("after second request pkg=%+v err=%v, want still pending", after2, err)
	}
	// The repair write is a no-op once already pending: a direct call now
	// reports no change, proving repeated playback 404s do not re-requeue.
	changed, err := db.MarkReadyPackagePendingForReencode(context.Background(), conn, "pkg-m1", 999, "probe")
	if err != nil {
		t.Fatalf("idempotent requeue probe: %v", err)
	}
	if changed {
		t.Fatalf("requeue reported a change while already pending, want no-op")
	}
}

// TestRepairRequeueDBWriteFailureStillReturns404 models the matrix row "repair
// write fails because DB is locked/unavailable": reads succeed but the repair
// write fails (read-only DB). The request must still fail visibly (404) without
// crashing or leaving partial state; the package stays ready for a later retry.
func TestRepairRequeueDBWriteFailureStillReturns404(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	rw, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	seedRepairDB(t, rw, filepath.Join(t.TempDir(), "missing.m4s"))
	// Checkpoint the WAL into the main file, then reopen read-only so the
	// read-only handle sees the committed rows but cannot satisfy the repair
	// write.
	if _, err := rw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	ro, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer ro.Close()
	a := repairTestApp(ro)

	res, req := packagedSegmentRequest()
	a.handlePackagedSegment(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 even when repair write fails", res.Code)
	}
	got, err := db.MediaPackageByID(context.Background(), ro, "pkg-m1")
	if err != nil || got == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", got, err)
	}
	if got.Status != db.PackageStatusReady {
		t.Fatalf("package status=%s after failed repair write, want still ready (no partial state)", got.Status)
	}
}

// TestPackagedManifestDBFailureRecovers verifies that a closed database during
// manifest generation returns a 503 and does not crash the handler, and that
// reopening the database allows the next request to serve a 200 manifest.
func TestPackagedManifestDBFailureRecovers(t *testing.T) {
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := (time.Now().UTC().UnixMilli() / db.ScheduleGridMs * db.ScheduleGridMs) - db.ScheduleGridMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', ?, 'm1', 0, 18000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	backfillScheduleAnchors(t, conn, "ch")

	packageRoot := t.TempDir()
	initPath := filepath.Join(packageRoot, "init.mp4")
	if err := os.WriteFile(initPath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	segPath := filepath.Join(packageRoot, "seg0.m4s")
	if err := os.WriteFile(segPath, []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	pkgDur := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		PackageRoot:        &packageRoot,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6006, Path: strptr(segPath)},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-1080p-8mbps",
			},
		},
	}

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/channels/ch/stream.m3u8", nil)
		r.SetPathValue("channelID", "ch")
		return r
	}

	// 1. Baseline: manifest succeeds before failure
	res := httptest.NewRecorder()
	a.handleManifest(res, req())
	if res.Code != http.StatusOK {
		t.Fatalf("initial manifest status=%d body=%s, want 200", res.Code, res.Body.String())
	}

	// 2. Close the database to simulate a disk/DB failure
	conn.Close()

	res2 := httptest.NewRecorder()
	a.handleManifest(res2, req())
	if res2.Code != http.StatusServiceUnavailable {
		t.Fatalf("after close status=%d body=%s, want 503", res2.Code, res2.Body.String())
	}

	// 3. Reopen and verify recovery
	conn2, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer conn2.Close()
	a2 := &app{
		dbConn: conn2,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-1080p-8mbps",
			},
		},
	}

	res3 := httptest.NewRecorder()
	a2.handleManifest(res3, req())
	if res3.Code != http.StatusOK {
		t.Fatalf("after reopen status=%d body=%s, want 200", res3.Code, res3.Body.String())
	}
}

// TestPackagedSegmentDBFailureRecovers verifies that a closed database during
// packaged segment serving returns a 500 and does not crash, and that reopening
// the database allows the next request to serve the segment successfully.
func TestPackagedSegmentDBFailureRecovers(t *testing.T) {
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	packageRoot := t.TempDir()
	segFile := filepath.Join(packageRoot, "seg0.m4s")
	if err := os.WriteFile(segFile, []byte("segment-data"), 0o644); err != nil {
		t.Fatalf("write segment file: %v", err)
	}
	initPath := filepath.Join(packageRoot, "init.mp4")
	if err := os.WriteFile(initPath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	pkgDur := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		PackageRoot:        &packageRoot,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6006, Path: strptr(segFile)},
	}); err != nil {
		t.Fatalf("replace segments: %v", err)
	}

	a := &app{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-1080p-8mbps",
			},
		},
	}

	segReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/channels/ch/streams/h264-1080p-8mbps/segments/pkg-m1/0.m4s", nil)
		r.SetPathValue("channelID", "ch")
		r.SetPathValue("profile", "h264-1080p-8mbps")
		r.SetPathValue("packageID", "pkg-m1")
		r.SetPathValue("name", "0.m4s")
		return r
	}

	// 1. Baseline: segment succeeds
	res := httptest.NewRecorder()
	a.handlePackagedSegment(res, segReq())
	if res.Code != http.StatusOK {
		t.Fatalf("initial segment status=%d body=%s, want 200", res.Code, res.Body.String())
	}

	// 2. Close the database
	conn.Close()

	res2 := httptest.NewRecorder()
	a.handlePackagedSegment(res2, segReq())
	if res2.Code != http.StatusInternalServerError {
		t.Fatalf("after close status=%d body=%s, want 500", res2.Code, res2.Body.String())
	}

	// 3. Reopen and verify recovery
	conn2, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer conn2.Close()
	a2 := &app{
		dbConn: conn2,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-1080p-8mbps",
			},
		},
	}

	res3 := httptest.NewRecorder()
	a2.handlePackagedSegment(res3, segReq())
	if res3.Code != http.StatusOK {
		t.Fatalf("after reopen status=%d body=%s, want 200", res3.Code, res3.Body.String())
	}
}

func TestExternalHLSProxyRewritesManifestAndServesNestedResources(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/stream.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(strings.Join([]string{
				"#EXTM3U",
				"#EXT-X-STREAM-INF:BANDWIDTH=800000",
				"variants/low/index.m3u8?token=keep",
				"",
			}, "\n")))
		case "/hls/variants/low/index.m3u8":
			if got := r.URL.Query().Get("token"); got != "keep" {
				t.Errorf("variant token=%q, want keep", got)
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(strings.Join([]string{
				"#EXTM3U",
				`#EXT-X-MAP:URI="init/init.mp4?map=1"`,
				"#EXTINF:4.000,",
				"../seg/seg_00000.ts?seg=1",
				"",
			}, "\n")))
		case "/hls/variants/low/init/init.mp4":
			if got := r.URL.Query().Get("map"); got != "1" {
				t.Errorf("init query=%q, want 1", got)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("init"))
		case "/hls/variants/seg/seg_00000.ts":
			if got := r.URL.Query().Get("seg"); got != "1" {
				t.Errorf("segment query=%q, want 1", got)
			}
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
	// Child URIs are relative to the manifest so the /hls mount prefix on the
	// manifest URL is preserved; the top manifest sits one level above the
	// proxy mount, so its children carry the proxy/ segment.
	if !strings.Contains(res.Body.String(), "\nproxy/variants/low/index.m3u8?token=keep") {
		t.Fatalf("manifest did not rewrite variant URI relative:\n%s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), "/external/") {
		t.Fatalf("manifest leaked root-absolute external URI:\n%s", res.Body.String())
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/external/spotify/proxy/variants/low/index.m3u8?token=keep", nil)
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("variant status=%d body=%s", res.Code, res.Body.String())
	}
	// Variant playlist children are relative to the variant's own directory.
	for _, want := range []string{
		`"init/init.mp4?map=1"`,
		"\n../seg/seg_00000.ts?seg=1",
	} {
		if !strings.Contains(res.Body.String(), want) {
			t.Fatalf("variant manifest missing relative %q:\n%s", want, res.Body.String())
		}
	}
	if strings.Contains(res.Body.String(), "/external/") {
		t.Fatalf("variant manifest leaked root-absolute external URI:\n%s", res.Body.String())
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/external/spotify/proxy/variants/seg/seg_00000.ts?seg=1", nil)
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
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 'on_demand')`); err != nil {
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
		RenditionProfile:   "h264-1080p-8mbps",
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

func playbackTestApp(conn *sql.DB, encodings *ondemand.Manager) *app {
	return &app{
		dbConn:                conn,
		encodings:             encodings,
		packagedProfile:       "h264-1080p-8mbps",
		onDemandPlaybackLagMs: defaultOnDemandPlaybackLagMs,
		onDemandWarmupMs:      defaultOnDemandWarmupMs,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				PlaybackMode:           db.PlaybackModePackaged,
				RequiredPackageProfile: "h264-1080p-8mbps",
				PrefillMode:            "on_demand",
			},
		},
	}
}

func newPlaybackTestEncodings(t *testing.T, nowMs int64, spawn ondemand.SpawnFunc) *ondemand.Manager {
	t.Helper()
	m, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:           filepath.Join(t.TempDir(), "encodings"),
		MaxConcurrent:  4,
		TailIntervalMs: 10,
		NowFn:          func() int64 { return nowMs },
		Spawn:          spawn,
	})
	if err != nil {
		t.Fatalf("new encodings: %v", err)
	}
	return m
}

type playbackFakeProcess struct {
	ctx      context.Context
	done     chan error
	waitDone chan struct{}
	once     sync.Once
}

func newPlaybackFakeProcess(ctx context.Context) *playbackFakeProcess {
	p := &playbackFakeProcess{ctx: ctx, done: make(chan error, 1), waitDone: make(chan struct{})}
	go func() {
		<-ctx.Done()
		p.finish(ctx.Err())
	}()
	return p
}

func (p *playbackFakeProcess) finish(err error) {
	p.once.Do(func() { p.done <- err })
}

func TestBurnSubtitleListExplainsCopyProfileUnavailable(t *testing.T) {
	conn := newPlaybackTestDB(t)
	a := playbackTestApp(conn, nil)
	a.channels["ch"].RequiredPackageProfile = "hevc-copy-source"

	req := httptest.NewRequest(http.MethodGet, "/hls/channels/ch/subtitles", nil)
	req.SetPathValue("channelID", "ch")
	rr := httptest.NewRecorder()
	a.handleBurnSubtitleList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body burnSubtitleListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Mode != "burn" {
		t.Fatalf("mode=%q, want burn", body.Mode)
	}
	if body.Unavailable != "burn-in subtitles require a transcode video profile" {
		t.Fatalf("unavailable=%q", body.Unavailable)
	}
	if len(body.Tracks) != 0 {
		t.Fatalf("tracks=%+v, want none", body.Tracks)
	}
}

func TestBurnSubtitleSetRejectsCopyProfile(t *testing.T) {
	conn := newPlaybackTestDB(t)
	a := playbackTestApp(conn, nil)
	a.channels["ch"].RequiredPackageProfile = "hevc-copy-source"

	req := httptest.NewRequest(http.MethodPost, "/hls/channels/ch/subtitles", strings.NewReader(`{"language":"eng"}`))
	req.SetPathValue("channelID", "ch")
	rr := httptest.NewRecorder()
	a.handleBurnSubtitleSet(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "burn-in subtitles require a transcode video profile") {
		t.Fatalf("body=%q", rr.Body.String())
	}
}

func TestBurnSubtitleTrackResponseMarksBurnMode(t *testing.T) {
	conn := newPlaybackTestDB(t)
	nowMs := time.Now().UTC().UnixMilli()
	nowMs -= nowMs % 6000
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 'on_demand')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	mediaPath := filepath.Join(t.TempDir(), "media.mkv")
	if err := os.WriteFile(mediaPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', ?, ?, 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`, mediaPath, filepath.Dir(mediaPath)); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('entry1', 'ch', ?, 'm1', 0, 18000, 0)`, nowMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	a := playbackTestApp(conn, nil)
	a.subtitleStreamCache.Store("m1", []packager.SubtitleStreamInfo{{
		Index:    3,
		Codec:    "hdmv_pgs_subtitle",
		Language: "eng",
		IsBitmap: true,
		Forced:   true,
	}})
	req := httptest.NewRequest(http.MethodGet, "/hls/channels/ch/subtitles", nil)
	req.SetPathValue("channelID", "ch")
	rr := httptest.NewRecorder()
	a.handleBurnSubtitleList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body burnSubtitleListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Unavailable != "" {
		t.Fatalf("unavailable=%q", body.Unavailable)
	}
	if len(body.Tracks) != 1 {
		t.Fatalf("tracks=%+v, want one", body.Tracks)
	}
	if body.Tracks[0].Mode != "burn" {
		t.Fatalf("track mode=%q, want burn", body.Tracks[0].Mode)
	}
	if body.Tracks[0].Label != "eng burned-in forced" {
		t.Fatalf("label=%q", body.Tracks[0].Label)
	}
}

func (p *playbackFakeProcess) Wait() error {
	err := <-p.done
	close(p.waitDone)
	return err
}

func writePlaybackLivePlaylist(t *testing.T, dir string, durations []int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir encoding dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MAP:URI=\"init.mp4\"\n", (scheduler.TargetSegmentMs+999)/1000)
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

// makeOnDemandDurations creates a slice of n target-segment durations.
func makeOnDemandDurations(n int) []int64 {
	d := make([]int64, n)
	for i := range d {
		d[i] = scheduler.TargetSegmentMs
	}
	return d
}

func TestReadyCoverageMsForMasterGate(t *testing.T) {
	got := readyCoverageMsForMasterGate()
	if got != onDemandReadyCoverageMs {
		t.Fatalf("ready gate = %d ms, want normal join cushion %d ms", got, onDemandReadyCoverageMs)
	}
}

// TestOnDemandEnsureReadyGate verifies that an on-demand channel is not
// declared ready until onDemandReadyCoverageMs of playable coverage is buffered
// from the served position. The gate sums each segment's actual duration rather
// than counting segments, so irregular (copy-mode) segment sizes are judged by
// real coverage; near the entry's end it accepts whatever coverage remains.
func TestOnDemandEnsureReadyGate(t *testing.T) {
	profile, ok := packageprofile.Lookup("h264-1080p-8mbps")
	if !ok {
		t.Fatal("default profile not found")
	}

	tests := []struct {
		name            string
		entryDurationMs int64
		entryOffsetMs   int64
		numSegments     int     // target-sized (2000ms) segments to write; ignored if durations is set
		durations       []int64 // explicit per-segment durations in ms, for irregular-coverage cases
		nowMsOffset     int64   // query time relative to entryStartMs
		wantReady       bool
	}{
		// Two target-sized (2000ms) segments cover the 4000ms cushion.
		{name: "three_segments_ready", entryDurationMs: 180_000, numSegments: 3, nowMsOffset: 30_000, wantReady: true},
		{name: "two_segments_ready", entryDurationMs: 180_000, numSegments: 2, nowMsOffset: 30_000, wantReady: true},
		{name: "one_segment_warming", entryDurationMs: 180_000, numSegments: 1, nowMsOffset: 30_000, wantReady: false},
		// No segments — gate holds.
		{name: "zero_segments_warming", entryDurationMs: 180_000, numSegments: 0, nowMsOffset: 30_000, wantReady: false},
		// Coverage, not count: one long copy-mode GOP that alone meets the cushion
		// is ready (a count gate of "2 segments" would have held it warming)...
		{name: "one_long_segment_covers_ready", entryDurationMs: 180_000, durations: []int64{4000}, nowMsOffset: 30_000, wantReady: true},
		// ...and two short segments that do not meet the cushion stay warming (a
		// count gate of "2 segments" would have wrongly declared them ready).
		{name: "two_short_segments_warming", entryDurationMs: 180_000, durations: []int64{1000, 1000}, nowMsOffset: 30_000, wantReady: false},
		// Tail fallback: enough coverage buffered ahead of the served position.
		{name: "tail_two_remaining_ready", entryDurationMs: 18_000, numSegments: 4, nowMsOffset: 29_000, wantReady: true},
		{name: "tail_one_remaining_ready", entryDurationMs: 18_000, numSegments: 4, nowMsOffset: 31_000, wantReady: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subConn := newPlaybackTestDB(t)
			if err := db.UpsertPackageProfile(context.Background(), subConn, profile); err != nil {
				t.Fatalf("upsert profile: %v", err)
			}

			channelID := "ch"
			mediaID := "m1"
			entryID := "se1"
			entryStartMs := int64(120_000)

			if _, err := subConn.Exec(`INSERT INTO channels (
				id, display_name, source_directory, ordering, enabled, created_at_ms,
				playback_mode, required_package_profile, prefill_mode
			) VALUES (?, 'Test Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 'on_demand')`, channelID); err != nil {
				t.Fatalf("insert channel: %v", err)
			}
			if _, err := subConn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
				video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
				VALUES (?, '/tmp/media.mkv', '/tmp', ?, 'mkv', 'h264', 1080, 'aac', 1, 0)`, mediaID, tt.entryDurationMs); err != nil {
				t.Fatalf("insert media: %v", err)
			}
			if _, err := subConn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
				VALUES (?, ?, ?, ?, ?, ?, 0)`, entryID, channelID, entryStartMs, mediaID, tt.entryOffsetMs, tt.entryDurationMs); err != nil {
				t.Fatalf("insert schedule: %v", err)
			}
			backfillScheduleAnchors(t, subConn, channelID)

			queryNowMs := entryStartMs + tt.nowMsOffset

			encodings := newPlaybackTestEncodings(t, queryNowMs, func(ctx context.Context, spec packager.LiveEncodingSpec) (ondemand.Process, error) {
				durations := tt.durations
				if durations == nil && tt.numSegments > 0 {
					durations = makeOnDemandDurations(tt.numSegments)
				}
				if len(durations) > 0 {
					writePlaybackLivePlaylist(t, spec.OutDir, durations)
				}
				return newPlaybackFakeProcess(ctx), nil
			})
			defer encodings.Shutdown()

			a := &app{
				dbConn:                subConn,
				encodings:             encodings,
				packagedProfile:       "h264-1080p-8mbps",
				onDemandPlaybackLagMs: defaultOnDemandPlaybackLagMs,
				onDemandWarmupMs:      defaultOnDemandWarmupMs,
				channels: map[string]*channelRuntime{
					channelID: {
						ID:                     channelID,
						DisplayName:            "Test Channel",
						PlaybackMode:           db.PlaybackModePackaged,
						RequiredPackageProfile: "h264-1080p-8mbps",
						PrefillMode:            "on_demand",
					},
				},
			}

			entry := db.ScheduleEntry{
				ID: entryID, ChannelID: channelID, StartMs: entryStartMs,
				MediaID: mediaID, OffsetMs: tt.entryOffsetMs, DurationMs: tt.entryDurationMs,
			}

			_, _, _, err := a.ensureOnDemandMasterReady(context.Background(), channelID, "h264-1080p-8mbps", entry, queryNowMs, onDemandReadyCoverageMs)
			if tt.wantReady {
				if err != nil {
					t.Fatalf("expected ready, got error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected warming error, got ready")
				}
				if !strings.Contains(err.Error(), "warming") {
					t.Fatalf("expected warming error, got: %v", err)
				}
			}
		})
	}
}
