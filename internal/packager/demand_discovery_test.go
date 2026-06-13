package packager

import (
	"context"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

// On-demand channels must be excluded from eager discovery (branch-0) so the
// worker never pre-packages them, but an operator-enqueued pending row must
// still be picked up via the orphan-pending path (branch-1).
func TestDiscoverCandidatesExcludesOnDemandButPicksUpEnqueued(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled,
		created_at_ms, playback_mode, required_package_profile, prefill_mode)
		VALUES ('od', 'On Demand', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 'on_demand')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('od', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}

	got, err := DiscoverCandidates(ctx, conn)
	if err != nil {
		t.Fatalf("discover (eager): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("on-demand media must not be eagerly discovered, got %+v", got)
	}

	// Operator/admin requests can still enqueue a pending package; the worker
	// should find it even though eager discovery skipped the on-demand channel.
	if _, err := conn.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('p1', 'm1', 'h264-main-1080p', 'pending', 0, 0)`); err != nil {
		t.Fatalf("enqueue pending: %v", err)
	}
	got, err = DiscoverCandidates(ctx, conn)
	if err != nil {
		t.Fatalf("discover (after enqueue): %v", err)
	}
	if len(got) != 1 || got[0].MediaID != "m1" || got[0].Profile != "h264-main-1080p" {
		t.Fatalf("want enqueued m1 picked up via orphan-pending path, got %+v", got)
	}
}

func TestDiscoverCandidatesExcludesPlexRelayButPicksUpEnqueued(t *testing.T) {
	path := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled,
		created_at_ms, playback_mode, required_package_profile, prefill_mode)
		VALUES ('relay', 'Plex Relay', '', 'alphabetical', 1, 0, 'plex_relay', NULL, 'eager')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms, source_ref)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 1200000, 'mkv', 'h264', 1080, 'aac', 1, 0, 'plex://101')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('relay', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}

	got, err := DiscoverCandidates(ctx, conn)
	if err != nil {
		t.Fatalf("discover (eager): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("plex relay media must not be eagerly discovered, got %+v", got)
	}

	if _, err := conn.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('p1', 'm1', 'h264-main-1080p', 'pending', 0, 0)`); err != nil {
		t.Fatalf("enqueue pending: %v", err)
	}
	got, err = DiscoverCandidates(ctx, conn)
	if err != nil {
		t.Fatalf("discover (after enqueue): %v", err)
	}
	if len(got) != 1 || got[0].MediaID != "m1" || got[0].Profile != "h264-main-1080p" {
		t.Fatalf("want enqueued m1 picked up via orphan-pending path, got %+v", got)
	}
}
