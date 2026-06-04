package admin

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}

func testAdminApp(t *testing.T) (*App, *sql.DB) {
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
	return New(Config{DB: conn}), conn
}

func insertDeleteFixture(t *testing.T, conn *sql.DB, enabled bool) {
	t.Helper()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', ?, 0, 'packaged', 'h264-main-1080p')`, enabledInt); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'ch', 0, 'm1', 0, 18000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "ch"); err != nil {
		t.Fatalf("backfill schedule anchors: %v", err)
	}
	pkgRoot := "/cache/packages/m1/h264-main-1080p"
	initPath := "/cache/packages/m1/h264-main-1080p/init.mp4"
	pkgDur := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   db.DefaultPackageProfile,
		Status:             db.PackageStatusReady,
		PackageRoot:        &pkgRoot,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
		CreatedAtMs:        0,
		UpdatedAtMs:        0,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("insert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 18000, Path: strptr("/cache/packages/m1/h264-main-1080p/segments/0.m4s")},
	}); err != nil {
		t.Fatalf("insert packaged segment: %v", err)
	}
}

func insertMedia(t *testing.T, conn *sql.DB, mediaID string, durationMs int64) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES (?, ?, '/tmp', ?, 'mkv', 'h264', 1080, 'aac', 1, 0)`,
		mediaID, "/tmp/"+mediaID+".mkv", durationMs); err != nil {
		t.Fatalf("insert media %s: %v", mediaID, err)
	}
}

func insertReadyPackage(t *testing.T, conn *sql.DB, mediaID string, durationMs int64) {
	t.Helper()
	pkgRoot := "/cache/packages/" + mediaID + "/h264-main-1080p"
	initPath := "/cache/packages/" + mediaID + "/h264-main-1080p/init.mp4"
	pkg := db.MediaPackage{
		ID:                 "pkg-" + mediaID,
		MediaID:            mediaID,
		RenditionProfile:   db.DefaultPackageProfile,
		Status:             db.PackageStatusReady,
		PackageRoot:        &pkgRoot,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &durationMs,
		CreatedAtMs:        0,
		UpdatedAtMs:        0,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("insert package %s: %v", mediaID, err)
	}
}

func insertFutureRangeFixture(t *testing.T, conn *sql.DB) int64 {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	for _, mediaID := range []string{"m1", "m2", "m3", "m4"} {
		insertMedia(t, conn, mediaID, 12000)
		insertReadyPackage(t, conn, mediaID, 12000)
		if _, err := db.AddChannelMedia(context.Background(), conn, "ch", mediaID, 0); err != nil {
			t.Fatalf("add channel media %s: %v", mediaID, err)
		}
	}
	start := scheduler.Align6s(time.Now().UTC().UnixMilli()) + 60000
	entries := make([]db.ScheduleEntry, 0, 4)
	for i, mediaID := range []string{"m1", "m2", "m3", "m4"} {
		entries = append(entries, db.ScheduleEntry{
			ChannelID:   "ch",
			StartMs:     start + int64(i)*12000,
			MediaID:     mediaID,
			OffsetMs:    0,
			DurationMs:  12000,
			CreatedAtMs: 0,
		})
	}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, entries); err != nil {
		t.Fatalf("insert schedule entries: %v", err)
	}
	return start
}

func assertCount(t *testing.T, conn *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if got != want {
		t.Fatalf("%s: got %d, want %d", query, got, want)
	}
}

func strptr(s string) *string { return &s }
