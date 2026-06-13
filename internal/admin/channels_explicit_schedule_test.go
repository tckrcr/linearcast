package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

type schedRow struct {
	StartMs    int64
	OffsetMs   int64
	DurationMs int64
	MediaID    string
}

func readScheduleRows(t *testing.T, conn *sql.DB, channelID string) []schedRow {
	t.Helper()
	rows, err := conn.Query(`SELECT start_ms, offset_ms, duration_ms, media_id
		FROM schedule_entries WHERE channel_id = ? ORDER BY start_ms`, channelID)
	if err != nil {
		t.Fatalf("query schedule rows: %v", err)
	}
	defer rows.Close()
	var out []schedRow
	for rows.Next() {
		var r schedRow
		if err := rows.Scan(&r.StartMs, &r.OffsetMs, &r.DurationMs, &r.MediaID); err != nil {
			t.Fatalf("scan schedule row: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// assertNoGaps checks both that the persisted rows are contiguous and that
// ScheduleGaps reports no hole across the covered window.
func assertNoGaps(t *testing.T, conn *sql.DB, channelID string, rows []schedRow) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		prevEnd := rows[i-1].StartMs + rows[i-1].DurationMs
		if rows[i].StartMs != prevEnd {
			t.Fatalf("row %d start=%d, want %d (contiguous)", i, rows[i].StartMs, prevEnd)
		}
	}
	first := rows[0].StartMs
	last := rows[len(rows)-1].StartMs + rows[len(rows)-1].DurationMs
	gaps, err := db.ScheduleGaps(context.Background(), conn, channelID, first, last)
	if err != nil {
		t.Fatalf("schedule gaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("expected no gaps, got %d", len(gaps))
	}
}

func insertFillerAsset(t *testing.T, conn *sql.DB, assetID, mediaID string) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
		VALUES (?, ?, ?, 'bumper', 1, 0)`, assetID, mediaID, mediaID); err != nil {
		t.Fatalf("insert filler asset %s: %v", assetID, err)
	}
}

func postCreateChannel(t *testing.T, app *App, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/schedule-builder/channels", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	app.handleScheduleBuilderCreateChannel(res, req)
	return res
}

func errorCode(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	var errBody map[string]string
	if err := json.NewDecoder(res.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return errBody["error"]
}

// Two 30-minute primaries tile the slot grid exactly: both land on slot
// boundaries with no filler required, and the schedule persists gap-free.
func TestCreateChannelExplicitSlotGridContiguous(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)
	insertMedia(t, conn, "show2", 1800000)

	res := postCreateChannel(t, app, `{
		"displayName": "Grid A",
		"packageProfile": "h264-main-1080p",
		"scheduleMode": "slot_grid",
		"slotDurationMs": 1800000,
		"mediaIds": ["show1", "show2"],
		"entries": [
			{"mediaId": "show1", "offsetMs": 0, "durationMs": 1800000},
			{"mediaId": "show2", "offsetMs": 0, "durationMs": 1800000}
		]
	}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	rows := readScheduleRows(t, conn, "grid-a")
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2", len(rows))
	}
	// Starts are wall-clock epoch ms: both primaries land on a 30-minute slot
	// boundary and the schedule is contiguous.
	if rows[0].StartMs%1800000 != 0 {
		t.Fatalf("row0 start=%d not slot-aligned", rows[0].StartMs)
	}
	if rows[1].StartMs != rows[0].StartMs+1800000 {
		t.Fatalf("row1 start=%d, want %d (contiguous on slot boundary)", rows[1].StartMs, rows[0].StartMs+1800000)
	}
	assertNoGaps(t, conn, "grid-a", rows)
}

// An 18-minute primary leaves a 12-minute gap that filler fills, so the next
// primary lands back on the 30-minute boundary. The filler asset is attached to
// the channel and the schedule is gap-free.
func TestCreateChannelExplicitSlotGridFillerAttached(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1080000) // 18m
	insertMedia(t, conn, "show2", 1800000) // 30m
	insertMedia(t, conn, "fill1", 1800000)
	insertFillerAsset(t, conn, "fa-1", "fill1")

	res := postCreateChannel(t, app, `{
		"displayName": "Grid B",
		"packageProfile": "h264-main-1080p",
		"scheduleMode": "slot_grid",
		"slotDurationMs": 1800000,
		"mediaIds": ["show1", "show2"],
		"fillerMediaIds": ["fill1"],
		"entries": [
			{"mediaId": "show1", "offsetMs": 0, "durationMs": 1080000},
			{"mediaId": "fill1", "offsetMs": 0, "durationMs": 720000},
			{"mediaId": "show2", "offsetMs": 0, "durationMs": 1800000}
		]
	}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	rows := readScheduleRows(t, conn, "grid-b")
	if len(rows) != 3 {
		t.Fatalf("rows=%d, want 3", len(rows))
	}
	base := rows[0].StartMs
	if base%1800000 != 0 {
		t.Fatalf("row0 start=%d not slot-aligned", base)
	}
	if rows[1].MediaID != "fill1" || rows[1].StartMs != base+1080000 {
		t.Fatalf("filler row=%+v, want fill1 at %d", rows[1], base+1080000)
	}
	if rows[2].StartMs != base+1800000 {
		t.Fatalf("show2 start=%d, want %d (slot boundary)", rows[2].StartMs, base+1800000)
	}
	exists, err := db.ChannelFillerAssetMediaExists(context.Background(), conn, "grid-b", "fill1")
	if err != nil {
		t.Fatalf("channel filler exists: %v", err)
	}
	if !exists {
		t.Fatal("expected fill1 attached to channel grid-b")
	}
	assertNoGaps(t, conn, "grid-b", rows)
}

// An 18-minute primary followed by another primary with no filler leaves the
// second primary off the slot boundary — the "never save a channel with gaps"
// invariant must reject it, and no schedule rows are persisted.
func TestCreateChannelExplicitSlotGridRejectsGap(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1080000)
	insertMedia(t, conn, "show2", 1800000)

	res := postCreateChannel(t, app, `{
		"displayName": "Grid C",
		"packageProfile": "h264-main-1080p",
		"scheduleMode": "slot_grid",
		"slotDurationMs": 1800000,
		"mediaIds": ["show1", "show2"],
		"entries": [
			{"mediaId": "show1", "offsetMs": 0, "durationMs": 1080000},
			{"mediaId": "show2", "offsetMs": 0, "durationMs": 1800000}
		]
	}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
	if code := errorCode(t, res); code != "schedule_has_gaps" {
		t.Fatalf("error=%q, want schedule_has_gaps", code)
	}
	// Validation runs before the channel is created, so nothing is persisted.
	if rows := readScheduleRows(t, conn, "grid-c"); len(rows) != 0 {
		t.Fatalf("expected no persisted rows on rejection, got %d", len(rows))
	}
	if ch, err := db.ChannelByID(context.Background(), conn, "grid-c"); err != nil {
		t.Fatalf("channel lookup: %v", err)
	} else if ch != nil {
		t.Fatal("expected no channel row created on rejected schedule")
	}
}

// A duration that is not aligned to the 6s segment grid is rejected.
func TestCreateChannelExplicitRejectsMisalignedDuration(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show1", 1800000)

	res := postCreateChannel(t, app, `{
		"displayName": "Grid D",
		"packageProfile": "h264-main-1080p",
		"scheduleMode": "slot_grid",
		"slotDurationMs": 1800000,
		"mediaIds": ["show1"],
		"entries": [
			{"mediaId": "show1", "offsetMs": 0, "durationMs": 1000}
		]
	}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
	if code := errorCode(t, res); code != "invalid_entry" {
		t.Fatalf("error=%q, want invalid_entry", code)
	}
}
