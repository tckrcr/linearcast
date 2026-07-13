package admin

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleM3U(t *testing.T) {
	app, conn := testAdminApp(t)

	// A packaged VOD channel (with artwork), an external/live channel, and a
	// hidden channel that must not appear in the playlist.
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, hidden_from_guide, artwork_url
		)
		VALUES ('vod one', 'VOD One', '/tmp', 'alphabetical', 1, 0, 'packaged', 0, 'https://img.example.com/vod.png'),
		       ('hidden',  'Hidden',  '/tmp', 'alphabetical', 1, 0, 'packaged', 1, NULL)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, media_kind, upstream_hls_url
		)
		VALUES ('spotify', 'Spotify', '', 'alphabetical', 1, 0, 'packaged', 'music', 'https://up.example.com/x.m3u8')`); err != nil {
		t.Fatalf("insert external channel: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/m3u", nil)
	req.Header.Set("X-Forwarded-Proto", "https") // mimic nginx so URLs come out absolute https
	res := httptest.NewRecorder()

	app.handleM3U(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, "mpegurl") {
		t.Fatalf("content-type=%q, want mpegurl", ct)
	}
	body := res.Body.String()
	if !strings.HasPrefix(body, "#EXTM3U") {
		t.Fatalf("body does not start with #EXTM3U: %q", body)
	}
	for _, want := range []string{
		`tvg-id="vod one"`,
		`tvg-name="VOD One"`,
		`tvg-logo="https://img.example.com/vod.png"`,
		`group-title="linearcast"`,
		// Absolute URLs derived from X-Forwarded-Proto + Host, with the id path-escaped.
		"https://example.com/hls/channels/vod%20one/stream.m3u8",
		// External channels point at the external manifest path.
		"https://example.com/hls/external/spotify/stream.m3u8",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "hidden") || strings.Contains(body, "Hidden") {
		t.Fatalf("hidden channel leaked into playlist:\n%s", body)
	}
}

func TestHandleXMLTV(t *testing.T) {
	dbPath := t.TempDir() + "/linearcast.db"
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(t.Context(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO collections (id, name, kind, source, genres_json, created_at_ms, updated_at_ms)
		VALUES ('col1', 'My Series', 'show', 'manual', '["Drama","Comedy"]', 0, 0)`); err != nil {
		t.Fatalf("insert collection: %v", err)
	}
	// Episode title lives on media; the series name comes from the collection.
	// The path must never surface in the guide (no-leak invariant).
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms,
		title, collection_id, season_number, episode_number, description, thumb_path, content_rating)
		VALUES ('m1', '/tmp/secret-path.mkv', '/tmp', 1800000, 'mkv', 'h264', 1080, 'aac', 1, 0,
		        'The Pilot', 'col1', 1, 2, 'Episode summary', '/library/metadata/1/thumb/2', 'TV-MA')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, hidden_from_guide, artwork_url
		)
		VALUES ('vod one', 'VOD One', '/tmp', 'alphabetical', 1, 0, 'packaged', 0, 'https://img.example.com/vod.png')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('e1', 'vod one', 0, 'm1', 0, 1800000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "vod one"); err != nil {
		t.Fatalf("backfill anchors: %v", err)
	}

	// now sits 10 minutes into the [0, 30m) entry: the currently-airing programme
	// started before now, so it must still be in the window (the "now cell").
	app := New(Config{DB: conn, Now: func() time.Time { return time.UnixMilli(600000).UTC() }})
	req := httptest.NewRequest(http.MethodGet, "/api/xmltv", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	res := httptest.NewRecorder()

	app.handleXMLTV(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Fatalf("content-type=%q, want xml", ct)
	}
	if strings.Contains(res.Body.String(), "secret-path.mkv") {
		t.Fatalf("response leaked filesystem path:\n%s", res.Body.String())
	}

	var doc xmltvDoc
	if err := xml.Unmarshal(res.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal xmltv: %v\n%s", err, res.Body.String())
	}
	if len(doc.Channels) != 1 {
		t.Fatalf("channels=%+v, want one", doc.Channels)
	}
	ch := doc.Channels[0]
	if ch.ID != "vod one" || ch.DisplayName != "VOD One" {
		t.Fatalf("unexpected channel: %+v", ch)
	}
	if ch.Icon == nil || ch.Icon.Src != "https://img.example.com/vod.png" {
		t.Fatalf("channel icon=%+v, want artwork src", ch.Icon)
	}
	if len(doc.Programmes) != 1 {
		t.Fatalf("programmes=%+v, want one (currently-airing entry included)", doc.Programmes)
	}
	p := doc.Programmes[0]
	if p.Channel != "vod one" {
		t.Fatalf("programme channel=%q, want vod one", p.Channel)
	}
	// Collection becomes the title; the per-entry media title becomes the sub-title.
	if p.Title != "My Series" || p.SubTitle != "The Pilot" {
		t.Fatalf("programme title/subtitle=%q/%q, want My Series/The Pilot", p.Title, p.SubTitle)
	}
	if p.EpisodeNum == nil || p.EpisodeNum.System != "onscreen" || p.EpisodeNum.Value != "S01E02" {
		t.Fatalf("episode-num=%+v, want onscreen S01E02", p.EpisodeNum)
	}
	if p.Desc != "Episode summary" {
		t.Fatalf("desc=%q, want Episode summary", p.Desc)
	}
	if p.Icon == nil || p.Icon.Src != "https://example.com/api/art/media/m1" {
		t.Fatalf("icon=%+v, want media artwork proxy", p.Icon)
	}
	if strings.Join(p.Categories, ",") != "Drama,Comedy" {
		t.Fatalf("categories=%+v, want Drama, Comedy", p.Categories)
	}
	if p.Rating == nil || p.Rating.Value != "TV-MA" {
		t.Fatalf("rating=%+v, want TV-MA", p.Rating)
	}
	// Times are the entry's own boundaries, not the now clock.
	if p.Start != "19700101000000 +0000" || p.Stop != "19700101003000 +0000" {
		t.Fatalf("programme start/stop=%q/%q", p.Start, p.Stop)
	}
}

func TestHandleXMLTVRejectsInvalidHours(t *testing.T) {
	app, _ := testAdminApp(t)
	for _, hours := range []string{"abc", "-1", "0"} {
		req := httptest.NewRequest(http.MethodGet, "/api/xmltv?hours="+hours, nil)
		res := httptest.NewRecorder()
		app.handleXMLTV(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("hours=%q: status=%d, want 400 (body=%s)", hours, res.Code, res.Body.String())
		}
	}
}
