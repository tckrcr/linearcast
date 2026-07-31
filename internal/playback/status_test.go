package playback

import (
	"context"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestReadyAllowsScheduledOnDemandChannelWithoutDurablePackage(t *testing.T) {
	a := readyTestRuntime(t, "on_demand")
	if err := a.CheckPlaybackReadiness(context.Background()); err != nil {
		t.Fatalf("readiness error: %v", err)
	}
}

func TestReadyRequiresDurablePackageForScheduledEagerChannel(t *testing.T) {
	a := readyTestRuntime(t, "eager")
	if err := a.CheckPlaybackReadiness(context.Background()); err == nil {
		t.Fatal("readiness succeeded without a durable package")
	}
}

func TestReadyAllowsPlayableEagerChannelBelowOneHourPackageCoverage(t *testing.T) {
	a := readyTestRuntime(t, "eager")
	addReadyPackage(t, a, 90_000)
	if err := a.CheckPlaybackReadiness(context.Background()); err != nil {
		t.Fatalf("readiness error: %v", err)
	}
}

func readyTestRuntime(t *testing.T, prefillMode string) *Runtime {
	t.Helper()
	conn := newPlaybackTestDB(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			required_package_profile, prefill_mode
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps', ?)`, prefillMode); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 3600000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	startMs := time.Now().UTC().UnixMilli() - 1000
	startMs -= startMs % 6000
	if _, err := conn.Exec(`INSERT INTO schedule_entries (
			id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms
		)
		VALUES ('se1', 'ch', ?, 'm1', 0, 3600000, 0)`, startMs); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	return &Runtime{
		dbConn: conn,
		channels: map[string]*channelRuntime{
			"ch": {
				ID:                     "ch",
				DisplayName:            "Channel",
				RequiredPackageProfile: "h264-1080p-8mbps",
				PrefillMode:            prefillMode,
			},
		},
	}
}

func addReadyPackage(t *testing.T, a *Runtime, packagedDurationMs int64) {
	t.Helper()
	if _, err := a.dbConn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel media: %v", err)
	}
	initPath := "/tmp/init.mp4"
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   "h264-1080p-8mbps",
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &packagedDurationMs,
	}
	if err := db.UpsertMediaPackage(context.Background(), a.dbConn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	segments := make([]db.PackagedSegment, 0, packagedDurationMs/6000)
	segmentPath := "/tmp/segment.m4s"
	for startMs := int64(0); startMs < packagedDurationMs; startMs += 6000 {
		segments = append(segments, db.PackagedSegment{
			PackageID:     pkg.ID,
			SegmentNumber: startMs / 6000,
			MediaStartMs:  startMs,
			DurationMs:    6000,
			Path:          &segmentPath,
		})
	}
	if err := db.ReplacePackagedSegments(context.Background(), a.dbConn, pkg.ID, segments); err != nil {
		t.Fatalf("replace packaged segments: %v", err)
	}
}
