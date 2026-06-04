package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestEnabledChannelsAndSchedule(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
        VALUES ('ch1', 'Channel One', '/tmp', 'alphabetical', 1, 0),
               ('ch2', 'Channel Two', '/tmp', 'alphabetical', 0, 0)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
        video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
        VALUES ('m1', '/tmp/m1.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := InsertScheduleEntries(context.Background(), rw, []ScheduleEntry{
		{ChannelID: "ch1", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
		{ChannelID: "ch1", StartMs: 6000, MediaID: "m1", OffsetMs: 0, DurationMs: 6000, CreatedAtMs: 0},
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer ro.Close()

	chans, err := EnabledChannels(context.Background(), ro)
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if len(chans) != 1 || chans[0].ID != "ch1" {
		t.Fatalf("want ch1 only, got %+v", chans)
	}
	if chans[0].PlaybackMode != PlaybackModePackaged {
		t.Fatalf("default playback mode = %q, want packaged", chans[0].PlaybackMode)
	}

	entries, err := ScheduleWindow(context.Background(), ro, "ch1", 0, 12000)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}

	m, err := MediaByID(context.Background(), ro, "m1")
	if err != nil || m == nil || m.DurationMs != 6000 {
		t.Fatalf("media lookup: %v %+v", err, m)
	}

	if _, err := MediaByID(context.Background(), ro, "missing"); err != nil {
		t.Fatalf("missing should be (nil,nil): %v", err)
	}

	has, err := ChannelHasSchedule(context.Background(), ro, "ch1")
	if err != nil || !has {
		t.Fatalf("expected ch1 has schedule: %v %v", has, err)
	}
}

func TestChannelPlaybackPolicy(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
        VALUES ('ch1', 'Channel One', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	ch, err := ChannelByID(context.Background(), rw, "ch1")
	if err != nil {
		t.Fatalf("channel by id: %v", err)
	}
	if ch.PlaybackMode != PlaybackModePackaged || ch.RequiredPackageProfile != "" || ch.PackagePrefillMs != nil {
		t.Fatalf("unexpected default policy: %+v", ch)
	}

	prefill := int64(86400000)
	updated, err := UpdateChannelPlaybackPolicy(context.Background(), rw, "ch1", PlaybackModePackaged,
		"h264-main-1080p", &prefill, MediaKindVideo)
	if err != nil || !updated {
		t.Fatalf("update packaged policy updated=%v err=%v", updated, err)
	}
	ch, err = ChannelByID(context.Background(), rw, "ch1")
	if err != nil {
		t.Fatalf("channel by id after update: %v", err)
	}
	if ch.PlaybackMode != PlaybackModePackaged ||
		ch.RequiredPackageProfile != "h264-main-1080p" || ch.PackagePrefillMs == nil ||
		*ch.PackagePrefillMs != 86400000 {
		t.Fatalf("unexpected packaged policy: %+v", ch)
	}

	if _, err := UpdateChannelPlaybackPolicy(context.Background(), rw, "ch1", PlaybackModePackaged, "", nil, MediaKindVideo); err != nil {
		t.Fatalf("expected packaged policy without profile to default successfully: %v", err)
	}
	if _, err := UpdateChannelPlaybackPolicy(context.Background(), rw, "ch1", PlaybackModeGenerated, "", nil, MediaKindVideo); err == nil {
		t.Fatalf("expected generated policy to fail")
	}
}

func TestEnabledGuideChannelsExcludeHidden(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms, hidden_from_guide)
        VALUES ('visible', 'Visible', '/tmp', 'alphabetical', 1, 0, 0),
               ('hidden', 'Hidden', '/tmp', 'alphabetical', 1, 0, 1),
               ('disabled', 'Disabled', '/tmp', 'alphabetical', 0, 0, 0)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}

	enabled, err := EnabledChannels(context.Background(), rw)
	if err != nil {
		t.Fatalf("enabled channels: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("enabled=%+v, want visible + hidden", enabled)
	}
	guide, err := EnabledGuideChannels(context.Background(), rw)
	if err != nil {
		t.Fatalf("enabled guide channels: %v", err)
	}
	if len(guide) != 1 || guide[0].ID != "visible" {
		t.Fatalf("guide=%+v, want visible only", guide)
	}
}

func TestCloneChannelCopiesConfigAndMediaWithoutSchedule(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			description, playback_mode, required_package_profile, package_prefill_ms
		)
		VALUES ('ch1', 'Channel One', '/tmp/src', 'block', 1, 1000,
			'desc', 'packaged', 'h264-main-1080p', 86400000)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/src/m1.mkv', '/tmp/src', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('m2', '/tmp/src/m2.mkv', '/tmp/src', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm1', NULL, 111), ('ch1', 'm2', 'm1', 222)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('s1', 'ch1', 0, 'm1', 0, 6000, 1000)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	clone, err := CloneChannel(context.Background(), rw, "ch1", 2000)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ID != "ch1-copy" || clone.DisplayName != "Channel One Copy" {
		t.Fatalf("unexpected clone identity: %+v", clone)
	}
	if clone.SourceDirectory != "/tmp/src" || clone.Ordering != "block" || clone.Enabled ||
		clone.CreatedAtMs != 2000 || clone.Description != "desc" ||
		clone.HiddenFromGuide ||
		clone.PlaybackMode != PlaybackModePackaged ||
		clone.RequiredPackageProfile != "h264-main-1080p" ||
		clone.PackagePrefillMs == nil || *clone.PackagePrefillMs != 86400000 {
		t.Fatalf("clone did not preserve config: %+v", clone)
	}
	rows, err := ChannelMediaList(context.Background(), rw, clone.ID)
	if err != nil {
		t.Fatalf("clone media: %v", err)
	}
	if len(rows) != 2 || rows[0].MediaID != "m1" || rows[0].AddedAtMs != 111 ||
		rows[1].MediaID != "m2" || rows[1].AddedAtMs != 222 {
		t.Fatalf("unexpected cloned media rows: %+v", rows)
	}
	var scheduleCount int
	if err := rw.QueryRow(`SELECT COUNT(*) FROM schedule_entries WHERE channel_id = ?`, clone.ID).Scan(&scheduleCount); err != nil {
		t.Fatalf("count clone schedule: %v", err)
	}
	if scheduleCount != 0 {
		t.Fatalf("clone copied schedule rows: %d", scheduleCount)
	}
}

func TestCloneChannelNameCollisionsAreDeterministic(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
        VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0),
               ('ch-copy', 'Channel Copy', '/tmp', 'alphabetical', 1, 0),
               ('ch-copy-2', 'Channel Copy 2', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	clone, err := CloneChannel(context.Background(), rw, "ch", 3000)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ID != "ch-copy-3" || clone.DisplayName != "Channel Copy 3" {
		t.Fatalf("unexpected collision result: %+v", clone)
	}
}

func TestCloneChannelMissingReturnsNoRows(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	_, err = CloneChannel(context.Background(), rw, "missing", 0)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err=%v, want sql.ErrNoRows", err)
	}
}
