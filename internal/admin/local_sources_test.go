package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestLocalSourceCreateListUpdateDelete(t *testing.T) {
	app, conn := testAdminApp(t)
	root := t.TempDir()
	app.mediaRoot = root
	movies := filepath.Join(root, "movies")
	shows := filepath.Join(root, "shows")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/local-sources",
		strings.NewReader(`{"name":"Movies","mediaKind":"movies","paths":["`+movies+`"]}`))
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created db.LocalMediaSource
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.Name != "Movies" || created.Paths[0] != movies {
		t.Fatalf("created=%+v", created)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/admin/local-sources", nil)
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Sources []db.LocalMediaSource `json:"sources"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Sources) != 1 || list.Sources[0].ID != created.ID {
		t.Fatalf("list=%+v", list)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/admin/local-sources/"+created.ID,
		strings.NewReader(`{"name":"TV","mediaKind":"tv","paths":["`+shows+`"]}`))
	req.SetPathValue("id", created.ID)
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated db.LocalMediaSource
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.MediaKind != "shows" || updated.Paths[0] != shows {
		t.Fatalf("updated=%+v", updated)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/local-sources/"+created.ID, nil)
	req.SetPathValue("id", created.ID)
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}

	got, err := db.ListLocalMediaSources(context.Background(), conn)
	if err != nil {
		t.Fatalf("list db: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("sources after delete=%+v", got)
	}
}

func TestLocalSourceRejectsPathOutsideMediaRoot(t *testing.T) {
	app, _ := testAdminApp(t)
	app.mediaRoot = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/local-sources",
		strings.NewReader(`{"name":"Bad","mediaKind":"movies","paths":["/tmp/not-under-root"]}`))
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "path must be under the media root") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestLocalSourceScanAcceptedWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	root := t.TempDir()
	app.mediaRoot = root
	source, err := db.UpsertLocalMediaSource(context.Background(), conn, db.LocalMediaSource{
		ID:        "src",
		Name:      "Movies",
		MediaKind: "movies",
		Paths:     []string{root},
	})
	if err != nil {
		t.Fatalf("insert local source: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/local-sources/src/scan", nil)
	req.SetPathValue("id", source.ID)
	app.handleLocalSourceScan(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	result := rec.Result()
	defer result.Body.Close()
	if got := result.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type=%q, want application/json", got)
	}
	if got := result.Header.Get("Cache-Control"); got != "" {
		t.Fatalf("cache-control=%q, want empty wire header", got)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body) != 1 || strings.TrimSpace(body["jobId"]) == "" {
		t.Fatalf("body=%v, want only non-empty jobId", body)
	}
	job, ok := app.ingestJobs.get(body["jobId"])
	if !ok {
		t.Fatalf("job %q missing", body["jobId"])
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		job.mu.Lock()
		status := job.status
		job.mu.Unlock()
		if status != "running" {
			break
		}
		if time.Now().After(deadline) {
			job.cancel()
			t.Fatalf("job %q still running", body["jobId"])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
