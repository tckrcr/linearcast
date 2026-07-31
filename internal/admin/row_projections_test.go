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
// (ChannelMediaPackageRow, ChannelFillerAsset, MediaPackageCandidate)
// across the A1 de-leak that flips Title/SchedulingGroup
// from sql.NullString → string and package-projection fields
// (PackageID, PackageStatus, etc.) from sql.Null* → pointer/*string.
// The handler signatures and response structs are unchanged,
// so the bytes must not move.

// --- ChannelMedia endpoint (ChannelMediaPackageRow) ---

func TestHandleChannelMediaNullFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "ep1", 12000)
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		required_package_profile) VALUES ('ch-null', 'Null Field', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps')`); err != nil {
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
		required_package_profile) VALUES ('ch-set', 'Set Field', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps')`); err != nil {
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
		required_package_profile) VALUES ('ch-fa-null', 'Null FA', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps')`); err != nil {
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
		required_package_profile) VALUES ('ch-fa-set', 'Set FA', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps')`); err != nil {
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
	if _, err := conn.Exec(`UPDATE media
		SET title = 'Candidate Title', scheduling_group = 'Group C', source_ref = 'plex://101',
		    season_number = 2, episode_number = 3, rating = 8.5
		WHERE id = 'cand-set'`); err != nil {
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
	responseBody := res.Body.Bytes()
	var body mediaPackageCandidateResponse
	if err := json.Unmarshal(responseBody, &body); err != nil {
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
	var raw struct {
		Media []map[string]json.RawMessage `json:"media"`
	}
	if err := json.Unmarshal(responseBody, &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	for _, field := range []string{"seasonNumber", "episodeNumber", "rating"} {
		if _, ok := raw.Media[0][field]; ok {
			t.Fatalf("candidate response unexpectedly contains inventory field %q: %s", field, res.Body.String())
		}
	}
}
