package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func inventoryBody(t *testing.T, app *App, target string) mediaInventoryResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	res := httptest.NewRecorder()
	app.handleMediaInventory(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got mediaInventoryResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestHandleMediaInventoryListsRowsWithCollectionAndPackageSummary(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "movie-one", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Movie One', scheduling_group = 'movie:Movie One', source_ref = 'plex://101' WHERE id = 'movie-one'`); err != nil {
		t.Fatalf("seed media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('pkg-ready', 'movie-one', 'h264-1080p-8mbps', 'ready', 0, 0),
		       ('pkg-failed', 'movie-one', 'hevc-copy-source', 'failed', 0, 0)`); err != nil {
		t.Fatalf("seed packages: %v", err)
	}

	got := inventoryBody(t, app, "/api/media/inventory?kind=movies")
	if got.Count != 1 || len(got.Media) != 1 {
		t.Fatalf("unexpected result count: %+v", got)
	}
	row := got.Media[0]
	if row.Collection != "" {
		t.Fatalf("movie inventory row should not expose a show label: %+v", row)
	}
	if row.PathRoot != "/tmp/movie-one.mkv" {
		t.Fatalf("path root mismatch: %q", row.PathRoot)
	}
	if row.Source != "plex" || row.SourceRef != "plex://101" {
		t.Fatalf("source mismatch: %+v", row)
	}
	if row.ReadyPackages != 1 || row.FailedPackages != 1 {
		t.Fatalf("package summary mismatch: %+v", row)
	}
}

func TestMediaInventoryDerivedFilenameFields(t *testing.T) {
	app, conn := testAdminApp(t)
	path := "/srv/media/tv/Example Show/example-show-s05e01-pilot-2160p-web-dl-h265-scenegroup.mkv"
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, title, scheduling_group, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('show-s05e01', ?, '/srv/media/tv/Example Show', 'Example Show S05E01', 'Example Show', 12000, 'mkv', 'hevc', 2160, 'eac3', 1, 0)`, path); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	got := inventoryBody(t, app, "/api/media/inventory?q=example-show")
	if got.Count != 1 || len(got.Media) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	row := got.Media[0]
	if row.PathRoot != "/srv/media/tv" {
		t.Fatalf("path root=%q", row.PathRoot)
	}
	if row.ReleaseGroup != "scenegroup" {
		t.Fatalf("release group=%q", row.ReleaseGroup)
	}
	if row.EpisodeCode != "S05E01" {
		t.Fatalf("episode code=%q", row.EpisodeCode)
	}
}

func TestHandleMediaInventoryKindShowsExcludesMovieGroups(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show-one-s01e01", 12000)
	insertMedia(t, conn, "movie-one", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Show One', scheduling_group = 'Show One' WHERE id = 'show-one-s01e01'`); err != nil {
		t.Fatalf("seed show: %v", err)
	}
	if _, err := conn.Exec(`UPDATE media SET title = 'Movie One', scheduling_group = 'movie:Movie One' WHERE id = 'movie-one'`); err != nil {
		t.Fatalf("seed movie: %v", err)
	}

	got := inventoryBody(t, app, "/api/media/inventory?kind=shows")
	if got.Count != 1 || len(got.Media) != 1 {
		t.Fatalf("unexpected shows result: %+v", got)
	}
	if got.Media[0].MediaID != "show-one-s01e01" {
		t.Fatalf("expected show row, got %+v", got.Media[0])
	}
}

func TestHandleMediaInventoryKindProgramsExcludesFillerAndMusic(t *testing.T) {
	app, conn := testAdminApp(t)
	insertMedia(t, conn, "show-one-s01e01", 12000)
	insertMedia(t, conn, "movie-one", 12000)
	insertMedia(t, conn, "bumper-one", 12000)
	insertMedia(t, conn, "track-one", 12000)
	if _, err := conn.Exec(`UPDATE media SET title = 'Movie One', scheduling_group = 'movie:Movie One' WHERE id = 'movie-one'`); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	if _, err := conn.Exec(`UPDATE media SET media_kind = 'music' WHERE id = 'track-one'`); err != nil {
		t.Fatalf("seed music: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
		VALUES ('fa-1', 'bumper-one', 'Bumper One', 'bumper', 1, 0)`); err != nil {
		t.Fatalf("seed filler asset: %v", err)
	}

	got := inventoryBody(t, app, "/api/media/inventory?kind=programs")
	if got.Count != 2 || len(got.Media) != 2 {
		t.Fatalf("unexpected programs result: %+v", got)
	}
	ids := map[string]bool{}
	for _, row := range got.Media {
		ids[row.MediaID] = true
	}
	if !ids["show-one-s01e01"] || !ids["movie-one"] {
		t.Fatalf("expected show and movie rows, got %+v", ids)
	}
}

func TestHandleMediaInventoryRejectsInvalidLimit(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/media/inventory?limit=abc", nil)
	res := httptest.NewRecorder()
	app.handleMediaInventory(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHandleMediaInventorySortsAndFilters(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, title, scheduling_group, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, source_ref, ingested_at_ms)
		VALUES
		('b', '/srv/media/zeta/Show/show-s01e02-cut-RLS.mkv', '/srv/media/zeta/Show', 'Bravo', 'Show', 1000, 'mkv', 'h264', 1080, 'aac', 1, 'plex://b', 0),
		('a', '/srv/media/alpha/Show/show-s01e01-cut-OTHER.mkv', '/srv/media/alpha/Show', 'Alpha', 'Show', 2000, 'mkv', 'h264', 720, 'aac', 1, '', 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	got := inventoryBody(t, app, "/api/media/inventory?source=plex&episode=s01e02&sortBy=duration&sortDir=desc")
	if got.Count != 1 || len(got.Media) != 1 {
		t.Fatalf("unexpected filtered result: %+v", got)
	}
	if got.Media[0].MediaID != "b" {
		t.Fatalf("got media id %q, want b", got.Media[0].MediaID)
	}

	got = inventoryBody(t, app, "/api/media/inventory?sortBy=title&sortDir=asc")
	if len(got.Media) != 2 || got.Media[0].MediaID != "a" || got.Media[1].MediaID != "b" {
		t.Fatalf("unexpected title sort order: %+v", got.Media)
	}
}

// seedInventorySwapFixtures inserts two episodes and one movie with varying
// ratings, season numbers, and package states to exercise the swap-era
// filters and per-profile fields.
func seedInventorySwapFixtures(t *testing.T, conn *sql.DB) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, title, scheduling_group, season_number, episode_number, rating, duration_ms, container, video_codec, video_height, video_bitrate_bps, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES
		('ep-ready', '/srv/media/Show/ep-ready.mkv', '/srv/media/Show', 'Ready Episode', 'Show', 1, 1, 9.0, 12000, 'mkv', 'h264', 1080, 8000000, 'aac', 1, 0),
		('ep-missing', '/srv/media/Show/ep-missing.mkv', '/srv/media/Show', 'Missing Episode', 'Show', 1, 2, 5.0, 13000, 'mkv', 'h264', 1080, 8000000, 'aac', 1, 0),
		('movie-low', '/srv/media/movie-low.mkv', '/srv/media', 'Low Movie', 'movie:Low Movie', NULL, NULL, 3.0, 90000, 'mkv', 'h264', 720, 4000000, 'aac', 1, 0)`); err != nil {
		t.Fatalf("seed media: %v", err)
	}
	insertReadyPackage(t, conn, "ep-ready", 11500)
}

func TestHandleMediaInventoryProfilePackageStatusFilter(t *testing.T) {
	app, conn := testAdminApp(t)
	seedInventorySwapFixtures(t, conn)

	got := inventoryBody(t, app, "/api/media/inventory?profile="+db.DefaultPackageProfile+"&profilePackageStatus=ready")
	if got.Count != 1 || len(got.Media) != 1 {
		t.Fatalf("expected 1 ready, got %+v", got)
	}
	if got.Media[0].MediaID != "ep-ready" {
		t.Fatalf("expected ep-ready, got %q", got.Media[0].MediaID)
	}
	if got.Media[0].ProfilePackageStatus != "ready" {
		t.Fatalf("profilePackageStatus=%q, want ready", got.Media[0].ProfilePackageStatus)
	}
	if got.Media[0].PackagedDurationMs == nil || *got.Media[0].PackagedDurationMs != 11500 {
		t.Fatalf("packagedDurationMs=%v, want 11500", got.Media[0].PackagedDurationMs)
	}

	got = inventoryBody(t, app, "/api/media/inventory?profile="+db.DefaultPackageProfile+"&profilePackageStatus=missing")
	if got.Count != 2 || len(got.Media) != 2 {
		t.Fatalf("expected 2 missing, got %+v", got)
	}
	for _, row := range got.Media {
		if row.ProfilePackageStatus != "missing" {
			t.Fatalf("profilePackageStatus=%q, want missing", row.ProfilePackageStatus)
		}
		if row.PackagedDurationMs != nil {
			t.Fatalf("packagedDurationMs should be nil for missing, got %v", *row.PackagedDurationMs)
		}
	}
}

func TestHandleMediaInventoryProfilePackageStatusRequiresProfile(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/media/inventory?profilePackageStatus=ready", nil)
	res := httptest.NewRecorder()
	app.handleMediaInventory(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHandleMediaInventoryVideoBitrateBpsInResponse(t *testing.T) {
	app, conn := testAdminApp(t)
	seedInventorySwapFixtures(t, conn)

	got := inventoryBody(t, app, "/api/media/inventory?q=Ready")
	if got.Count != 1 || len(got.Media) != 1 {
		t.Fatalf("expected 1 result, got %+v", got)
	}
	if got.Media[0].VideoBitrateBps != 8000000 {
		t.Fatalf("videoBitrateBps=%d, want 8000000", got.Media[0].VideoBitrateBps)
	}
}

func TestHandleMediaInventoryMinRatingFilter(t *testing.T) {
	app, conn := testAdminApp(t)
	seedInventorySwapFixtures(t, conn)

	got := inventoryBody(t, app, "/api/media/inventory?minRating=8.0")
	if got.Count != 1 || len(got.Media) != 1 {
		t.Fatalf("expected 1 result with rating >= 8.0, got %+v", got)
	}
	if got.Media[0].MediaID != "ep-ready" {
		t.Fatalf("expected ep-ready, got %q", got.Media[0].MediaID)
	}
}

func TestHandleMediaInventoryEpisodesOnlyFilter(t *testing.T) {
	app, conn := testAdminApp(t)
	seedInventorySwapFixtures(t, conn)

	got := inventoryBody(t, app, "/api/media/inventory?episodesOnly=1")
	if got.Count != 2 || len(got.Media) != 2 {
		t.Fatalf("expected 2 episodes, got %+v", got)
	}
	for _, row := range got.Media {
		if row.SeasonNumber == nil {
			t.Fatalf("expected season_number to be set, got nil for %q", row.MediaID)
		}
	}
}
