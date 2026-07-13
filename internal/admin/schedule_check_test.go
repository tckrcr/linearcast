package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/schedcheck"
)

// scheduleCheckFixture inserts a channel + one properly-aligned schedule entry
// with a ready package. fromMs is the entry start; durationMs must be a
// multiple of 6000.
func scheduleCheckFixture(t *testing.T, app *App, channelID, mediaID string, fromMs, durationMs int64, enabled bool) {
	t.Helper()
	conn := app.dbConn
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	mustExecSchedule := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	mustExecSchedule(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled,
		created_at_ms, playback_mode, required_package_profile, hidden_from_guide)
		VALUES (?, ?, '/tmp', 'alphabetical', ?, 0, 'packaged', 'h264-1080p-8mbps', 0)`,
		channelID, channelID, enabledInt)
	mustExecSchedule(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES (?, ?, '/tmp', ?, 'mkv', 'h264', 1080, 'aac', 1, 0)`,
		mediaID, "/tmp/"+mediaID+".mkv", durationMs)
	mustExecSchedule(`INSERT INTO media_packages (id, media_id, rendition_profile, status,
		packaged_duration_ms, created_at_ms, updated_at_ms)
		VALUES (?, ?, 'h264-1080p-8mbps', 'ready', ?, 0, 0)`,
		"pkg-"+mediaID, mediaID, durationMs)
	mustExecSchedule(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (?, ?, ?, ?, 0, ?, 0)`,
		"sched-"+channelID, channelID, fromMs, mediaID, durationMs)
}

func doScheduleCheck(t *testing.T, app *App, query string) (int, scheduleCheckResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/maintenance/schedule-check"+query, nil)
	res := httptest.NewRecorder()
	app.handleMaintenanceScheduleCheck(res, req)
	var resp scheduleCheckResponse
	if res.Code == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return res.Code, resp
}

func TestScheduleCheckClean(t *testing.T) {
	app, _ := testAdminApp(t)
	// from=0 so the entry at 0 is within the window; duration 6000ms = 1 segment.
	scheduleCheckFixture(t, app, "ch", "m1", 0, 6000, true)

	code, resp := doScheduleCheck(t, app, "?from=1970-01-01T00:00:00Z&hours=1")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if len(resp.Issues) != 0 {
		t.Fatalf("expected no issues, got %+v", resp.Issues)
	}
	if resp.ChannelsChecked != 1 {
		t.Fatalf("expected channelsChecked=1, got %d", resp.ChannelsChecked)
	}
}

func TestScheduleCheckGapDetected(t *testing.T) {
	app, _ := testAdminApp(t)
	// Entry starts at 60000ms — leaves a 60s leading gap from window start (0).
	scheduleCheckFixture(t, app, "ch", "m1", 60000, 6000, true)

	code, resp := doScheduleCheck(t, app, "?from=1970-01-01T00:00:00Z&hours=1&gap-ms=0")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	found := false
	for _, iss := range resp.Issues {
		if iss.Kind == schedcheck.KindGap {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected gap issue, got %+v", resp.Issues)
	}
}

func TestScheduleCheckNoScheduleOnEnabledChannel(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering,
		enabled, created_at_ms, playback_mode, required_package_profile, hidden_from_guide)
		VALUES ('empty-ch', 'Empty', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	code, resp := doScheduleCheck(t, app, "?from=1970-01-01T00:00:00Z&hours=1")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	found := false
	for _, iss := range resp.Issues {
		if iss.Kind == schedcheck.KindNoSchedule && iss.ChannelID == "empty-ch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected no_schedule issue, got %+v", resp.Issues)
	}
}

// scheduleCheckNoPackageFixture inserts a channel (with the given playback and
// prefill modes) plus a media row and an aligned schedule entry, but no ready
// package. It is used to verify that the package-not-ready check is gated on the
// channel actually requiring ready packages.
func scheduleCheckNoPackageFixture(t *testing.T, app *App, channelID, playbackMode, prefillMode, requiredProfile string) {
	t.Helper()
	conn := app.dbConn
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	mustExec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled,
		created_at_ms, playback_mode, required_package_profile, hidden_from_guide, prefill_mode)
		VALUES (?, ?, '/tmp', 'alphabetical', 1, 0, ?, ?, 0, ?)`,
		channelID, channelID, playbackMode, requiredProfile, prefillMode)
	mediaID := channelID + "-m1"
	mustExec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES (?, ?, '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0)`,
		mediaID, "/tmp/"+mediaID+".mkv")
	mustExec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES (?, ?, 0, ?, 0, 6000, 0)`,
		"sched-"+channelID, channelID, mediaID)
}

func hasPackageNotReady(issues []schedcheck.Issue, channelID string) bool {
	for _, iss := range issues {
		if iss.Kind == schedcheck.KindPackageNotReady && iss.ChannelID == channelID {
			return true
		}
	}
	return false
}

// On-demand channels schedule without ready linearcast packages, so the audit
// must not flag their entries as package_not_ready — while an eager packaged
// channel in the same window still gets flagged.
func TestScheduleCheckPackageNotReadyGatedByChannelMode(t *testing.T) {
	app, _ := testAdminApp(t)
	scheduleCheckNoPackageFixture(t, app, "ondemand-ch", "packaged", "on_demand", "h264-1080p-8mbps")
	scheduleCheckNoPackageFixture(t, app, "eager-ch", "packaged", "eager", "h264-1080p-8mbps")

	code, resp := doScheduleCheck(t, app, "?from=1970-01-01T00:00:00Z&hours=1")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if hasPackageNotReady(resp.Issues, "ondemand-ch") {
		t.Errorf("on-demand channel should not report package_not_ready: %+v", resp.Issues)
	}
	if !hasPackageNotReady(resp.Issues, "eager-ch") {
		t.Errorf("eager packaged channel should report package_not_ready: %+v", resp.Issues)
	}
}

func TestScheduleCheckChannelNotFound(t *testing.T) {
	app, _ := testAdminApp(t)
	code, _ := doScheduleCheck(t, app, "?channel=does-not-exist")
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestScheduleCheckBadParams(t *testing.T) {
	app, _ := testAdminApp(t)
	for _, q := range []string{"?hours=0", "?hours=-1", "?gap-ms=-1", "?from=not-a-date"} {
		code, _ := doScheduleCheck(t, app, q)
		if code != http.StatusBadRequest {
			t.Fatalf("query %q: expected 400, got %d", q, code)
		}
	}
}

func TestScheduleCheckDefaultWindowIsAligned(t *testing.T) {
	// Default from = now aligned to 6s grid; verify the response window bounds
	// are multiples of 6000.
	app, _ := testAdminApp(t)
	app.now = func() time.Time { return time.UnixMilli(7001).UTC() } // not aligned

	code, resp := doScheduleCheck(t, app, "")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if resp.WindowFromMs%6000 != 0 {
		t.Fatalf("windowFromMs=%d is not grid-aligned", resp.WindowFromMs)
	}
}
