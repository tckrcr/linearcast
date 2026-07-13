package db

import (
	"context"
	"fmt"
	"testing"
)

// TestPackageBitmapSubtitleInventory: an embedded_bitmap row records a non-text
// subtitle stream (path NULL) that is visible in the full listing but excluded
// from playback selection, carries its forced flag, and does not collide with a
// same-language bitmap stream at a different index.
func TestPackageBitmapSubtitleInventory(t *testing.T) {
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
	if _, err := rw.Exec(`INSERT INTO media_packages
		(id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('pkg', 'm1', 'prof', 'ready', 0, 0)`); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	// A forced PGS track and a full PGS track, both English, on distinct streams.
	forced := PackageTrack{PackageID: "pkg", Kind: "subtitle", StreamIndex: 3, Language: "eng", Title: "English (Forced)",
		Codec: "hdmv_pgs_subtitle", Source: TrackSourceEmbeddedBitmap, Forced: true}
	full := PackageTrack{PackageID: "pkg", Kind: "subtitle", StreamIndex: 4, Language: "eng", Title: "English (SDH)",
		Codec: "hdmv_pgs_subtitle", Source: TrackSourceEmbeddedBitmap}
	for _, tr := range []PackageTrack{forced, full} {
		if err := UpsertPackageTrack(context.Background(), rw, tr); err != nil {
			t.Fatalf("upsert bitmap stream %d: %v", tr.StreamIndex, err)
		}
	}

	// Both stay distinct (same language, different stream) — the embedded index
	// keys on stream_index, not language.
	all, err := PackageTracksByPackageID(context.Background(), rw, "pkg")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d tracks, want 2 (same-language bitmap streams must not collide)", len(all))
	}
	var sawForced bool
	var forcedTitle string
	for _, tr := range all {
		if tr.Source != TrackSourceEmbeddedBitmap {
			t.Errorf("stream %d source = %q, want embedded_bitmap", tr.StreamIndex, tr.Source)
		}
		if tr.Path != nil {
			t.Errorf("stream %d has non-nil path; bitmap inventory rows must stay NULL", tr.StreamIndex)
		}
		if tr.StreamIndex == 3 {
			sawForced = tr.Forced
			forcedTitle = tr.Title
		}
	}
	if !sawForced {
		t.Error("forced flag not persisted on stream 3")
	}
	if forcedTitle != "English (Forced)" {
		t.Fatalf("forced title = %q, want English (Forced)", forcedTitle)
	}

	// Excluded from playback selection (path IS NULL).
	if pref := PreferredSubtitleTrack(all, "eng"); pref != nil {
		t.Errorf("preferred returned bitmap row %+v, want nil (NULL path must be excluded)", pref)
	}
	// Re-upserting the same stream updates in place (idempotent re-package).
	if err := UpsertPackageTrack(context.Background(), rw, full); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	all, _ = PackageTracksByPackageID(context.Background(), rw, "pkg")
	if len(all) != 2 {
		t.Fatalf("after re-upsert got %d tracks, want 2 (must be idempotent)", len(all))
	}
}

func textSub(streamIndex int, language string, forced, hi bool) PackageTrack {
	p := fmt.Sprintf("/tmp/subs/s%d.vtt", streamIndex)
	return PackageTrack{
		PackageID: "pkg", Kind: "subtitle", StreamIndex: streamIndex,
		Language: language, Codec: "webvtt", Source: TrackSourceEmbedded,
		Forced: forced, HearingImpaired: hi, Path: &p,
	}
}

// TestPreferredSubtitleTrackExcludesForced: the Rings of Power shape — a forced
// narrative track at the lowest stream index, full dialogue, then SDH. The
// plain per-language pick must be the full dialogue track, never the forced or
// SDH one, so the CC toggle shows real subtitles.
func TestPreferredSubtitleTrackExcludesForced(t *testing.T) {
	forced := textSub(2, "eng", true, false)
	dialogue := textSub(3, "eng", false, false)
	sdh := textSub(4, "eng", false, true)

	if got := PreferredSubtitleTrack([]PackageTrack{forced, dialogue, sdh}, "eng"); got == nil || got.StreamIndex != 3 {
		t.Fatalf("preferred = %+v, want dialogue stream 3", got)
	}
	if got := ForcedSubtitleTrack([]PackageTrack{forced, dialogue, sdh}, "eng"); got == nil || got.StreamIndex != 2 {
		t.Fatalf("forced = %+v, want forced stream 2", got)
	}
	// SDH-only English (E02 shape): SDH is the only non-forced dialogue, so it
	// is served rather than nothing.
	if got := PreferredSubtitleTrack([]PackageTrack{forced, sdh}, "eng"); got == nil || got.StreamIndex != 4 {
		t.Fatalf("SDH-only preferred = %+v, want SDH stream 4", got)
	}
	// Forced-only English resolves to no plain rendition.
	if got := PreferredSubtitleTrack([]PackageTrack{forced}, "eng"); got != nil {
		t.Fatalf("forced-only preferred = %+v, want nil", got)
	}
	// Language isolation: an English request ignores a French dialogue track.
	if got := PreferredSubtitleTrack([]PackageTrack{textSub(2, "fre", false, false)}, "eng"); got != nil {
		t.Fatalf("cross-language preferred = %+v, want nil", got)
	}
}

// TestPackageSubtitleTracksForMediaIDsPrefersDialogue: the master-playlist query
// collapses to one non-forced track per language and prefers full dialogue over
// SDH, matching PreferredSubtitleTrack.
func TestPackageSubtitleTracksForMediaIDsPrefersDialogue(t *testing.T) {
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
	if _, err := rw.Exec(`INSERT INTO media_packages
		(id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('pkg', 'm1', 'prof', 'ready', 0, 0)`); err != nil {
		t.Fatalf("insert package: %v", err)
	}
	for _, tr := range []PackageTrack{
		textSub(2, "eng", true, false),  // forced
		textSub(3, "eng", false, false), // dialogue
		textSub(4, "eng", false, true),  // SDH
	} {
		if err := UpsertPackageTrack(context.Background(), rw, tr); err != nil {
			t.Fatalf("upsert stream %d: %v", tr.StreamIndex, err)
		}
	}
	got, err := PackageSubtitleTracksForMediaIDs(context.Background(), rw, []string{"m1"}, "prof")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d advertised tracks, want 1 per language", len(got))
	}
	if got[0].StreamIndex != 3 || got[0].Forced {
		t.Fatalf("advertised track = %+v, want non-forced dialogue stream 3", got[0])
	}

	forced, err := ForcedPackageSubtitleTracksForMediaIDs(context.Background(), rw, []string{"m1"}, "prof")
	if err != nil {
		t.Fatalf("forced query: %v", err)
	}
	if len(forced) != 1 {
		t.Fatalf("got %d forced tracks, want 1 per language", len(forced))
	}
	if forced[0].StreamIndex != 2 || !forced[0].Forced {
		t.Fatalf("forced advertised track = %+v, want forced stream 2", forced[0])
	}
}

func TestPackageTracksCascadeWithMediaPackage(t *testing.T) {
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
	if _, err := rw.Exec(`INSERT INTO media_packages
		(id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('pkg', 'm1', 'prof', 'ready', 0, 0)`); err != nil {
		t.Fatalf("insert package: %v", err)
	}
	if err := UpsertPackageTrack(context.Background(), rw, textSub(3, "eng", false, false)); err != nil {
		t.Fatalf("upsert track: %v", err)
	}
	if err := DeleteMediaPackage(context.Background(), rw, "pkg"); err != nil {
		t.Fatalf("delete package: %v", err)
	}
	var got int
	if err := rw.QueryRow(`SELECT COUNT(*) FROM package_tracks WHERE package_id = 'pkg'`).Scan(&got); err != nil {
		t.Fatalf("count package tracks: %v", err)
	}
	if got != 0 {
		t.Fatalf("package_tracks rows = %d, want cascade delete", got)
	}
}
