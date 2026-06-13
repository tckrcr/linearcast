package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleScheduleBuilderSourceStatusNoSources(t *testing.T) {
	app, _ := testAdminApp(t)

	req := httptest.NewRequest(http.MethodGet, "/api/schedule-builder/source-status", nil)
	res := httptest.NewRecorder()

	app.handleScheduleBuilderSourceStatus(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body scheduleBuilderSourceStatusResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.HasMediaSource {
		t.Fatal("expected hasMediaSource=false with no sources configured")
	}
	if body.PlexConfigured {
		t.Fatal("expected plexConfigured=false")
	}
	if body.JellyfinConfigured {
		t.Fatal("expected jellyfinConfigured=false")
	}
	if body.LocalSourceCount != 0 {
		t.Fatalf("localSourceCount=%d, want 0", body.LocalSourceCount)
	}
}

func TestHandleScheduleBuilderSourceStatusWithPlexToken(t *testing.T) {
	app, conn := testAdminApp(t)

	if err := db.SetPlexToken(context.Background(), conn, "test-token"); err != nil {
		t.Fatalf("set plex token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/schedule-builder/source-status", nil)
	res := httptest.NewRecorder()

	app.handleScheduleBuilderSourceStatus(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body scheduleBuilderSourceStatusResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.HasMediaSource {
		t.Fatal("expected hasMediaSource=true with plex token set")
	}
	if !body.PlexConfigured {
		t.Fatal("expected plexConfigured=true")
	}
}

func TestHandleScheduleBuilderSourceStatusWithJellyfin(t *testing.T) {
	app, conn := testAdminApp(t)

	if err := db.SetJellyfinURL(context.Background(), conn, "http://jf.local:8096"); err != nil {
		t.Fatalf("set jellyfin url: %v", err)
	}
	if err := db.SetJellyfinAPIKey(context.Background(), conn, "apikey"); err != nil {
		t.Fatalf("set jellyfin api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/schedule-builder/source-status", nil)
	res := httptest.NewRecorder()

	app.handleScheduleBuilderSourceStatus(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body scheduleBuilderSourceStatusResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.HasMediaSource {
		t.Fatal("expected hasMediaSource=true with jellyfin configured")
	}
	if !body.JellyfinConfigured {
		t.Fatal("expected jellyfinConfigured=true")
	}
}

func TestHandleScheduleBuilderCreateChannelValid(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)
	insertMedia(t, conn, "show2", 1740000)
	insertReadyPackage(t, conn, "show1", 1800000)
	insertReadyPackage(t, conn, "show2", 1740000)

	body := bytes.NewBufferString(`{
		"displayName": "Test Channel",
		"mediaIds": ["show1", "show2"],
		"packageProfile": "h264-main-1080p"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	app.handleScheduleBuilderCreateChannel(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var resp scheduleBuilderCreateChannelResponse
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ChannelID != "test-channel" {
		t.Fatalf("channelID=%q, want test-channel", resp.ChannelID)
	}
	if !resp.Created {
		t.Fatal("expected created=true")
	}
	if resp.SyncedMedia != 2 {
		t.Fatalf("syncedMedia=%d, want 2", resp.SyncedMedia)
	}
	if resp.PackageProfile != "h264-main-1080p" {
		t.Fatalf("profile=%q, want h264-main-1080p", resp.PackageProfile)
	}
	if len(resp.AlreadyReady) != 2 {
		t.Fatalf("alreadyReady=%d items, want 2", len(resp.AlreadyReady))
	}
	if len(resp.Failed) != 0 {
		t.Fatalf("failed=%d items, want 0", len(resp.Failed))
	}
	if resp.ChannelID == "" || resp.DisplayName == "" {
		t.Fatal("response missing channelID or displayName")
	}
}

func TestHandleScheduleBuilderCreateChannelAdaptiveBitrateWritesLadder(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)
	insertReadyPackage(t, conn, "show1", 1800000)

	body := bytes.NewBufferString(`{
		"displayName": "ABR Channel",
		"mediaIds": ["show1"],
		"packageProfile": "h264-main-1080p",
		"adaptiveBitrate": "cpu"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	app.handleScheduleBuilderCreateChannel(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	ch, err := db.ChannelByID(context.Background(), conn, "abr-channel")
	if err != nil || ch == nil {
		t.Fatalf("lookup channel: ch=%v err=%v", ch, err)
	}
	want := []string{"h264-copy-source", "h264-main-1080p", "h264-main-720p", "h264-main-480p"}
	if got := fmt.Sprint(ch.ABRLadder); got != fmt.Sprint(want) {
		t.Fatalf("ladder=%v, want %v", ch.ABRLadder, want)
	}
}

func TestHandleScheduleBuilderCreateChannelAdaptiveBitrateRejectsMusic(t *testing.T) {
	app, _ := testAdminApp(t)

	body := bytes.NewBufferString(`{
		"displayName": "Music ABR",
		"mediaIds": ["song1"],
		"packageProfile": "music-aac-720p",
		"adaptiveBitrate": "cpu"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	app.handleScheduleBuilderCreateChannel(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
	var errBody map[string]string
	if err := json.NewDecoder(res.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errBody["error"] != "invalid_abr_media_kind" {
		t.Fatalf("error=%q, want invalid_abr_media_kind", errBody["error"])
	}
}

func TestHandleScheduleBuilderCreateChannelEmptyDisplayName(t *testing.T) {
	app, _ := testAdminApp(t)

	body := bytes.NewBufferString(`{
		"displayName": "",
		"mediaIds": ["show1"]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	app.handleScheduleBuilderCreateChannel(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
	var errBody map[string]string
	if err := json.NewDecoder(res.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errBody["error"] != "missing_display_name" {
		t.Fatalf("error=%q, want missing_display_name", errBody["error"])
	}
}

func TestHandleScheduleBuilderCreateChannelBadJSON(t *testing.T) {
	app, _ := testAdminApp(t)

	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	app.handleScheduleBuilderCreateChannel(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
	var errBody map[string]string
	if err := json.NewDecoder(res.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errBody["error"] != "bad_json" {
		t.Fatalf("error=%q, want bad_json", errBody["error"])
	}
}

func TestHandleScheduleBuilderCreateChannelQueuedAndAlreadyReady(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ready", 12000)
	insertMedia(t, conn, "pending", 12000)
	insertReadyPackage(t, conn, "ready", 12000)

	body := bytes.NewBufferString(`{
		"displayName": "Mixed Channel",
		"mediaIds": ["ready", "pending"],
		"packageProfile": "h264-main-1080p"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	app.handleScheduleBuilderCreateChannel(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var resp scheduleBuilderCreateChannelResponse
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SyncedMedia != 2 {
		t.Fatalf("syncedMedia=%d, want 2", resp.SyncedMedia)
	}
	if len(resp.AlreadyReady) != 1 {
		t.Fatalf("alreadyReady=%d items, want 1", len(resp.AlreadyReady))
	}
	if len(resp.Queued)+len(resp.AlreadyPending) < 1 {
		t.Fatal("expected at least one queued or already-pending item for media without package")
	}
}

func TestHandleScheduleBuilderCreatePlexRelayChannel(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "plex1", 12000)
	insertMedia(t, conn, "plex2", 18000)
	if err := db.SetMediaSourceRef(context.Background(), conn, "/tmp/plex1.mkv", "plex://101"); err != nil {
		t.Fatalf("set plex1 source_ref: %v", err)
	}
	if err := db.SetMediaSourceRef(context.Background(), conn, "/tmp/plex2.mkv", "plex://102"); err != nil {
		t.Fatalf("set plex2 source_ref: %v", err)
	}

	body := bytes.NewBufferString(`{
		"displayName": "Plex Relay",
		"playbackMode": "plex_relay",
		"mediaIds": ["plex1", "plex2"],
		"packageProfile": "h264-main-1080p"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	app.handleScheduleBuilderCreateChannel(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var resp scheduleBuilderCreateChannelResponse
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ChannelID != "plex-relay" || resp.SyncedMedia != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.Queued) != 0 || len(resp.AlreadyPending) != 0 || len(resp.AlreadyReady) != 0 || len(resp.Failed) != 0 {
		t.Fatalf("plex relay should not request packages: %+v", resp)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id = 'plex-relay'`, 2)
	var scheduleCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'plex-relay'`).Scan(&scheduleCount); err != nil {
		t.Fatalf("count schedule entries: %v", err)
	}
	if scheduleCount == 0 {
		t.Fatal("expected plex relay schedule entries")
	}
}
