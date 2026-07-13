package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandlePlayableSourcesReturnsVODManifestURLs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, hidden_from_guide
		)
		VALUES ('vod one', 'VOD One', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 0),
		       ('hidden', 'Hidden', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 1),
		       ('disabled', 'Disabled', '/tmp', 'alphabetical', 0, 0, 'packaged', 'h264-1080p-8mbps', 0)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'vod one', 0, 'm1', 0, 18000, 0),
		       (lower(hex(randomblob(16))), 'hidden', 0, 'm1', 0, 18000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "vod one"); err != nil {
		t.Fatalf("backfill vod one anchors: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "hidden"); err != nil {
		t.Fatalf("backfill hidden anchors: %v", err)
	}
	pkgDur := int64(18000)
	pkg := db.MediaPackage{
		ID:                 "pkg-m1",
		MediaID:            "m1",
		RenditionProfile:   db.DefaultPackageProfile,
		Status:             db.PackageStatusReady,
		PackagedDurationMs: &pkgDur,
		CreatedAtMs:        0,
		UpdatedAtMs:        0,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(6000).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/playable-sources", nil)
	res := httptest.NewRecorder()

	app.handlePlayableSources(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body playableSourcesResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.NowMs != 6000 {
		t.Fatalf("nowMs=%d, want 6000", body.NowMs)
	}
	if len(body.Sources) != 1 {
		t.Fatalf("sources=%+v, want one enabled source", body.Sources)
	}
	got := body.Sources[0]
	if got.ID != "vod one" || got.DisplayName != "VOD One" || got.Kind != "vod" || got.PlaybackType != "hls" {
		t.Fatalf("unexpected source identity: %+v", got)
	}
	if got.ManifestURL != "/hls/channels/vod%20one/stream.m3u8" {
		t.Fatalf("manifestUrl=%q", got.ManifestURL)
	}
	if got.Status != "playing" || got.Current == nil || got.Current.MediaID != "m1" {
		t.Fatalf("unexpected source playback state: %+v", got)
	}
	if got.Current.PackageStatus != string(db.PackageStatusReady) {
		t.Fatalf("current packageStatus=%q, want ready", got.Current.PackageStatus)
	}
}

func TestHandlePlayableSourcesIncludesCurrentPackageFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('vod', 'VOD', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/missing.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (lower(hex(randomblob(16))), 'vod', 0, 'm1', 0, 18000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "vod"); err != nil {
		t.Fatalf("backfill vod anchors: %v", err)
	}
	errStr := "source file unavailable: stat /tmp/missing.mkv: no such file or directory"
	pkg := db.MediaPackage{
		ID:               "pkg-m1",
		MediaID:          "m1",
		RenditionProfile: db.DefaultPackageProfile,
		Status:           db.PackageStatusFailed,
		Error:            &errStr,
		CreatedAtMs:      0,
		UpdatedAtMs:      0,
	}
	if err := db.UpsertMediaPackage(context.Background(), conn, pkg); err != nil {
		t.Fatalf("upsert package: %v", err)
	}

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(6000).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/playable-sources", nil)
	res := httptest.NewRecorder()

	app.handlePlayableSources(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body playableSourcesResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Sources) != 1 || body.Sources[0].Current == nil {
		t.Fatalf("sources=%+v, want one source with current", body.Sources)
	}
	cur := body.Sources[0].Current
	if cur.PackageStatus != string(db.PackageStatusFailed) || cur.PackageError == "" {
		t.Fatalf("current package fields=%+v, want failed with error", cur)
	}
}

func TestHandlePlayableSourcesReturnsExternalHLSManifestURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/now-playing":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Song","artist":"Artist","album":"Album","artUrl":"http://example.test/art.jpg","playing":true}`))
		case "/hls/stream.m3u8":
			// The heartbeat probes the upstream manifest to derive live/down.
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"))
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
	t.Cleanup(func() { _ = conn.Close() })
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

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(6000).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/playable-sources", nil)
	res := httptest.NewRecorder()

	app.handlePlayableSources(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body playableSourcesResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Sources) != 1 {
		t.Fatalf("sources=%+v, want one", body.Sources)
	}
	got := body.Sources[0]
	if got.ID != "spotify" || got.Kind != "live" || got.PlaybackType != "hls" || got.Status != "live" {
		t.Fatalf("unexpected external source: %+v", got)
	}
	if got.ManifestURL != "/hls/external/spotify/stream.m3u8" {
		t.Fatalf("manifestUrl=%q", got.ManifestURL)
	}
	if got.ArtworkURL != "http://example.test/art.jpg" {
		t.Fatalf("artworkUrl=%q", got.ArtworkURL)
	}
	if got.NowPlaying == nil || got.NowPlaying.Title != "Song" || got.NowPlaying.Artist != "Artist" || !got.NowPlaying.Playing {
		t.Fatalf("nowPlaying=%+v", got.NowPlaying)
	}
}
