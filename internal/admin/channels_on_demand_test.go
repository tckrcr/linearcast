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

// Creating an on-demand channel persists prefill_mode and, crucially, does NOT
// eagerly queue packages — that deferral is the entire point of on-demand.
func TestCreateChannelOnDemandDoesNotEagerlyQueue(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)

	res := postCreateChannel(t, app, `{
		"displayName": "OnDem",
		"packageProfile": "h264-1080p-8mbps",
		"prefillMode": "on_demand",
		"mediaIds": ["show1"]
	}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	ch, err := db.ChannelByID(context.Background(), conn, "ondem")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil || ch.PrefillMode != "on_demand" {
		t.Fatalf("channel prefill_mode = %+v, want on_demand", ch)
	}

	var pkgCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM media_packages`).Scan(&pkgCount); err != nil {
		t.Fatalf("count packages: %v", err)
	}
	if pkgCount != 0 {
		t.Fatalf("on-demand create must not eagerly queue packages, got %d", pkgCount)
	}

	var body struct {
		Queued []string `json:"queued"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Queued) != 0 {
		t.Fatalf("queued=%v, want empty for on-demand", body.Queued)
	}
}

func TestCreateChannelRejectsInvalidPrefillMode(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)

	res := postCreateChannel(t, app, `{
		"displayName": "Bad",
		"packageProfile": "h264-1080p-8mbps",
		"prefillMode": "sometimes",
		"mediaIds": ["show1"]
	}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", res.Code)
	}
	if code := errorCode(t, res); code != "invalid_prefill_mode" {
		t.Fatalf("error code=%q, want invalid_prefill_mode", code)
	}
}

func TestHandleChannelOnDemandProfileUpdateChangesProfileOnly(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('od', 'On Demand', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 'on_demand')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "od", "show1", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('entry1', 'od', 0, 'show1', 0, 1800000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	res := putOnDemandProfile(t, app, "od", `{"profile":"hevc-2160p-40mbps-hdr"}`)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body channelPolicyResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.RequiredPackageProfile != "hevc-2160p-40mbps-hdr" || body.ChannelID != "od" {
		t.Fatalf("unexpected response: %+v", body)
	}
	ch, err := db.ChannelByID(context.Background(), conn, "od")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil || ch.RequiredPackageProfile != "hevc-2160p-40mbps-hdr" || ch.PrefillMode != "on_demand" {
		t.Fatalf("unexpected channel row: %+v", ch)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'od'`, 1)
	assertCount(t, conn, `SELECT COUNT(*) FROM media_packages`, 0)
}

func TestHandleChannelOnDemandProfileUpdateRejectsEagerChannel(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, prefill_mode
		)
		VALUES ('eager', 'Eager', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 'eager')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	res := putOnDemandProfile(t, app, "eager", `{"profile":"hevc-2160p-40mbps-hdr"}`)

	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict", res.Code, res.Body.String())
	}
	if code := errorCode(t, res); code != "unsupported_channel_type" {
		t.Fatalf("error code=%q, want unsupported_channel_type", code)
	}
	ch, err := db.ChannelByID(context.Background(), conn, "eager")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil || ch.RequiredPackageProfile != "h264-1080p-8mbps" {
		t.Fatalf("channel profile changed: %+v", ch)
	}
}

func putOnDemandProfile(t *testing.T, app *App, channelID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/channels/"+channelID+"/on-demand-profile", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("channelID", channelID)
	res := httptest.NewRecorder()
	app.handleChannelOnDemandProfileUpdate(res, req)
	return res
}
