package db

import (
	"context"
	"testing"
)

// TestBitmapSubtitleInventory: an embedded_bitmap row records a non-text
// subtitle stream (path NULL) that is visible in the full listing but excluded
// from playback selection, carries its forced flag, and does not collide with a
// same-language bitmap stream at a different index.
func TestBitmapSubtitleInventory(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO media
		(id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1', '/tmp', 1000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	// A forced PGS track and a full PGS track, both English, on distinct streams.
	forced := MediaTrack{MediaID: "m1", Kind: "subtitle", StreamIndex: 3, Language: "eng",
		Codec: "hdmv_pgs_subtitle", Source: TrackSourceEmbeddedBitmap, Forced: true}
	full := MediaTrack{MediaID: "m1", Kind: "subtitle", StreamIndex: 4, Language: "eng",
		Codec: "hdmv_pgs_subtitle", Source: TrackSourceEmbeddedBitmap}
	for _, tr := range []MediaTrack{forced, full} {
		if err := UpsertMediaTrack(context.Background(), rw, tr); err != nil {
			t.Fatalf("upsert bitmap stream %d: %v", tr.StreamIndex, err)
		}
	}

	// Both stay distinct (same language, different stream) — the embedded index
	// keys on stream_index, not language.
	all, err := MediaTracksByMediaID(context.Background(), rw, "m1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d tracks, want 2 (same-language bitmap streams must not collide)", len(all))
	}
	var sawForced bool
	for _, tr := range all {
		if tr.Source != TrackSourceEmbeddedBitmap {
			t.Errorf("stream %d source = %q, want embedded_bitmap", tr.StreamIndex, tr.Source)
		}
		if tr.Path != nil {
			t.Errorf("stream %d has non-nil path; bitmap inventory rows must stay NULL", tr.StreamIndex)
		}
		if tr.StreamIndex == 3 {
			sawForced = tr.Forced
		}
	}
	if !sawForced {
		t.Error("forced flag not persisted on stream 3")
	}

	// Excluded from playback selection (path IS NULL).
	pref, err := PreferredSubtitleTracksByMediaID(context.Background(), rw, "m1")
	if err != nil {
		t.Fatalf("preferred: %v", err)
	}
	if len(pref) != 0 {
		t.Errorf("preferred returned %d bitmap rows, want 0 (NULL path must be excluded)", len(pref))
	}
	has, err := HasSubtitleTrackForLang(context.Background(), rw, "m1", "eng")
	if err != nil {
		t.Fatalf("has: %v", err)
	}
	if has {
		t.Error("HasSubtitleTrackForLang true for a bitmap-only language, want false")
	}
	bitmap, err := BitmapSubtitleTracksForMedia(context.Background(), rw, "m1")
	if err != nil {
		t.Fatalf("bitmap tracks: %v", err)
	}
	if len(bitmap) != 2 || bitmap[0].StreamIndex != 3 || bitmap[1].StreamIndex != 4 {
		t.Fatalf("bitmap tracks = %+v, want forced stream 3 then stream 4", bitmap)
	}

	// Re-upserting the same stream updates in place (idempotent re-package).
	if err := UpsertMediaTrack(context.Background(), rw, full); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	all, _ = MediaTracksByMediaID(context.Background(), rw, "m1")
	if len(all) != 2 {
		t.Fatalf("after re-upsert got %d tracks, want 2 (must be idempotent)", len(all))
	}
}

// TestMigrateV20toV21PreservesTracks simulates a v20 media_tracks (no forced
// column), inserts a row, rewinds schema_version, and re-runs ApplySchema. The
// existing row must survive with forced defaulting to 0, and embedded_bitmap
// must be insertable afterward.
func TestMigrateV20toV21PreservesTracks(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO media
		(id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1', '/tmp', 1000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	// Simulate a v20 table: drop the forced column and rewind the version so the
	// v20->v21 rebuild re-runs. (CHECK/index shape is recreated by the rebuild.)
	if _, err := rw.Exec(`ALTER TABLE media_tracks DROP COLUMN forced`); err != nil {
		t.Fatalf("simulate v20 (drop forced): %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media_tracks
		(media_id, kind, stream_index, language, codec, source, default_flag, path)
		VALUES ('m1', 'subtitle', 2, 'eng', 'webvtt', 'embedded_text', 1, '/tmp/m1/sub.vtt')`); err != nil {
		t.Fatalf("insert legacy track: %v", err)
	}
	if _, err := rw.Exec(`UPDATE meta SET value = '20' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("rewind schema_version: %v", err)
	}

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema (migration): %v", err)
	}
	if err := VerifySchema(context.Background(), rw); err != nil {
		t.Fatalf("VerifySchema after migration: %v", err)
	}

	tracks, err := MediaTracksByMediaID(context.Background(), rw, "m1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks after migration, want 1 (row must be preserved)", len(tracks))
	}
	if tracks[0].Forced {
		t.Error("migrated row forced = true, want false (default backfill)")
	}
	if tracks[0].Source != TrackSourceEmbedded || tracks[0].Path == nil {
		t.Errorf("migrated row not preserved faithfully: %+v", tracks[0])
	}

	// The widened CHECK now admits embedded_bitmap.
	if err := UpsertMediaTrack(context.Background(), rw, MediaTrack{MediaID: "m1", Kind: "subtitle", StreamIndex: 5,
		Language: "fra", Codec: "dvd_subtitle", Source: TrackSourceEmbeddedBitmap, Forced: true}); err != nil {
		t.Fatalf("insert embedded_bitmap after migration: %v", err)
	}
}
