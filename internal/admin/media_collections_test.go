package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func postCollectionBulk(t *testing.T, app *App, body string) (int, mediaCollectionBulkResponse, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/media/collections/bulk", strings.NewReader(body))
	res := httptest.NewRecorder()
	app.handleMediaCollectionBulk(res, req)
	var got mediaCollectionBulkResponse
	_ = json.NewDecoder(res.Body).Decode(&got)
	return res.Code, got, res.Body.String()
}

func mediaGroupForID(t *testing.T, conn *sql.DB, mediaID string) string {
	t.Helper()
	m, err := db.MediaByID(context.Background(), conn, mediaID)
	if err != nil {
		t.Fatalf("query media: %v", err)
	}
	if m == nil {
		t.Fatalf("missing media %s", mediaID)
	}
	return m.CollectionName
}

func TestHandleMediaCollectionBulkDryRunSelectedIDs(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)
	insertMedia(t, conn, "m2", 12000)

	code, got, body := postCollectionBulk(t, app, `{
		"action":"set",
		"collection":"Show One",
		"mediaIds":["m1","m2"],
		"dryRun":true
	}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if !got.DryRun || got.Matched != 2 || got.Updated != 0 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if group := mediaGroupForID(t, conn, "m1"); group != "" {
		t.Fatalf("dry run changed group: %q", group)
	}
}

func TestHandleMediaCollectionBulkSetSelectedIDs(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)
	insertMedia(t, conn, "m2", 12000)

	code, got, body := postCollectionBulk(t, app, `{
		"action":"set",
		"collection":"Show One",
		"mediaIds":["m1","m2"]
	}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if got.Matched != 2 || got.Updated != 2 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if group := mediaGroupForID(t, conn, "m1"); group != "Show One" {
		t.Fatalf("group m1=%q", group)
	}
	if group := mediaGroupForID(t, conn, "m2"); group != "Show One" {
		t.Fatalf("group m2=%q", group)
	}
}

func TestHandleMediaCollectionBulkClearFilteredRows(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)
	insertMedia(t, conn, "m2", 12000)
	if _, err := conn.Exec(`UPDATE media SET scheduling_group = 'Old Show' WHERE id IN ('m1', 'm2')`); err != nil {
		t.Fatalf("seed groups: %v", err)
	}

	code, got, body := postCollectionBulk(t, app, `{
		"action":"clear",
		"filter":{"collection":"Old Show"}
	}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if got.Matched != 2 || got.Updated != 2 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if group := mediaGroupForID(t, conn, "m1"); group != "" {
		t.Fatalf("group m1=%q", group)
	}
}

func TestHandleMediaCollectionBulkRenamePreservesMoviePrefix(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "movie-one", 12000)
	if _, err := conn.Exec(`UPDATE media SET scheduling_group = 'movie:Old Movie' WHERE id = 'movie-one'`); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	code, got, body := postCollectionBulk(t, app, `{
		"action":"rename",
		"fromCollection":"Old Movie",
		"collection":"New Movie",
		"filter":{"kind":"movies"}
	}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if got.Matched != 1 || got.Updated != 1 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if group := mediaGroupForID(t, conn, "movie-one"); group != "movie:New Movie" {
		t.Fatalf("group=%q", group)
	}
}

func TestHandleMediaCollectionBulkRequiresScope(t *testing.T) {
	app, _ := testAdminApp(t)
	code, _, body := postCollectionBulk(t, app, `{"action":"clear"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", code, body)
	}
}
