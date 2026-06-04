package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func backfillScheduleAnchors(t *testing.T, conn *sql.DB, channelID string) {
	t.Helper()
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, channelID); err != nil {
		t.Fatalf("backfill schedule anchors for %s: %v", channelID, err)
	}
}

func newScheduleCLITestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return conn
}

func TestCheckScheduleIntegrityPassesCleanSchedule(t *testing.T) {
	conn := newScheduleCLITestDB(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m2', '/tmp/m2.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	dur := int64(12000)
	for _, pkg := range []db.MediaPackage{
		{ID: "pkg-m1", MediaID: "m1", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, PackagedDurationMs: &dur, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-m2", MediaID: "m2", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, PackagedDurationMs: &dur, CreatedAtMs: 1, UpdatedAtMs: 1},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, []db.ScheduleEntry{
		{ChannelID: "ch", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
		{ChannelID: "ch", StartMs: 12000, MediaID: "m2", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	issues, err := checkScheduleIntegrity(conn, integrityOptions{FromMs: 0, ToMs: 24000, GapMs: 30000})
	if err != nil {
		t.Fatalf("check integrity: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues=%+v, want none", issues)
	}
}

func TestCheckScheduleIntegrityReportsOperationalIssues(t *testing.T) {
	conn := newScheduleCLITestDB(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-overlap', '/tmp/overlap.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-gap', '/tmp/gap.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-unready', '/tmp/unready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m-invalid', '/tmp/invalid.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	dur := int64(12000)
	for _, pkg := range []db.MediaPackage{
		{ID: "pkg-ready", MediaID: "m-ready", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, PackagedDurationMs: &dur, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-overlap", MediaID: "m-overlap", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, PackagedDurationMs: &dur, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-gap", MediaID: "m-gap", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, PackagedDurationMs: &dur, CreatedAtMs: 1, UpdatedAtMs: 1},
		{ID: "pkg-invalid", MediaID: "m-invalid", RenditionProfile: "h264-main-1080p", Status: db.PackageStatusReady, PackagedDurationMs: &dur, CreatedAtMs: 1, UpdatedAtMs: 1},
	} {
		if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
			t.Fatalf("upsert package %s: %v", pkg.ID, err)
		}
	}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, []db.ScheduleEntry{
		{ID: "se-ready", ChannelID: "ch", StartMs: 0, MediaID: "m-ready", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
		{ID: "se-overlap", ChannelID: "ch", StartMs: 6000, MediaID: "m-overlap", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
		{ID: "se-gap", ChannelID: "ch", StartMs: 60000, MediaID: "m-gap", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
		{ID: "se-unready", ChannelID: "ch", StartMs: 72000, MediaID: "m-unready", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if _, err := conn.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("disable check constraints: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES ('se-invalid', 'ch', 84000, 'm-invalid', 0, 7000, 'se-unready', 0)`); err != nil {
		t.Fatalf("insert invalid schedule: %v", err)
	}
	if _, err := conn.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES ('se-missing-media', 'ch', 96000, 'missing-media', 0, 12000, 'se-invalid', 0)`); err != nil {
		t.Fatalf("insert missing media schedule: %v", err)
	}

	issues, err := checkScheduleIntegrity(conn, integrityOptions{ChannelID: "ch", FromMs: 0, ToMs: 120000, GapMs: 30000})
	if err != nil {
		t.Fatalf("check integrity: %v", err)
	}
	kinds := map[string]int{}
	for _, issue := range issues {
		kinds[issue.Kind]++
	}
	for _, kind := range []string{"overlap", "gap", "package_not_ready", "invalid_alignment", "missing_media"} {
		if kinds[kind] == 0 {
			t.Fatalf("missing issue kind %s in %+v", kind, issues)
		}
	}
}
