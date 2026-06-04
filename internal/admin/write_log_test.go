package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// --- classifyRequest tests ---

func TestClassifyRequest(t *testing.T) {
	tests := []struct {
		method         string
		path           string
		wantAction     string
		wantTargetType string
		wantTargetID   string
	}{
		{"POST", "/api/channels/ch1/restart-playback", "restart_playback", "channel", "ch1"},
		{"POST", "/api/channels/ch1/extend", "extend_schedule", "channel", "ch1"},
		{"POST", "/api/channels/ch1/disable", "disable_channel", "channel", "ch1"},
		{"POST", "/api/channels/ch1/enable", "enable_channel", "channel", "ch1"},
		{"POST", "/api/channels/ch1/hide-from-guide", "hide_channel_from_guide", "channel", "ch1"},
		{"POST", "/api/channels/ch1/show-in-guide", "show_channel_in_guide", "channel", "ch1"},
		{"POST", "/api/channels/ch1/clone", "clone_channel", "channel", "ch1"},
		{"PUT", "/api/channels/ch1/policy", "update_channel_policy", "channel", "ch1"},
		{"DELETE", "/api/channels/ch1", "delete_channel", "channel", "ch1"},
		{"POST", "/api/channels/ch1/media", "add_channel_media", "channel", "ch1"},
		{"DELETE", "/api/channels/ch1/media/m1", "remove_channel_media", "channel", "ch1"},
		{"PUT", "/api/channels/ch1/media/order", "reorder_channel_media", "channel", "ch1"},
		{"DELETE", "/api/channels/ch1/schedule", "clear_schedule", "channel", "ch1"},
		{"DELETE", "/api/channels/ch1/schedule/range", "delete_schedule_range", "channel", "ch1"},
		{"PUT", "/api/channels/ch1/schedule/window/order", "update_schedule_window_order", "channel", "ch1"},
		{"POST", "/api/channels/ch1/schedule/entries", "upsert_schedule_entry", "channel", "ch1"},
		{"POST", "/api/channels/ch1/schedule/entries/1000/after", "insert_schedule_entry_after", "channel", "ch1"},
		{"POST", "/api/channels/ch1/schedule/entries/1000/before", "insert_schedule_entry_before", "channel", "ch1"},
		{"DELETE", "/api/channels/ch1/schedule/entries/1000", "delete_schedule_entry", "channel", "ch1"},
		{"PUT", "/api/package-profiles/h264-main-1080p", "update_package_profile", "profile", "h264-main-1080p"},
		{"DELETE", "/api/package-profiles/h264-main-1080p", "delete_package_profile", "profile", "h264-main-1080p"},
		{"POST", "/api/admin/plex/token", "set_plex_token", "settings", "plex"},
		{"DELETE", "/api/admin/plex/token", "clear_plex_token", "settings", "plex"},
		{"POST", "/api/admin/local-sources", "create_local_source", "local_source", ""},
		{"PUT", "/api/admin/local-sources/local1", "update_local_source", "local_source", "local1"},
		{"DELETE", "/api/admin/local-sources/local1", "delete_local_source", "local_source", "local1"},
		{"POST", "/api/schedule-builder/channels", "create_schedule_builder_channel", "channel", ""},
		{"PUT", "/api/subtitle-settings", "update_subtitle_settings", "settings", "subtitle"},
		// unrecognised path returns empty strings
		{"POST", "/api/unknown/path", "", "", ""},
		// GET on a write path still classifies (middleware skips GETs, but
		// classify itself is method-agnostic where the action is unambiguous)
		{"POST", "/api/channels/ch2/extend", "extend_schedule", "channel", "ch2"},
	}

	for _, tt := range tests {
		action, targetType, targetID := classifyRequest(tt.method, tt.path)
		if action != tt.wantAction || targetType != tt.wantTargetType || targetID != tt.wantTargetID {
			t.Errorf("classifyRequest(%q, %q) = (%q, %q, %q), want (%q, %q, %q)",
				tt.method, tt.path,
				action, targetType, targetID,
				tt.wantAction, tt.wantTargetType, tt.wantTargetID)
		}
	}
}

// --- shouldSkipWriteLog tests ---

func TestShouldSkipWriteLog(t *testing.T) {
	skip := []string{
		"/api/auth/login",
		"/api/auth/logout",
		"/api/media/package",
		"/api/media/package/cancel",
		"/api/cache/invalid-profiles",
		"/api/admin/plex/scan",
		"/api/admin/jellyfin/scan",
		"/api/admin/local-sources/src1/scan",
		"/api/admin/local-sources/any-id/scan",
	}
	for _, path := range skip {
		if !shouldSkipWriteLog(path) {
			t.Errorf("shouldSkipWriteLog(%q) = false, want true", path)
		}
	}

	keep := []string{
		"/api/channels/ch1/policy",
		"/api/channels/ch1/disable",
		"/api/admin/local-sources",
		"/api/admin/local-sources/src1",
		"/api/package-profiles/h264-main-1080p",
		"/api/subtitle-settings",
		"/api/schedule-builder/channels",
	}
	for _, path := range keep {
		if shouldSkipWriteLog(path) {
			t.Errorf("shouldSkipWriteLog(%q) = true, want false", path)
		}
	}
}

// --- captureResponseWriter tests ---

func TestCaptureResponseWriterDefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	crw := &captureResponseWriter{ResponseWriter: rec, status: http.StatusOK}
	_, _ = crw.Write([]byte("hello"))
	if crw.status != 200 {
		t.Fatalf("want 200, got %d", crw.status)
	}
}

func TestCaptureResponseWriterRecordsExplicitStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	crw := &captureResponseWriter{ResponseWriter: rec, status: http.StatusOK}
	crw.WriteHeader(http.StatusConflict)
	if crw.status != http.StatusConflict {
		t.Fatalf("want 409, got %d", crw.status)
	}
}

func TestCaptureResponseWriterDoesNotOverwriteStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	crw := &captureResponseWriter{ResponseWriter: rec, status: http.StatusOK}
	crw.WriteHeader(http.StatusBadRequest)
	crw.WriteHeader(http.StatusInternalServerError)
	if crw.status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", crw.status)
	}
}

// --- middleware tests ---

func TestWriteLogMiddlewareSkipsGET(t *testing.T) {
	app, conn := testAdminApp(t)

	handler := app.writeLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch1/schedule", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rows, err := db.RecentAdminWriteLogs(context.Background(), conn, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no log rows for GET, got %d", len(rows))
	}
}

func TestWriteLogMiddlewareSkipsReadEndpointGet(t *testing.T) {
	// GET /api/admin/write-log must never create a log row.
	app, conn := testAdminApp(t)
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/write-log", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from read endpoint, got %d", rec.Code)
	}
	rows, err := db.RecentAdminWriteLogs(context.Background(), conn, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no log rows after GET /api/admin/write-log, got %d", len(rows))
	}
}

func TestWriteLogMiddlewarePassesThroughResponseOnDBFailure(t *testing.T) {
	// A DB error during logging must not change the response the client sees.
	app, conn := testAdminApp(t)
	conn.Close() // force every DB call to fail

	handler := app.writeLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch1/extend", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 pass-through, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != `{"ok":true}` {
		t.Fatalf("expected body pass-through, got %q", body)
	}
}

func TestWriteLogMiddlewareRecordsPost(t *testing.T) {
	app, conn := testAdminApp(t)
	fixedNow := time.UnixMilli(5000).UTC()
	app.now = func() time.Time { return fixedNow }

	handler := app.writeLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch1/extend", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rows, err := db.RecentAdminWriteLogs(context.Background(), conn, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 log row, got %d", len(rows))
	}
	e := rows[0]
	if e.Method != "POST" {
		t.Errorf("method: got %q, want POST", e.Method)
	}
	if e.Path != "/api/channels/ch1/extend" {
		t.Errorf("path: got %q", e.Path)
	}
	if e.Action == nil || *e.Action != "extend_schedule" {
		t.Errorf("action: got %v", e.Action)
	}
	if e.TargetType == nil || *e.TargetType != "channel" {
		t.Errorf("target_type: got %v", e.TargetType)
	}
	if e.TargetID == nil || *e.TargetID != "ch1" {
		t.Errorf("target_id: got %v", e.TargetID)
	}
	if e.Status != http.StatusOK {
		t.Errorf("status: got %d, want 200", e.Status)
	}
	if e.CreatedAtMs != fixedNow.UnixMilli() {
		t.Errorf("created_at_ms: got %d, want %d", e.CreatedAtMs, fixedNow.UnixMilli())
	}
}

func TestWriteLogMiddlewareRecordsFailedWrite(t *testing.T) {
	app, conn := testAdminApp(t)

	handler := app.writeLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch1/schedule/entries/1000", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rows, err := db.RecentAdminWriteLogs(context.Background(), conn, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 log row, got %d", len(rows))
	}
	if rows[0].Status != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rows[0].Status)
	}
	if rows[0].Action == nil || *rows[0].Action != "delete_schedule_entry" {
		t.Errorf("action: got %v", rows[0].Action)
	}
}

// --- read endpoint tests ---

func TestHandleAdminWriteLogReturnsRowsNewestFirst(t *testing.T) {
	app, conn := testAdminApp(t)

	for _, e := range []db.AdminWriteLog{
		{CreatedAtMs: 1000, Method: "POST", Path: "/api/channels/ch1/extend", Status: 200, DurationMs: 1},
		{CreatedAtMs: 2000, Method: "POST", Path: "/api/channels/ch1/restart-playback", Status: 200, DurationMs: 2},
		{CreatedAtMs: 3000, Method: "DELETE", Path: "/api/channels/ch1/schedule", Status: 500, DurationMs: 3},
	} {
		if err := db.InsertAdminWriteLog(context.Background(), conn, e); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/write-log", nil)
	rec := httptest.NewRecorder()
	app.handleAdminWriteLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var body struct {
		Entries []struct {
			CreatedAtMs int64  `json:"createdAtMs"`
			Method      string `json:"method"`
			Status      int    `json:"status"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(body.Entries))
	}
	// newest first
	if body.Entries[0].CreatedAtMs != 3000 {
		t.Errorf("first entry should be newest (3000), got %d", body.Entries[0].CreatedAtMs)
	}
	if body.Entries[2].CreatedAtMs != 1000 {
		t.Errorf("last entry should be oldest (1000), got %d", body.Entries[2].CreatedAtMs)
	}
}

func TestHandleAdminWriteLogRespectsLimit(t *testing.T) {
	app, conn := testAdminApp(t)

	for i := 0; i < 5; i++ {
		if err := db.InsertAdminWriteLog(context.Background(), conn, db.AdminWriteLog{
			CreatedAtMs: int64(i * 1000),
			Method:      "POST",
			Path:        "/api/channels/ch1/extend",
			Status:      200,
			DurationMs:  1,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/write-log?limit=2", nil)
	rec := httptest.NewRecorder()
	app.handleAdminWriteLog(rec, req)

	var body struct {
		Entries []any `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(body.Entries))
	}
}

func TestHandleAdminWriteLogRejectsInvalidLimit(t *testing.T) {
	app, _ := testAdminApp(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/write-log?limit=bad", nil)
	rec := httptest.NewRecorder()
	app.handleAdminWriteLog(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestHandleAdminWriteLogNullableFieldsOmittedWhenEmpty(t *testing.T) {
	app, conn := testAdminApp(t)

	// Insert a row with no action/target (unrecognised path)
	if err := db.InsertAdminWriteLog(context.Background(), conn, db.AdminWriteLog{
		CreatedAtMs: 1000,
		Method:      "POST",
		Path:        "/api/unknown/path",
		Status:      200,
		DurationMs:  1,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/write-log", nil)
	rec := httptest.NewRecorder()
	app.handleAdminWriteLog(rec, req)

	var body struct {
		Entries []struct {
			Action     *string `json:"action"`
			TargetType *string `json:"targetType"`
			TargetID   *string `json:"targetId"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(body.Entries))
	}
	e := body.Entries[0]
	if e.Action != nil || e.TargetType != nil || e.TargetID != nil {
		t.Errorf("expected nil nullable fields, got action=%v targetType=%v targetId=%v",
			e.Action, e.TargetType, e.TargetID)
	}
}
