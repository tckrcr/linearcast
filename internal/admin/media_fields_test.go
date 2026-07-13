package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These characterize the JSON wire shape of the nullable fields the Media and
// ScheduleEntryEnriched db projections expose (Title, SchedulingGroup), across
// the A1 de-leak that flips them from sql.Null* to native string. The handler
// signatures and response structs are unchanged, so the bytes must not move.

func mediaSearchBody(t *testing.T, app *App, q string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/media?q="+q, nil)
	res := httptest.NewRecorder()
	app.handleMediaSearch(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	return res.Body.String()
}

func TestHandleMediaSearchNullTitleGroupWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ep-alpha", 12000) // NULL title + scheduling_group

	want := `[{"mediaId":"ep-alpha","title":"","path":"/tmp/ep-alpha.mkv","collectionName":"","durationMs":12000,"videoHeight":1080,"videoCodec":"h264","codecCheckPassed":true}]` + "\n"
	if got := mediaSearchBody(t, app, "ep-alpha"); got != want {
		t.Fatalf("media search body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleMediaSearchSetTitleGroupWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ep-alpha", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Alpha Episode', scheduling_group = 'Season 1' WHERE id = 'ep-alpha'`); err != nil {
		t.Fatalf("set fields: %v", err)
	}

	want := `[{"mediaId":"ep-alpha","title":"Alpha Episode","path":"/tmp/ep-alpha.mkv","collectionName":"Season 1","durationMs":12000,"videoHeight":1080,"videoCodec":"h264","codecCheckPassed":true}]` + "\n"
	if got := mediaSearchBody(t, app, "ep-alpha"); got != want {
		t.Fatalf("media search body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// scheduleEntryItem uses omitempty on title/collectionName, so a NULL media
// title is omitted and a set one is present. The entry id is randomized by the
// fixture, so assert the affected fields rather than the whole body.
func TestHandleChannelScheduleMediaTitleOmitemptyWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true) // channel "ch", media "m1" (NULL title), one entry at start 0

	decodeEntry := func() scheduleEntryItem {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/channels/ch/schedule?from=0&hours=1", nil)
		req.SetPathValue("channelID", "ch")
		res := httptest.NewRecorder()
		app.handleChannelSchedule(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
		}
		var resp channelScheduleResponse
		if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Entries) != 1 {
			t.Fatalf("want 1 entry, got %d: %+v", len(resp.Entries), resp.Entries)
		}
		return resp.Entries[0]
	}

	if e := decodeEntry(); e.Title != "" || e.CollectionName != "" {
		t.Fatalf("NULL media title/collection should be empty: %+v", e)
	}

	if _, err := conn.Exec(`UPDATE media SET title = 'M1 Title', scheduling_group = 'Group A' WHERE id = 'm1'`); err != nil {
		t.Fatalf("set fields: %v", err)
	}
	if e := decodeEntry(); e.Title != "M1 Title" || e.CollectionName != "Group A" {
		t.Fatalf("set media title/collection not surfaced: %+v", e)
	}
}
