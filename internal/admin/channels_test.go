package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleChannelDeleteRequiresDisabledChannel(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelDelete(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict", res.Code, res.Body.String())
	}
	row, err := db.ChannelByID(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if row == nil {
		t.Fatalf("enabled channel was deleted")
	}
}

func TestHandleChannelDeleteKeepsPackagesByDefault(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, false)

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelDelete(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		ChannelID string `json:"channelID"`
		Deleted   bool   `json:"deleted"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ChannelID != "ch" || !body.Deleted {
		t.Fatalf("unexpected response: %+v", body)
	}

	row, err := db.ChannelByID(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if row != nil {
		t.Fatalf("channel still exists: %+v", row)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id = 'ch'`, 0)
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch'`, 0)
	assertCount(t, conn, `SELECT COUNT(*) FROM media WHERE id = 'm1'`, 1)
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE media_id = 'm1'`, 1)
	assertCount(t, conn, `SELECT COUNT(*) FROM packaged_segments WHERE package_id = 'pkg-m1'`, 1)
}

func TestHandleChannelDeleteReclaimEncodes(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	pkgRoot := filepath.Join(cacheDir, "packages")
	dbPath := filepath.Join(dir, "linearcast.db")

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	makeDir := func(rel string) string {
		full := filepath.Join(pkgRoot, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(filepath.Join(full, "init.mp4"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write init: %v", err)
		}
		return filepath.Clean(full)
	}
	sharedRoot := makeDir("shared/h264-main-1080p")
	soloRoot := makeDir("solo/h264-main-1080p")

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	// 'ch' is the disabled channel being deleted; 'keep' survives and also pools
	// 'shared', so 'shared' must be skipped while 'solo' (only on 'ch') is reclaimed.
	mustExec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile, hidden_from_guide)
		VALUES ('ch', 'Ch', '/tmp', 'alphabetical', 0, 0, 'packaged', 'h264-main-1080p', 0),
		       ('keep', 'Keep', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p', 0)`)
	mustExec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('shared', '/tmp/shared.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		       ('solo', '/tmp/solo.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0)`)
	// ch pool chains shared (head) -> solo; keep pool holds shared (head).
	mustExec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES
		('ch', 'shared', NULL, 0),
		('ch', 'solo', 'shared', 1),
		('keep', 'shared', NULL, 0)`)
	mustExec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, package_root, created_at_ms, updated_at_ms)
		VALUES ('pkg-shared', 'shared', 'h264-main-1080p', 'ready', ?, 0, 0),
		       ('pkg-solo', 'solo', 'h264-main-1080p', 'ready', ?, 0, 0)`, sharedRoot, soloRoot)

	app := New(Config{DB: conn, CacheDir: cacheDir, Now: func() time.Time { return time.UnixMilli(0).UTC() }})

	// Explicit reclaim deletes encodes for media no surviving channel uses.
	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch?reclaim-encodes=true", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelDelete(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	var body struct {
		Deleted bool                  `json:"deleted"`
		Reclaim encodeReclaimResponse `json:"reclaim"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Deleted {
		t.Fatalf("channel not deleted")
	}
	// 'solo' reclaimed; 'shared' skipped because 'keep' still pools it.
	if body.Reclaim.DeletedRows != 1 || body.Reclaim.SkippedRows != 1 {
		t.Fatalf("reclaim=%+v, want 1 deleted 1 skipped", body.Reclaim)
	}

	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE id = 'pkg-solo'`, 0)
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages WHERE id = 'pkg-shared'`, 1)
	if _, err := os.Stat(soloRoot); !os.IsNotExist(err) {
		t.Fatalf("solo dir still exists: err=%v", err)
	}
	if _, err := os.Stat(sharedRoot); err != nil {
		t.Fatalf("shared dir removed despite being referenced: %v", err)
	}
}

func TestHandleChannelCloneCreatesConfigCopyWithoutSchedule(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	if _, err := conn.Exec(`UPDATE channels SET package_prefill_ms = 86400000 WHERE id = 'ch'`); err != nil {
		t.Fatalf("update channel policy: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/clone", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelClone(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		SourceChannelID string `json:"sourceChannelID"`
		ChannelID       string `json:"channelID"`
		DisplayName     string `json:"displayName"`
		Enabled         bool   `json:"enabled"`
		MediaCount      int    `json:"mediaCount"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.SourceChannelID != "ch" || body.ChannelID != "ch-copy" ||
		body.DisplayName != "Channel Copy" || body.Enabled || body.MediaCount != 1 {
		t.Fatalf("unexpected response: %+v", body)
	}

	clone, err := db.ChannelByID(context.Background(), conn, body.ChannelID)
	if err != nil {
		t.Fatalf("lookup clone: %v", err)
	}
	if clone == nil {
		t.Fatalf("clone row missing")
	}
	if clone.Ordering != "alphabetical" || clone.SourceDirectory != "/tmp" ||
		clone.RequiredPackageProfile != "h264-main-1080p" ||
		clone.PackagePrefillMs == nil || *clone.PackagePrefillMs != 86400000 {
		t.Fatalf("clone config mismatch: %+v", clone)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id = 'ch-copy'`, 1)
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch-copy'`, 0)
}

func TestHandleChannelCloneMissing(t *testing.T) {
	app, _ := testAdminApp(t)

	req := httptest.NewRequest(http.MethodPost, "/api/channels/missing/clone", nil)
	req.SetPathValue("channelID", "missing")
	res := httptest.NewRecorder()

	app.handleChannelClone(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want not found", res.Code, res.Body.String())
	}
}

func TestHandleCreateChannelWithUpstreamHLSURL(t *testing.T) {
	app, conn := testAdminApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewBufferString(`{
		"displayName":"Spotify",
		"upstreamHlsUrl":"http://192.168.1.100:8080/hls/stream.m3u8"
	}`))
	res := httptest.NewRecorder()

	app.handleCreateChannel(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	ch, err := db.ChannelByID(context.Background(), conn, "spotify")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil || ch.UpstreamHLSURL == nil || *ch.UpstreamHLSURL != "http://192.168.1.100:8080/hls/stream.m3u8" {
		t.Fatalf("channel upstream hls mismatch: %+v", ch)
	}
	if ch.RequiredPackageProfile != "" || ch.SourceDirectory != "" || ch.MediaKind != db.MediaKindMusic {
		t.Fatalf("external channel packaged fields not cleared: %+v", ch)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id = 'spotify'`, 0)
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'spotify'`, 0)
}

func TestHandleChannelHiddenFromGuideToggle(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/hide-from-guide", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelHideFromGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("hide status=%d body=%s", res.Code, res.Body.String())
	}
	ch, err := db.ChannelByID(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("lookup after hide: %v", err)
	}
	if ch == nil || !ch.HiddenFromGuide {
		t.Fatalf("channel was not hidden: %+v", ch)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/channels/ch/show-in-guide", nil)
	req.SetPathValue("channelID", "ch")
	res = httptest.NewRecorder()

	app.handleChannelShowInGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("show status=%d body=%s", res.Code, res.Body.String())
	}
	ch, err = db.ChannelByID(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("lookup after show: %v", err)
	}
	if ch == nil || ch.HiddenFromGuide {
		t.Fatalf("channel was not shown: %+v", ch)
	}
}

func TestHandleChannelArtworkSetGetReset(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/artwork", bytes.NewBufferString(`{"artworkUrl":"https://example.test/logo.png"}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelArtworkUpdate(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", res.Code, res.Body.String())
	}
	ch, err := db.ChannelByID(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("lookup after set: %v", err)
	}
	if ch == nil || ch.ArtworkURL != "https://example.test/logo.png" {
		t.Fatalf("channel artwork was not stored: %+v", ch)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/channels/ch/artwork", nil)
	req.SetPathValue("channelID", "ch")
	res = httptest.NewRecorder()

	app.handleChannelArtwork(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		ChannelID  string `json:"channelId"`
		ArtworkURL string `json:"artworkUrl"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ChannelID != "ch" || body.ArtworkURL != "https://example.test/logo.png" {
		t.Fatalf("unexpected response: %+v", body)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/channels/ch/artwork", nil)
	req.SetPathValue("channelID", "ch")
	res = httptest.NewRecorder()

	app.handleChannelArtworkReset(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", res.Code, res.Body.String())
	}
	ch, err = db.ChannelByID(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("lookup after reset: %v", err)
	}
	if ch == nil || ch.ArtworkURL != "" {
		t.Fatalf("channel artwork was not cleared: %+v", ch)
	}
}

func TestHandleChannelArtworkRejectsRelativeURL(t *testing.T) {
	app, _ := testAdminApp(t)

	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/artwork", bytes.NewBufferString(`{"artworkUrl":"/logo.png"}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelArtworkUpdate(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want bad request", res.Code, res.Body.String())
	}
}

func TestHandleChannelRestartPlaybackExtendsInsideOuterTransaction(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/restart-playback", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelRestartPlayback(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	// 18s media, 24h horizon → 24*3600*1000/18000 = 4800 looped entries
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch'`, 4800)
}

func TestHandleChannelMediaListsPackageReadiness(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch/media", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelMedia(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		ChannelID string `json:"channelId"`
		Count     int    `json:"count"`
		Media     []struct {
			MediaID       string `json:"mediaId"`
			PackageStatus string `json:"packageStatus"`
			PackageReady  bool   `json:"packageReady"`
		} `json:"media"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ChannelID != "ch" || body.Count != 2 {
		t.Fatalf("unexpected response: %+v", body)
	}
	if body.Media[0].MediaID != "m1" || body.Media[0].PackageStatus != "ready" || !body.Media[0].PackageReady {
		t.Fatalf("first media not ready: %+v", body.Media[0])
	}
	if body.Media[1].MediaID != "m2" || body.Media[1].PackageStatus != "missing" || body.Media[1].PackageReady {
		t.Fatalf("second media readiness mismatch: %+v", body.Media[1])
	}
}
