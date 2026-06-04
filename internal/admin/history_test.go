package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleChannelHistoryReturnsRowsSince(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	entry := db.ScheduleEntry{
		ID:         "history-entry",
		ChannelID:  "ch",
		StartMs:    24000,
		MediaID:    "m1",
		OffsetMs:   0,
		DurationMs: 18000,
	}
	var anchorID string
	if err := conn.QueryRow(`SELECT id FROM schedule_entries WHERE channel_id = 'ch' ORDER BY start_ms DESC LIMIT 1`).Scan(&anchorID); err != nil {
		t.Fatalf("read schedule tail: %v", err)
	}
	entry.AnchorScheduleEntryID = &anchorID
	if _, err := db.InsertScheduleEntries(context.Background(), conn, []db.ScheduleEntry{entry}); err != nil {
		t.Fatalf("insert schedule entry: %v", err)
	}
	if _, err := db.RecordPlayHistory(context.Background(), conn, entry); err != nil {
		t.Fatalf("record history: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch/history?since=1", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelHistory(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got playHistoryResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ChannelID != "ch" || got.SinceMs != 1 || got.Count != 1 || len(got.Entries) != 1 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Entries[0].ScheduleEntryID != entry.ID || got.Entries[0].MediaID != "m1" ||
		got.Entries[0].StartedAtMs != 24000 || got.Entries[0].EndedAtMs != 42000 {
		t.Fatalf("unexpected entry: %+v", got.Entries[0])
	}
}

func TestHandleChannelHistoryRequiresSince(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch/history", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelHistory(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}
