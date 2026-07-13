package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleGuideReturnsTrimmedScheduleEntries(t *testing.T) {
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
		       ('hidden', 'Hidden', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 1)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms, title)
		VALUES ('m1', '/tmp/secret-path.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0, 'My Show')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('e1', 'vod one', 0, 'm1', 0, 18000, 0),
		       ('e2', 'hidden', 0, 'm1', 0, 18000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "vod one"); err != nil {
		t.Fatalf("backfill vod one anchors: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "hidden"); err != nil {
		t.Fatalf("backfill hidden anchors: %v", err)
	}

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(6000).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/guide?from=0&hours=6", nil)
	res := httptest.NewRecorder()

	app.handleGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if got := res.Body.String(); strings.Contains(got, "secret-path.mkv") {
		t.Fatalf("response leaked filesystem path: %s", got)
	}
	var body guideResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.NowMs != 6000 || body.FromMs != 0 || body.ToMs != 6*3600*1000 {
		t.Fatalf("window fields=%+v", body)
	}
	if len(body.Channels) != 1 {
		t.Fatalf("channels=%+v, want one (hidden excluded)", body.Channels)
	}
	ch := body.Channels[0]
	if ch.ID != "vod one" || ch.IsExternal {
		t.Fatalf("unexpected channel identity: %+v", ch)
	}
	if ch.ScheduleEndMs == nil || *ch.ScheduleEndMs != 18000 {
		t.Fatalf("scheduleEndMs=%v, want 18000", ch.ScheduleEndMs)
	}
	if len(ch.Entries) != 1 {
		t.Fatalf("entries=%+v, want one", ch.Entries)
	}
	e := ch.Entries[0]
	if e.EntryID != "e1" || e.MediaID != "m1" || e.Title != "My Show" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if e.StartMs != 0 || e.EndMs != 18000 || e.DurationMs != 18000 {
		t.Fatalf("unexpected entry timing: %+v", e)
	}
}

func TestHandleGuideReturnsExternalChannelAsLive(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/now-playing":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Song","artist":"Artist","playing":true}`))
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
	req := httptest.NewRequest(http.MethodGet, "/api/guide", nil)
	res := httptest.NewRecorder()

	app.handleGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body guideResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Channels) != 1 {
		t.Fatalf("channels=%+v, want one", body.Channels)
	}
	ch := body.Channels[0]
	if ch.ID != "spotify" || !ch.IsExternal || ch.Status != "live" {
		t.Fatalf("unexpected external channel: %+v", ch)
	}
	if len(ch.Entries) != 0 {
		t.Fatalf("external entries=%+v, want none", ch.Entries)
	}
	if ch.NowPlaying == nil || ch.NowPlaying.Title != "Song" || !ch.NowPlaying.Playing {
		t.Fatalf("nowPlaying=%+v", ch.NowPlaying)
	}
}

func TestHandleGuideReportsExternalChannelDownWhenUpstreamUnreachable(t *testing.T) {
	// Start then immediately close so the upstream address refuses connections.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstreamURL := upstream.URL
	upstream.Close()

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
		upstreamURL+"/hls/stream.m3u8"); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(6000).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/guide", nil)
	res := httptest.NewRecorder()
	app.handleGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body guideResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Channels) != 1 {
		t.Fatalf("channels=%+v, want one", body.Channels)
	}
	if ch := body.Channels[0]; !ch.IsExternal || ch.Status != "down" {
		t.Fatalf("want external channel down, got %+v", ch)
	}
}

func TestHandleGuideClampsHours(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(0).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/guide?from=0&hours=9999", nil)
	res := httptest.NewRecorder()

	app.handleGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body guideResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ToMs != guideMaxHours*3600*1000 {
		t.Fatalf("toMs=%d, want clamped to %d hours", body.ToMs, guideMaxHours)
	}
}
