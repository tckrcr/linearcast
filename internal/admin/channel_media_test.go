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

func TestHandleChannelAddMediaRejectsUnknownChannel(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/missing/media",
		bytes.NewBufferString(`{"mediaId":"m1"}`))
	req.SetPathValue("channelID", "missing")
	res := httptest.NewRecorder()
	app.handleChannelAddMedia(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func TestHandleChannelAddMediaRejectsUnknownMedia(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/media",
		bytes.NewBufferString(`{"mediaId":"nope"}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelAddMedia(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func TestHandleChannelAddMediaRejectsCodecCheckFailed(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, codec_check_reason, ingested_at_ms)
		VALUES ('bad', '/tmp/bad.mkv', '/tmp', 12000, 'mkv', 'hevc', 1080, 'aac', 0, 'unsupported codec', 0)`); err != nil {
		t.Fatalf("insert bad media: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/media",
		bytes.NewBufferString(`{"mediaId":"bad"}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelAddMedia(res, req)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422", res.Code, res.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "codec_check_failed" {
		t.Fatalf("error=%q, want codec_check_failed", body["error"])
	}
}

func TestHandleChannelAddMediaRejectsDuplicate(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/media",
		bytes.NewBufferString(`{"mediaId":"m1"}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelAddMedia(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id='ch'`, 1)
}

func TestHandleChannelAddMediaAppendsToTail(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/media",
		bytes.NewBufferString(`{"mediaId":"m2"}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelAddMedia(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	gotBody := res.Body.String()
	var body struct {
		Added bool `json:"added"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Added {
		t.Fatalf("unexpected response: %+v", body)
	}
	result := res.Result()
	defer result.Body.Close()
	if got := result.Header.Get("Content-Type"); got != "" {
		t.Fatalf("content-type=%q, want empty wire header", got)
	}
	if got := result.Header.Get("Cache-Control"); got != "" {
		t.Fatalf("cache-control=%q, want empty wire header", got)
	}
	wantBody := `{"added":true,"channelID":"ch","mediaId":"m2","note":"future schedule extension will include this media; run extend to rebuild immediately"}` + "\n"
	if got := gotBody; got != wantBody {
		t.Fatalf("body mismatch:\n got: %s\nwant: %s", got, wantBody)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id='ch'`, 2)
	ordered, err := db.ChannelMediaOrdered(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("ordered: %v", err)
	}
	if want := []string{"m1", "m2"}; !equalStrings(ordered, want) {
		t.Fatalf("chain after add: got %v want %v", ordered, want)
	}
}

func TestHandleChannelRemoveMediaRejectsUnknownChannel(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/channels/missing/media/m1", nil)
	req.SetPathValue("channelID", "missing")
	req.SetPathValue("mediaID", "m1")
	res := httptest.NewRecorder()
	app.handleChannelRemoveMedia(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func TestHandleChannelRemoveMediaRejectsNonMember(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch/media/m9", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m9")
	res := httptest.NewRecorder()
	app.handleChannelRemoveMedia(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func TestHandleChannelRemoveMediaNoFutureEntriesJustRemovesMembership(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	// The fixture inserts a past schedule entry (start_ms=0), which is before now.
	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch/media/m1", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m1")
	res := httptest.NewRecorder()
	app.handleChannelRemoveMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id='ch'`, 0)
	// Past schedule entry should still be there.
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id='ch'`, 1)
}

func TestHandleChannelRemoveMediaPrunesFutureEntriesAndRebuilds(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch/media/m2", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m2")
	res := httptest.NewRecorder()
	app.handleChannelRemoveMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Removed        bool  `json:"removed"`
		PrunedSchedule int64 `json:"prunedSchedule"`
		Inserted       int64 `json:"inserted"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Removed || body.PrunedSchedule == 0 {
		t.Fatalf("unexpected response: %+v", body)
	}
	assertCount(t, conn,
		fmt.Sprintf(`SELECT COUNT(*) FROM schedule_entries WHERE channel_id='ch' AND media_id='m2' AND start_ms >= %d`, start),
		0)
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id='ch' AND media_id='m2'`, 0)
}

func TestHandleChannelRemoveMediaNoPruneSkipsSchedule(t *testing.T) {
	app, conn := testAdminApp(t)
	start := insertFutureRangeFixture(t, conn)
	_ = start

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/ch/media/m2?pruneSchedule=false", nil)
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m2")
	res := httptest.NewRecorder()
	app.handleChannelRemoveMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM channel_media WHERE channel_id='ch' AND media_id='m2'`, 0)
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id='ch'`, 4)
}

func TestHandleChannelReorderMediaRejectsUnknownChannel(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/missing/media/order",
		bytes.NewBufferString(`{"order":["m1"]}`))
	req.SetPathValue("channelID", "missing")
	res := httptest.NewRecorder()
	app.handleChannelReorderMedia(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func TestHandleChannelReorderMediaRejectsMismatchedCount(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/media/order",
		bytes.NewBufferString(`{"order":["m1","m2"]}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelReorderMedia(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
}

func TestHandleChannelReorderMediaRejectsDuplicateID(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add m2: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/media/order",
		bytes.NewBufferString(`{"order":["m1","m1"]}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelReorderMedia(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
}

func TestHandleChannelReorderMediaRejectsNonMember(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add m2: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/media/order",
		bytes.NewBufferString(`{"order":["m1","m9"]}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelReorderMedia(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
}

func TestHandleChannelReorderMediaUpdatesChain(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	insertMedia(t, conn, "m3", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add m2: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m3", 0); err != nil {
		t.Fatalf("add m3: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/media/order",
		bytes.NewBufferString(`{"order":["m3","m1","m2"]}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelReorderMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	rows, err := db.ChannelMediaList(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 || rows[0].MediaID != "m3" || rows[1].MediaID != "m1" || rows[2].MediaID != "m2" {
		t.Fatalf("unexpected order: %+v", rows)
	}
}

func TestHandleChannelMoveMediaRepositionsInChain(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)
	insertMedia(t, conn, "m3", 12000)
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add m2: %v", err)
	}
	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m3", 0); err != nil {
		t.Fatalf("add m3: %v", err)
	}
	// Starting chain: m1 → m2 → m3 (m1 from fixture).

	// Move m1 to after m2: m2 → m1 → m3.
	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/media/m1/move",
		bytes.NewBufferString(`{"afterMediaId":"m2"}`))
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m1")
	res := httptest.NewRecorder()
	app.handleChannelMoveMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	ordered, err := db.ChannelMediaOrdered(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("ordered: %v", err)
	}
	if want := []string{"m2", "m1", "m3"}; !equalStrings(ordered, want) {
		t.Fatalf("after move m1 after m2: got %v want %v", ordered, want)
	}

	// Move m3 to head (empty afterMediaId): m3 → m2 → m1.
	req = httptest.NewRequest(http.MethodPost, "/api/channels/ch/media/m3/move",
		bytes.NewBufferString(`{"afterMediaId":""}`))
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m3")
	res = httptest.NewRecorder()
	app.handleChannelMoveMedia(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("move to head: status=%d body=%s", res.Code, res.Body.String())
	}
	ordered, _ = db.ChannelMediaOrdered(context.Background(), conn, "ch")
	if want := []string{"m3", "m2", "m1"}; !equalStrings(ordered, want) {
		t.Fatalf("after move m3 to head: got %v want %v", ordered, want)
	}
}

func TestHandleChannelMoveMediaRejectsSelfAnchor(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/media/m1/move",
		bytes.NewBufferString(`{"afterMediaId":"m1"}`))
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m1")
	res := httptest.NewRecorder()
	app.handleChannelMoveMedia(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body.String())
	}
}

func TestHandleChannelMoveMediaRejectsUnknownChannel(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/missing/media/m1/move",
		bytes.NewBufferString(`{"afterMediaId":""}`))
	req.SetPathValue("channelID", "missing")
	req.SetPathValue("mediaID", "m1")
	res := httptest.NewRecorder()
	app.handleChannelMoveMedia(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func TestHandleChannelMoveMediaRejectsNonMember(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	// m1 exists in fixture; m9 does not.
	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/media/m9/move",
		bytes.NewBufferString(`{"afterMediaId":""}`))
	req.SetPathValue("channelID", "ch")
	req.SetPathValue("mediaID", "m9")
	res := httptest.NewRecorder()
	app.handleChannelMoveMedia(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
