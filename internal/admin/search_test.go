package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleMediaSearchRequiresQ(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
}

func TestHandleMediaSearchReturnsMatchingMedia(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ep-alpha", 12000)
	insertMedia(t, conn, "ep-beta", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Alpha Episode' WHERE id = 'ep-alpha'`); err != nil {
		t.Fatalf("set title: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/media?q=alpha", nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var results []struct {
		MediaID string `json:"mediaId"`
		Title   string `json:"title"`
	}
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 1 || results[0].MediaID != "ep-alpha" {
		t.Fatalf("unexpected results: %+v", results)
	}
	if results[0].Title != "Alpha Episode" {
		t.Fatalf("unexpected title: %q", results[0].Title)
	}
}

func TestHandleMediaSearchTreatsSuspiciousQueryAsLiteral(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ep-alpha", 12000)
	insertMedia(t, conn, "ep-beta", 12000)

	req := httptest.NewRequest(http.MethodGet, "/api/media?q=%27%20OR%201%3D1%20--", nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var results []struct {
		MediaID string `json:"mediaId"`
	}
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("suspicious query returned %d rows: %+v", len(results), results)
	}
}

func TestHandleMediaSearchTreatsSuspiciousChannelIDAsLiteral(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	req := httptest.NewRequest(http.MethodGet, "/api/media?q=tmp&channelId=ch%27%20OR%201%3D1%20--", nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var results []struct {
		MediaID string `json:"mediaId"`
	}
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	foundMember := false
	for _, r := range results {
		if r.MediaID == "m1" {
			foundMember = true
		}
	}
	if !foundMember {
		t.Fatalf("suspicious channelId behaved like real channel filter; results: %+v", results)
	}
}

func TestHandleMediaSearchExcludesChannelMembers(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	req := httptest.NewRequest(http.MethodGet, "/api/media?q=tmp&channelId=ch", nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var results []struct {
		MediaID string `json:"mediaId"`
	}
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range results {
		if r.MediaID == "m1" {
			t.Fatalf("m1 is a member of ch but was included in search results")
		}
	}
	found := false
	for _, r := range results {
		if r.MediaID == "m2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("m2 not found in results: %+v", results)
	}
}

func TestHandleMediaSearchSurfacesCodecCheckPassed(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, codec_check_reason, ingested_at_ms)
		VALUES ('bad', '/tmp/bad.mkv', '/tmp', 12000, 'mkv', 'hevc', 1080, 'aac', 0, 'unsupported codec', 0)`); err != nil {
		t.Fatalf("insert bad media: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/media?q=bad", nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var results []struct {
		MediaID          string `json:"mediaId"`
		CodecCheckPassed bool   `json:"codecCheckPassed"`
	}
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 1 || results[0].MediaID != "bad" {
		t.Fatalf("unexpected results: %+v", results)
	}
	if results[0].CodecCheckPassed {
		t.Fatalf("bad media should have codecCheckPassed=false")
	}
}

func TestHandleMediaSearchReturnsEmptyArrayForNoMatch(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/media?q=zzznomatch", nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var results []struct{}
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty, got %d results", len(results))
	}
}
