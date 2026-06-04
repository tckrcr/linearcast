package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestHandleChannelDeleteRemovesChannelButKeepsPackages(t *testing.T) {
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
