package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

// These characterize the JSON wire shape of the row-projection types
// (ChannelMediaPackageRow, ChannelFillerAsset, MediaPackageCandidate,
// PlayHistoryEntry) across the A1 de-leak that flips Title/SchedulingGroup
// from sql.NullString → string and package-projection fields
// (PackageID, PackageStatus, etc.) from sql.Null* → pointer/*string.
// The handler signatures and response structs are unchanged,
// so the bytes must not move.

// --- ChannelMedia endpoint (ChannelMediaPackageRow) ---

func TestHandleChannelMediaNullFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ep1", 12000)
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile) VALUES ('ch-null', 'Null Field', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch-null', 'ep1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel media: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch-null/media", nil)
	req.SetPathValue("channelID", "ch-null")
	res := httptest.NewRecorder()
	app.handleChannelMedia(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	want := `{"channelId":"ch-null","displayName":"Null Field","requiredPackageProfile":"h264-1080p-8mbps","count":1,"media":[{"mediaId":"ep1","path":"/tmp/ep1.mkv","durationMs":12000,"codecCheckPassed":true,"packageStatus":"missing","packageReady":false}]}` + "\n"
	if got := res.Body.String(); got != want {
		t.Fatalf("body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleChannelMediaSetFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ep2", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'My Episode', scheduling_group = 'Season 1', codec_check_reason = 'fast-pics' WHERE id = 'ep2'`); err != nil {
		t.Fatalf("set fields: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile) VALUES ('ch-set', 'Set Field', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch-set', 'ep2', NULL, 0)`); err != nil {
		t.Fatalf("insert channel media: %v", err)
	}
	insertReadyPackage(t, conn, "ep2", 12000)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch-set/media", nil)
	req.SetPathValue("channelID", "ch-set")
	res := httptest.NewRecorder()
	app.handleChannelMedia(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	want := `{"channelId":"ch-set","displayName":"Set Field","requiredPackageProfile":"h264-1080p-8mbps","count":1,"media":[{"mediaId":"ep2","title":"My Episode","path":"/tmp/ep2.mkv","collectionName":"Season 1","durationMs":12000,"codecCheckPassed":true,"codecCheckReason":"fast-pics","packageId":"pkg-ep2","packageStatus":"ready","packageReady":true,"packagedDurationMs":12000}]}` + "\n"
	if got := res.Body.String(); got != want {
		t.Fatalf("body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// --- ChannelFillerAssets endpoint (ChannelFillerAsset) ---

func TestHandleChannelFillerAssetsNullFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile) VALUES ('ch-fa-null', 'Null FA', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	insertMedia(t, conn, "fa1", 30000)
	if _, err := conn.Exec(`INSERT INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
		VALUES ('fa-null', 'fa1', 'Null Filler', 'bumper', 1, 0)`); err != nil {
		t.Fatalf("insert filler asset: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_filler_assets (channel_id, asset_id, weight, enabled)
		VALUES ('ch-fa-null', 'fa-null', 10, 1)`); err != nil {
		t.Fatalf("insert channel filler asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch-fa-null/filler-assets", nil)
	req.SetPathValue("channelID", "ch-fa-null")
	res := httptest.NewRecorder()
	app.handleChannelFillerAssets(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	want := `{"assets":[{"id":"fa-null","mediaId":"fa1","label":"Null Filler","kind":"bumper","enabled":true,"createdAtMs":0,"channelId":"ch-fa-null","weight":10,"channelEnabled":true,"path":"/tmp/fa1.mkv","durationMs":30000,"packageStatus":"missing","packageReady":false}],"channelId":"ch-fa-null","count":1,"requiredPackageProfile":"h264-1080p-8mbps"}` + "\n"
	if got := res.Body.String(); got != want {
		t.Fatalf("body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleChannelFillerAssetsSetFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		playback_mode, required_package_profile) VALUES ('ch-fa-set', 'Set FA', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	insertMedia(t, conn, "fa2", 45000)
	if _, err := conn.Exec(`UPDATE media SET title = 'My Filler', scheduling_group = 'Group F' WHERE id = 'fa2'`); err != nil {
		t.Fatalf("set fields: %v", err)
	}
	insertReadyPackage(t, conn, "fa2", 45000)
	if _, err := conn.Exec(`INSERT INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
		VALUES ('fa-set', 'fa2', 'Set Filler', 'filler', 1, 0)`); err != nil {
		t.Fatalf("insert filler asset: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_filler_assets (channel_id, asset_id, weight, enabled)
		VALUES ('ch-fa-set', 'fa-set', 5, 1)`); err != nil {
		t.Fatalf("insert channel filler asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch-fa-set/filler-assets", nil)
	req.SetPathValue("channelID", "ch-fa-set")
	res := httptest.NewRecorder()
	app.handleChannelFillerAssets(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	want := `{"assets":[{"id":"fa-set","mediaId":"fa2","label":"Set Filler","kind":"filler","enabled":true,"createdAtMs":0,"channelId":"ch-fa-set","weight":5,"channelEnabled":true,"path":"/tmp/fa2.mkv","title":"My Filler","collectionName":"Group F","durationMs":45000,"packageId":"pkg-fa2","packageStatus":"ready","packageReady":true,"packagedDurationMs":45000}],"channelId":"ch-fa-set","count":1,"requiredPackageProfile":"h264-1080p-8mbps"}` + "\n"
	if got := res.Body.String(); got != want {
		t.Fatalf("body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// --- MediaPackageCandidates endpoint (MediaPackageCandidate) ---

func TestHandleMediaPackageCandidatesNullTitleGroupWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "cand-null", 18000)

	req := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates", nil)
	res := httptest.NewRecorder()
	app.handleMediaPackageCandidates(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body mediaPackageCandidateResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Media) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(body.Media))
	}
	e := body.Media[0]
	if e.MediaID != "cand-null" || e.Title != "" || e.CollectionName != "" || e.PackageStatus != "missing" || e.PackageProfile != db.DefaultPackageProfile {
		t.Fatalf("null candidate mismatch: %+v", e)
	}
}

func TestHandleMediaPackageCandidatesSetTitleGroupWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "cand-set", 24000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Candidate Title', scheduling_group = 'Group C', source_ref = 'plex://101' WHERE id = 'cand-set'`); err != nil {
		t.Fatalf("set fields: %v", err)
	}
	pkgBytes := int64(123456)
	pkgDur := int64(24000)
	if err := db.UpsertMediaPackage(context.Background(), conn, db.MediaPackage{
		ID:                 "pkg-cand-set",
		MediaID:            "cand-set",
		RenditionProfile:   db.DefaultPackageProfile,
		Status:             db.PackageStatusReady,
		PackagedDurationMs: &pkgDur,
		PackageBytes:       &pkgBytes,
		CreatedAtMs:        1,
		UpdatedAtMs:        2,
	}); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates?status=ready", nil)
	res := httptest.NewRecorder()
	app.handleMediaPackageCandidates(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body mediaPackageCandidateResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Media) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(body.Media))
	}
	e := body.Media[0]
	if e.MediaID != "cand-set" || e.Title != "Candidate Title" || e.CollectionName != "Group C" || e.SourceRef != "plex://101" {
		t.Fatalf("set candidate mismatch: %+v", e)
	}
	if e.PackageBytes == nil || *e.PackageBytes != pkgBytes {
		t.Fatalf("packageBytes=%v, want %d", e.PackageBytes, pkgBytes)
	}
}

// --- ChannelHistory endpoint (PlayHistoryEntry) ---

func TestHandleChannelHistoryMediaFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	// The fixture inserts media m1 with NULL title and path /tmp/m1.mkv.
	insertDeleteFixture(t, conn, true)

	// Insert a second entry with a set title.
	insertMedia(t, conn, "m2", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'History Title' WHERE id = 'm2'`); err != nil {
		t.Fatalf("set title: %v", err)
	}
	var tailAnchor string
	if err := conn.QueryRow(`SELECT id FROM schedule_entries WHERE channel_id = 'ch' ORDER BY start_ms DESC LIMIT 1`).Scan(&tailAnchor); err != nil {
		t.Fatalf("read tail: %v", err)
	}
	e2 := db.ScheduleEntry{ID: "hist-m2", ChannelID: "ch", StartMs: 42000, MediaID: "m2", OffsetMs: 0, DurationMs: 12000, AnchorScheduleEntryID: &tailAnchor}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)`, e2.ID, e2.ChannelID, e2.StartMs, e2.MediaID, e2.OffsetMs, e2.DurationMs, tailAnchor); err != nil {
		t.Fatalf("insert e2: %v", err)
	}
	if _, err := db.RecordPlayHistory(context.Background(), conn, e2); err != nil {
		t.Fatalf("record history: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/ch/history?since=1", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelHistory(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body playHistoryResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]playHistoryAPIEntry{}
	for _, e := range body.Entries {
		byID[e.ScheduleEntryID] = e
	}
	// m2 has set title, m1 has NULL title.
	if e := byID[e2.ID]; e.MediaTitle != "History Title" || e.MediaPath != "/tmp/m2.mkv" {
		t.Fatalf("m2 history entry mismatch: %+v", e)
	}
	// m1 from fixture: the fixture creates the first entry in insertDeleteFixture.
	// The fixture entry has a random ID, so find it by media_id.
	for _, e := range body.Entries {
		if e.MediaID == "m1" && e.DurationMs == 18000 {
			if e.MediaTitle != "" {
				t.Fatalf("m1 NULL title should be empty, got %q", e.MediaTitle)
			}
			if e.MediaPath != "/tmp/m1.mkv" {
				t.Fatalf("m1 path mismatch: got %q", e.MediaPath)
			}
		}
	}
}
