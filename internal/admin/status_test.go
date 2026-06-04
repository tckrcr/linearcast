package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleNowReturnsAdminPlaybackState(t *testing.T) {
	app, conn := testAdminApp(t)
	app.now = func() time.Time { return time.UnixMilli(6000).UTC() }
	insertNowFixture(t, conn)

	req := httptest.NewRequest(http.MethodGet, "/api/now", nil)
	res := httptest.NewRecorder()

	app.handleNow(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		NowMs    int64        `json:"nowMs"`
		Channels []channelNow `json:"channels"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.NowMs != 6000 {
		t.Fatalf("nowMs=%d, want 6000", body.NowMs)
	}
	if len(body.Channels) != 1 {
		t.Fatalf("channels=%+v, want one enabled channel", body.Channels)
	}

	ch := body.Channels[0]
	if ch.ID != "ch" || ch.DisplayName != "Channel" || !ch.Enabled || ch.Ordering != "alphabetical" {
		t.Fatalf("unexpected channel identity: %+v", ch)
	}
	if ch.Status != "playing" {
		t.Fatalf("status=%q, want playing", ch.Status)
	}
	if ch.Current == nil || ch.Current.MediaID != "m1" || ch.Current.StartMs != 0 || ch.Current.EndMs != 18000 {
		t.Fatalf("unexpected current window: %+v", ch.Current)
	}
	if ch.Current.ElapsedMs != 6000 || ch.Current.RemainingMs != 12000 {
		t.Fatalf("unexpected current timing: %+v", ch.Current)
	}
	if ch.Next == nil || ch.Next.MediaID != "m2" || ch.Next.StartMs != 18000 || ch.Next.EndMs != 30000 {
		t.Fatalf("unexpected next window: %+v", ch.Next)
	}
	if ch.ScheduleEndMs == nil || *ch.ScheduleEndMs != 30000 {
		t.Fatalf("scheduleEndMs=%v, want 30000", ch.ScheduleEndMs)
	}
	if ch.ScheduleCoverageMs != 24000 {
		t.Fatalf("scheduleCoverageMs=%d, want 24000", ch.ScheduleCoverageMs)
	}
	if ch.PackageProfile != db.DefaultPackageProfile {
		t.Fatalf("packageProfile=%q, want %q", ch.PackageProfile, db.DefaultPackageProfile)
	}
	if ch.PackageCoverageMs != 30000 {
		t.Fatalf("packageCoverageMs=%d, want 30000", ch.PackageCoverageMs)
	}
}

func TestHandleChannelNowReturnsNotFoundForMissingChannel(t *testing.T) {
	app, _ := testAdminApp(t)
	app.now = func() time.Time { return time.UnixMilli(6000).UTC() }

	req := httptest.NewRequest(http.MethodGet, "/api/channels/missing/now", nil)
	req.SetPathValue("channelID", "missing")
	res := httptest.NewRecorder()

	app.handleChannelNow(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want not found", res.Code, res.Body.String())
	}
}

func TestHandleChannelNowReturnsSingleChannelState(t *testing.T) {
	app, conn := testAdminApp(t)
	app.now = func() time.Time { return time.UnixMilli(6000).UTC() }
	insertNowFixture(t, conn)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch/now", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelNow(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body channelNow
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != "ch" || body.Status != "playing" {
		t.Fatalf("unexpected channel state: %+v", body)
	}
	if body.Current == nil || body.Current.MediaID != "m1" {
		t.Fatalf("unexpected current window: %+v", body.Current)
	}
}

func TestHandleChannelNowReturnsHiddenChannelState(t *testing.T) {
	app, conn := testAdminApp(t)
	app.now = func() time.Time { return time.UnixMilli(6000).UTC() }
	insertNowFixture(t, conn)
	if _, err := conn.Exec(`UPDATE channels SET hidden_from_guide = 1 WHERE id = 'ch'`); err != nil {
		t.Fatalf("hide channel: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch/now", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelNow(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body channelNow
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != "ch" || !body.HiddenFromGuide || body.Status != "playing" {
		t.Fatalf("unexpected hidden channel state: %+v", body)
	}
}

func insertNowFixture(t *testing.T, conn *sql.DB) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-main-1080p'),
		       ('disabled', 'Disabled', '/tmp', 'alphabetical', 0, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	for _, item := range []struct {
		id         string
		durationMs int64
	}{
		{id: "m1", durationMs: 18000},
		{id: "m2", durationMs: 12000},
	} {
		insertMedia(t, conn, item.id, item.durationMs)
		insertReadyPackage(t, conn, item.id, item.durationMs)
		if _, err := db.AddChannelMedia(context.Background(), conn, "ch", item.id, 0); err != nil {
			t.Fatalf("add channel media %s: %v", item.id, err)
		}
	}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, []db.ScheduleEntry{
		{ChannelID: "ch", StartMs: 0, MediaID: "m1", OffsetMs: 0, DurationMs: 18000, CreatedAtMs: 0},
		{ChannelID: "ch", StartMs: 18000, MediaID: "m2", OffsetMs: 0, DurationMs: 12000, CreatedAtMs: 0},
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
}
