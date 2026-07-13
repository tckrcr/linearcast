package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func patchMedia(t *testing.T, app *App, mediaID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/media/"+mediaID, strings.NewReader(body))
	req.SetPathValue("mediaID", mediaID)
	res := httptest.NewRecorder()
	app.handleMediaUpdate(res, req)
	return res
}

func TestHandleMediaUpdateTitle(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)

	res := patchMedia(t, app, "m1", `{"title":"New Title"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaUpdateResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.MediaID != "m1" || got.Title != "New Title" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleMediaUpdateSchedulingGroup(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)

	res := patchMedia(t, app, "m1", `{"collectionName":"Season 2"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaUpdateResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CollectionName != "Season 2" {
		t.Fatalf("unexpected collectionName: %q", got.CollectionName)
	}
}

func TestHandleMediaUpdateBothFields(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)

	res := patchMedia(t, app, "m1", `{"title":"Ep 1","collectionName":"Show One"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaUpdateResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "Ep 1" || got.CollectionName != "Show One" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleMediaUpdateOrderingFields(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)

	res := patchMedia(t, app, "m1", `{"seasonNumber":2,"episodeNumber":11}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaUpdateResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SeasonNumber == nil || *got.SeasonNumber != 2 || got.EpisodeNumber == nil || *got.EpisodeNumber != 11 {
		t.Fatalf("unexpected ordering response: %+v", got)
	}
}

func TestHandleMediaUpdateClearsOrderingFields(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)
	if _, err := conn.Exec(`UPDATE media SET season_number = 2, episode_number = 11 WHERE id = 'm1'`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := patchMedia(t, app, "m1", `{"seasonNumber":null,"episodeNumber":null}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaUpdateResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SeasonNumber != nil || got.EpisodeNumber != nil {
		t.Fatalf("expected cleared ordering response: %+v", got)
	}
}

func TestHandleMediaUpdateRejectsInvalidOrdering(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)

	res := patchMedia(t, app, "m1", `{"seasonNumber":0}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHandleMediaUpdateClearTitle(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Old Title' WHERE id = 'm1'`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := patchMedia(t, app, "m1", `{"title":""}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaUpdateResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "" {
		t.Fatalf("expected empty title after clear, got %q", got.Title)
	}
}

func TestHandleMediaUpdateNotFound(t *testing.T) {
	app, _ := testAdminApp(t)
	res := patchMedia(t, app, "does-not-exist", `{"title":"x"}`)
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHandleMediaUpdateNoFields(t *testing.T) {
	app, _ := testAdminApp(t)
	res := patchMedia(t, app, "m1", `{}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHandleMediaUpdatePartialLeaveOther(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "m1", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Keep Me', scheduling_group = 'Keep Group' WHERE id = 'm1'`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Update only title; collectionName should be untouched.
	res := patchMedia(t, app, "m1", `{"title":"Changed"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaUpdateResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "Changed" || got.CollectionName != "Keep Group" {
		t.Fatalf("expected Changed/Keep Group, got %+v", got)
	}
}
