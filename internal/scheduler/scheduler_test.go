package scheduler

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestBuildEntriesLoopsMediaAndPreservesOffsets(t *testing.T) {
	media := []db.Media{
		mediaRow("m1", "", 12_000),
		mediaRow("m2", "", 6_000),
	}
	entries, err := BuildEntries("ch", "alphabetical", media, 0, 30_000)
	if err != nil {
		t.Fatalf("BuildEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries)=%d, want 3", len(entries))
	}
	want := []struct {
		mediaID  string
		startMs  int64
		offsetMs int64
		durMs    int64
	}{
		{"m1", 0, 0, 12_000},
		{"m2", 12_000, 0, 6_000},
		{"m1", 18_000, 0, 12_000},
	}
	for i, w := range want {
		if entries[i].MediaID != w.mediaID || entries[i].StartMs != w.startMs || entries[i].OffsetMs != w.offsetMs || entries[i].DurationMs != w.durMs {
			t.Fatalf("entry[%d]=%+v, want media=%s start=%d offset=%d dur=%d", i, entries[i], w.mediaID, w.startMs, w.offsetMs, w.durMs)
		}
	}
}

func TestBuildEntriesSingleMediaLoopsToFillHorizon(t *testing.T) {
	// A channel with one episode should loop it to fill the schedule horizon.
	// 12s media, 1-hour horizon → 300 entries.
	media := []db.Media{mediaRow("m1", "", 12_000)}
	entries, err := BuildEntries("ch", "alphabetical", media, 0, 60*60*1000)
	if err != nil {
		t.Fatalf("BuildEntries: %v", err)
	}
	want := 60 * 60 * 1000 / 12_000 // 300
	if len(entries) != want {
		t.Fatalf("len(entries)=%d, want %d", len(entries), want)
	}
	for _, e := range entries {
		if e.MediaID != "m1" {
			t.Fatalf("unexpected mediaID %s", e.MediaID)
		}
	}
}

func TestBuildEntriesDoesNotWriteShortTail(t *testing.T) {
	media := []db.Media{mediaRow("m1", "", 12_000)}
	entries, err := BuildEntries("ch", "alphabetical", media, 0, 18_000)
	if err != nil {
		t.Fatalf("BuildEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries)=%d, want 1", len(entries))
	}
	if entries[0].DurationMs != 12_000 {
		t.Fatalf("duration=%d, want 12000", entries[0].DurationMs)
	}
}

func TestExtendChannelContinuesFromExistingSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := db.InsertChannel(context.Background(), conn, db.ChannelWrite{
		ID:              "ch",
		DisplayName:     "Channel",
		SourceDirectory: "/tmp",
		Ordering:        "alphabetical",
		CreatedAtMs:     1,
	}); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 600000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m1", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	seedReadyPackage(t, conn, "m1")
	start := Align6s(time.Now().UTC().Add(10 * time.Minute).UnixMilli())
	existing := db.ScheduleEntry{ID: "se0", ChannelID: "ch", StartMs: start, MediaID: "m1", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, []db.ScheduleEntry{existing}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	res, err := ExtendChannel(context.Background(), conn, "ch", ServiceOptions{HorizonHours: 1})
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if res.Inserted == 0 {
		t.Fatalf("inserted=0, want continuation entries")
	}
	entries, err := db.ScheduleWindow(context.Background(), conn, "ch", start, res.LastEndMs)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("entries=%+v, want continuation after seed row", entries)
	}
	if entries[1].StartMs != start+6000 {
		t.Fatalf("second start=%d, want continuation at %d", entries[1].StartMs, start+6000)
	}
	ordered, err := db.ScheduleEntriesOrdered(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("ordered schedule: %v", err)
	}
	if len(ordered) < 2 {
		t.Fatalf("ordered schedule=%+v, want appended entries", ordered)
	}
	if ordered[0].ID != existing.ID {
		t.Fatalf("head id=%s, want %s", ordered[0].ID, existing.ID)
	}
	if ordered[1].AnchorScheduleEntryID == nil || *ordered[1].AnchorScheduleEntryID != existing.ID {
		t.Fatalf("second anchor=%+v, want %s", ordered[1].AnchorScheduleEntryID, existing.ID)
	}
	issues, err := db.ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("validate chains returned issues: %+v", issues)
	}
}

func TestExtendAllEnabledContinuesAfterChannelError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	for _, id := range []string{"bad", "ok"} {
		if err := db.InsertChannel(context.Background(), conn, db.ChannelWrite{
			ID:              id,
			DisplayName:     id,
			SourceDirectory: "/tmp",
			Ordering:        "alphabetical",
			CreatedAtMs:     1,
		}); err != nil {
			t.Fatalf("insert channel %s: %v", id, err)
		}
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 600000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ok", "m1", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	seedReadyPackage(t, conn, "m1")

	result, err := ExtendAllEnabled(context.Background(), conn, ServiceOptions{HorizonHours: 1})
	if err != nil {
		t.Fatalf("extend all: %v", err)
	}
	if len(result.Channels) != 2 {
		t.Fatalf("channels=%d, want 2", len(result.Channels))
	}
	if result.Channels[0].ChannelID != "bad" || result.Channels[0].Error == "" {
		t.Fatalf("bad result=%+v, want per-channel error", result.Channels[0])
	}
	if result.Channels[1].ChannelID != "ok" || result.Channels[1].Error != "" || result.Channels[1].Inserted == 0 {
		t.Fatalf("ok result=%+v, want successful extension", result.Channels[1])
	}
}

func TestExtendChannelRequiresReadyPackagedMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := db.InsertChannel(context.Background(), conn, db.ChannelWrite{
		ID:              "ch",
		DisplayName:     "Channel",
		SourceDirectory: "/tmp",
		Ordering:        "alphabetical",
		CreatedAtMs:     1,
	}); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 600000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m1", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}

	_, err = ExtendChannel(context.Background(), conn, "ch", ServiceOptions{HorizonHours: 1})
	if err == nil {
		t.Fatal("extend succeeded without ready package")
	}
}

func TestExtendChannelBootstrapRequiresAllReadyPackages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := db.InsertChannel(context.Background(), conn, db.ChannelWrite{
		ID:              "ch",
		DisplayName:     "Channel",
		SourceDirectory: "/tmp",
		Ordering:        "alphabetical",
		CreatedAtMs:     1,
	}); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES
		('m1', '/tmp/m1.mkv', '/tmp', 600000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		('m2', '/tmp/m2.mkv', '/tmp', 600000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m1", 0); err != nil {
		t.Fatalf("add channel media m1: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add channel media m2: %v", err)
	}
	seedReadyPackage(t, conn, "m1")

	res, err := ExtendChannel(context.Background(), conn, "ch", ServiceOptions{
		HorizonHours:             1,
		BootstrapRequireAllReady: true,
	})
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if !res.BootstrapDelayed || res.BootstrapReady != 1 || res.BootstrapTotal != 2 {
		t.Fatalf("result=%+v, want delayed with 1/2 ready", res)
	}
	count, err := db.CountScheduleEntries(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("count schedule: %v", err)
	}
	if count != 0 {
		t.Fatalf("schedule entries=%d, want 0 while bootstrap is delayed", count)
	}

	seedReadyPackage(t, conn, "m2")
	res, err = ExtendChannel(context.Background(), conn, "ch", ServiceOptions{
		HorizonHours:             1,
		BootstrapRequireAllReady: true,
	})
	if err != nil {
		t.Fatalf("extend after ready: %v", err)
	}
	if res.BootstrapDelayed || res.Inserted == 0 {
		t.Fatalf("result=%+v, want schedule inserted after all packages are ready", res)
	}
}

func TestPreviewChannelDoesNotWriteScheduleEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := db.InsertChannel(context.Background(), conn, db.ChannelWrite{
		ID:              "ch",
		DisplayName:     "Channel",
		SourceDirectory: "/tmp",
		Ordering:        "alphabetical",
		CreatedAtMs:     1,
	}); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 600000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m1", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	seedReadyPackage(t, conn, "m1")

	fromMs := Align6s(time.Now().UTC().UnixMilli())
	preview, err := PreviewChannel(context.Background(), conn, "ch", PreviewOptions{
		FromMs:     fromMs,
		DurationMs: int64(time.Hour / time.Millisecond),
		NowMs:      fromMs,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Entries) == 0 {
		t.Fatalf("preview entries empty")
	}
	if preview.GeneratedEndMs <= preview.FromMs {
		t.Fatalf("generatedEndMs=%d from=%d", preview.GeneratedEndMs, preview.FromMs)
	}
	count, err := db.CountScheduleEntries(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("count schedule: %v", err)
	}
	if count != 0 {
		t.Fatalf("schedule rows=%d, want 0 after preview", count)
	}
}

func TestPreviewChannelWarnsWhenPackagesAreMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := db.InsertChannel(context.Background(), conn, db.ChannelWrite{
		ID:              "ch",
		DisplayName:     "Channel",
		SourceDirectory: "/tmp",
		Ordering:        "alphabetical",
		CreatedAtMs:     1,
	}); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 600000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m1", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}

	preview, err := PreviewChannel(context.Background(), conn, "ch", PreviewOptions{
		FromMs:     Align6s(time.Now().UTC().UnixMilli()),
		DurationMs: int64(time.Hour / time.Millisecond),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Entries) != 0 {
		t.Fatalf("entries=%+v, want none without ready packages", preview.Entries)
	}
	if len(preview.Warnings) != 1 || preview.Warnings[0].Code != "no_ready_packages" {
		t.Fatalf("warnings=%+v, want no_ready_packages", preview.Warnings)
	}
}

func seedReadyPackage(t *testing.T, conn *sql.DB, mediaID string) {
	t.Helper()
	initPath := "/tmp/init.mp4"
	pkgDur := int64(600000)
	pkg := db.MediaPackage{
		ID:                 "pkg-" + mediaID,
		MediaID:            mediaID,
		RenditionProfile:   db.DefaultPackageProfile,
		Status:             db.PackageStatusReady,
		InitSegmentPath:    &initPath,
		PackagedDurationMs: &pkgDur,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.ReplacePackagedSegments(context.Background(), conn, pkg.ID, []db.PackagedSegment{
		{PackageID: pkg.ID, SegmentNumber: 0, MediaStartMs: 0, DurationMs: 6000, Path: strptr("/tmp/0.m4s")},
	}); err != nil {
		t.Fatalf("replace packaged segments: %v", err)
	}
}

func strptr(s string) *string { return &s }
