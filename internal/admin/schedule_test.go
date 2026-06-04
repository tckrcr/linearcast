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

func TestHandleChannelUpsertScheduleEntryRejectsInsideExistingEntry(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/schedule/entries",
		bytes.NewBufferString(`{"mediaId":"m1","startMs":6000}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelUpsertScheduleEntry(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch'`, 1)
}

func TestHandleChannelUpsertScheduleEntryRequiresReadyChannelPackage(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/schedule/entries",
		bytes.NewBufferString(`{"mediaId":"m2","startMs":18000}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelUpsertScheduleEntry(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict", res.Code, res.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error"] != "package_not_ready" {
		t.Fatalf("error=%q, want package_not_ready", body["error"])
	}
	if body["hint"] == "" {
		t.Fatalf("hint is empty: %+v", body)
	}
}

func TestHandleChannelUpsertScheduleEntryAllowsAttachedFillerAsset(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "bumper", 6000)
	insertReadyPackage(t, conn, "bumper", 6000)
	asset, err := db.UpsertFillerAsset(context.Background(), conn, db.FillerAsset{
		MediaID:     "bumper",
		Label:       "Bumper",
		Kind:        db.FillerKindBumper,
		Enabled:     true,
		CreatedAtMs: 0,
	})
	if err != nil {
		t.Fatalf("upsert filler asset: %v", err)
	}
	if err := db.AttachChannelFillerAsset(context.Background(), conn, "ch", asset.ID, 1, true); err != nil {
		t.Fatalf("attach filler asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/schedule/entries",
		bytes.NewBufferString(`{"mediaId":"bumper","startMs":18000}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelUpsertScheduleEntry(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id = 'ch' AND media_id = 'bumper'`, 0)
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch' AND media_id = 'bumper'`, 1)
}

func TestHandleChannelUpsertScheduleEntryInvalidAlignmentIncludesHint(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/schedule/entries",
		bytes.NewBufferString(`{"mediaId":"m1","startMs":6001}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelUpsertScheduleEntry(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want bad request", res.Code, res.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error"] != "invalid_start_ms" {
		t.Fatalf("error=%q, want invalid_start_ms", body["error"])
	}
	if body["hint"] == "" {
		t.Fatalf("hint is empty: %+v", body)
	}
}

func TestHandleChannelSaveScheduleWindowPreservesOriginalTail(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	body := fmt.Sprintf(`{
		"fromMs": %d,
		"toMs": %d,
		"tailMode": "preserve",
		"entries": [{"mediaId": "m4"}]
	}`, start+12000, start+24000)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/schedule/window/order", bytes.NewBufferString(body))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelSaveScheduleWindowOrdered(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var saved struct {
		TailMode         string `json:"tailMode"`
		ResumeAfterMedia string `json:"resumeAfterMedia"`
	}
	if err := json.NewDecoder(res.Body).Decode(&saved); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if saved.TailMode != "preserve" || saved.ResumeAfterMedia != "m2" {
		t.Fatalf("unexpected response: %+v", saved)
	}
	got, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+48000)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	if len(got) < 4 {
		t.Fatalf("entries=%+v, want at least 4", got)
	}
	if got[0].MediaID != "m1" || got[1].MediaID != "m4" || got[2].MediaID != "m3" {
		t.Fatalf("preserve sequence mismatch: %+v", got[:3])
	}
}

func TestHandleChannelSaveScheduleWindowPreservesOriginalWindowTail(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	body := fmt.Sprintf(`{
		"fromMs": %d,
		"toMs": %d,
		"tailMode": "preserve",
		"entries": [{"mediaId": "m2"}]
	}`, start+12000, start+48000)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/schedule/window/order", bytes.NewBufferString(body))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelSaveScheduleWindowOrdered(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var saved struct {
		ResumeAfterMedia string `json:"resumeAfterMedia"`
	}
	if err := json.NewDecoder(res.Body).Decode(&saved); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if saved.ResumeAfterMedia != "m4" {
		t.Fatalf("resumeAfterMedia=%q, want latest original media m4", saved.ResumeAfterMedia)
	}
	got, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+60000)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	if len(got) < 4 {
		t.Fatalf("entries=%+v, want at least 4", got)
	}
	if got[0].MediaID != "m1" || got[1].MediaID != "m2" || got[2].MediaID != "m1" {
		t.Fatalf("window preserve sequence mismatch: %+v", got[:3])
	}
}

func TestHandleChannelSaveScheduleWindowJumpsAfterInsertedMedia(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	body := fmt.Sprintf(`{
		"fromMs": %d,
		"toMs": %d,
		"tailMode": "jump",
		"entries": [{"mediaId": "m4"}]
	}`, start+12000, start+24000)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/schedule/window/order", bytes.NewBufferString(body))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelSaveScheduleWindowOrdered(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	got, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+48000)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	if len(got) < 4 {
		t.Fatalf("entries=%+v, want at least 4", got)
	}
	if got[0].MediaID != "m1" || got[1].MediaID != "m4" || got[2].MediaID != "m1" {
		t.Fatalf("jump sequence mismatch: %+v", got[:3])
	}
}

func TestHandleChannelSaveScheduleWindowCanSkipTailExtension(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	insertMedia(t, conn, "m5", 12000)
	insertReadyPackage(t, conn, "m5", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m5", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	body := fmt.Sprintf(`{
		"fromMs": %d,
		"toMs": %d,
		"tailMode": "preserve",
		"extendTail": false,
		"entries": [{"mediaId": "m5"}]
	}`, start+12000, start+24000)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/schedule/window/order", bytes.NewBufferString(body))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelSaveScheduleWindowOrdered(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var saved struct {
		ExtendTail bool  `json:"extendTail"`
		Cleared    int64 `json:"cleared"`
		Inserted   int64 `json:"inserted"`
	}
	if err := json.NewDecoder(res.Body).Decode(&saved); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if saved.ExtendTail || saved.Cleared != 1 || saved.Inserted != 1 {
		t.Fatalf("unexpected response: %+v", saved)
	}
	got, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+72000)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("entries=%+v, want replacement plus preserved tail", got)
	}
	if got[0].MediaID != "m1" || got[1].MediaID != "m5" || got[2].MediaID != "m3" || got[3].MediaID != "m4" {
		t.Fatalf("sequence mismatch: %+v", got)
	}
	issues, err := db.ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("validate chains returned issues: %+v", issues)
	}
}

func TestHandleChannelSaveScheduleWindowOrderedUsesDraftOrder(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	insertMedia(t, conn, "m5", 12000)
	insertReadyPackage(t, conn, "m5", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m5", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}

	body := fmt.Sprintf(`{
		"fromMs": %d,
		"toMs": %d,
		"tailMode": "preserve",
		"extendTail": false,
		"entries": [
			{"mediaId": "m3"},
			{"mediaId": "m2"},
			{"mediaId": "m5"}
		]
	}`, start+12000, start+48000)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/schedule/window/order", bytes.NewBufferString(body))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelSaveScheduleWindowOrdered(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	got, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+72000)
	if err != nil {
		t.Fatalf("schedule window: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("entries=%+v, want 4 rows after ordered save without tail extension", got)
	}
	want := []string{"m1", "m3", "m2", "m5"}
	for i, wantID := range want {
		if got[i].MediaID != wantID {
			t.Fatalf("got[%d].MediaID=%s, want %s (entries=%+v)", i, got[i].MediaID, wantID, got)
		}
	}
	if got[1].StartMs != start+12000 || got[2].StartMs != start+24000 || got[3].StartMs != start+36000 {
		t.Fatalf("unexpected shifted start times: %+v", got)
	}
	issues, err := db.ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("validate chains returned issues: %+v", issues)
	}
}

func TestHandleChannelInsertScheduleEntryAfterShiftsTail(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	insertMedia(t, conn, "m5", 12000)
	insertReadyPackage(t, conn, "m5", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m5", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	before, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+48000)
	if err != nil {
		t.Fatalf("schedule window before: %v", err)
	}
	target := before[1]

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/channels/ch/schedule/entries/%s/after", target.ID),
		bytes.NewBufferString(`{"mediaId":"m5"}`))
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("entryId", target.ID)
	res := httptest.NewRecorder()

	app.handleChannelInsertScheduleEntryAfter(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	after, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+72000)
	if err != nil {
		t.Fatalf("schedule window after: %v", err)
	}
	want := []string{"m1", "m2", "m5", "m3", "m4"}
	for i, wantID := range want {
		if after[i].MediaID != wantID {
			t.Fatalf("after[%d].MediaID=%s, want %s (entries=%+v)", i, after[i].MediaID, wantID, after[:5])
		}
	}
	if after[2].StartMs != start+24000 || after[3].StartMs != start+36000 || after[4].StartMs != start+48000 {
		t.Fatalf("unexpected shifted start times: %+v", after[:5])
	}
}

func TestHandleChannelInsertScheduleEntryBeforeShiftsTail(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	insertMedia(t, conn, "m5", 12000)
	insertReadyPackage(t, conn, "m5", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m5", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	before, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+48000)
	if err != nil {
		t.Fatalf("schedule window before: %v", err)
	}
	target := before[0]

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/channels/ch/schedule/entries/%s/before", target.ID),
		bytes.NewBufferString(`{"mediaId":"m5"}`))
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("entryId", target.ID)
	res := httptest.NewRecorder()

	app.handleChannelInsertScheduleEntryBefore(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	after, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+72000)
	if err != nil {
		t.Fatalf("schedule window after: %v", err)
	}
	want := []string{"m5", "m1", "m2", "m3", "m4"}
	for i, wantID := range want {
		if after[i].MediaID != wantID {
			t.Fatalf("after[%d].MediaID=%s, want %s (entries=%+v)", i, after[i].MediaID, wantID, after[:5])
		}
	}
	if after[0].StartMs != start || after[1].StartMs != start+12000 || after[2].StartMs != start+24000 {
		t.Fatalf("unexpected shifted start times: %+v", after[:5])
	}
}

func TestHandleChannelDeleteScheduleEntryCanSkipRebuild(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	before, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+48000)
	if err != nil {
		t.Fatalf("schedule window before: %v", err)
	}
	target := before[1]

	req := httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/channels/ch/schedule/entries/%s?rebuild=false", target.ID), nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("entryId", target.ID)
	res := httptest.NewRecorder()

	app.handleChannelDeleteScheduleEntry(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Inserted int64 `json:"inserted"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Inserted != 0 {
		t.Fatalf("inserted=%d, want 0 without rebuild", body.Inserted)
	}
	after, err := db.ScheduleWindow(context.Background(), conn, "ch", start, start+48000)
	if err != nil {
		t.Fatalf("schedule window after: %v", err)
	}
	if len(after) != len(before)-1 {
		t.Fatalf("entries=%+v, want one row removed", after)
	}
	for _, entry := range after {
		if entry.ID == target.ID {
			t.Fatalf("deleted entry still present: %+v", after)
		}
	}
}

func TestHandleChannelDeleteScheduleRangeIntersectsAndRebuildsFromEarliestDeleted(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)

	req := httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/channels/ch/schedule/range?from=%d&to=%d", start+18000, start+30000), nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelDeleteScheduleRange(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Deleted        int64  `json:"deleted"`
		RebuildStartMs int64  `json:"rebuildStartMs"`
		ResumeAfter    string `json:"resumeAfterMedia"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Deleted != 2 || body.RebuildStartMs != start+12000 || body.ResumeAfter != "m3" {
		t.Fatalf("unexpected response: %+v", body)
	}
	assertCount(t, conn,
		fmt.Sprintf(`SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch' AND start_ms >= %d AND start_ms < %d AND media_id IN ('m2', 'm3')`, start+12000, start+36000), 0)
}

func TestHandleChannelDeleteScheduleRangeIsIdempotent(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch/schedule/range?from=60000&to=120000", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelDeleteScheduleRange(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Deleted  int64 `json:"deleted"`
		Inserted int   `json:"inserted"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Deleted != 0 || body.Inserted != 0 {
		t.Fatalf("unexpected response: %+v", body)
	}
}

func TestHandleChannelSchedulePreviewReturnsDiffWithoutWriting(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	if _, err := conn.Exec(`DELETE FROM schedule_entries WHERE channel_id = 'ch' AND start_ms = ?`, start); err != nil {
		t.Fatalf("delete first schedule entry: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('preview-existing', 'ch', ?, 'm4', 0, 12000, 0)`, start); err != nil {
		t.Fatalf("insert alternate schedule entry: %v", err)
	}
	if err := db.RepairScheduleEntryAnchorsForChannel(conn, "ch"); err != nil {
		t.Fatalf("repair schedule anchors: %v", err)
	}
	before, err := db.CountScheduleEntries(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("count before: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/channels/ch/schedule/preview?from=%d&hours=1", start), nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelSchedulePreview(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		ChannelID          string `json:"channelId"`
		Profile            string `json:"profile"`
		Count              int    `json:"count"`
		EligibleMedia      int    `json:"eligibleMedia"`
		EligibleReadyMedia int    `json:"eligibleReadyMedia"`
		Diff               struct {
			Unchanged int `json:"unchanged"`
			Added     []struct {
				MediaID string `json:"mediaId"`
				StartMs int64  `json:"startMs"`
			} `json:"added"`
			Removed []struct {
				MediaID string `json:"mediaId"`
				StartMs int64  `json:"startMs"`
			} `json:"removed"`
		} `json:"diff"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ChannelID != "ch" || body.Profile != db.DefaultPackageProfile {
		t.Fatalf("unexpected body metadata: %+v", body)
	}
	if body.Count == 0 || body.EligibleMedia != 4 || body.EligibleReadyMedia != 4 {
		t.Fatalf("unexpected preview counts: %+v", body)
	}
	if len(body.Diff.Added) == 0 || len(body.Diff.Removed) == 0 {
		t.Fatalf("diff=%+v, want added and removed entries", body.Diff)
	}
	after, err := db.CountScheduleEntries(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Fatalf("schedule row count changed from %d to %d", before, after)
	}
}
