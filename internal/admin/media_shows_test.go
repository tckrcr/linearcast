package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleMediaShowsUsesCollectionsAndStoredSeasonMetadata(t *testing.T) {
	app, conn := testAdminApp(t)
	ctx := context.Background()

	madMenID, err := db.UpsertCollection(ctx, conn, "Mad Men", "show", "manual")
	if err != nil {
		t.Fatalf("upsert Mad Men collection: %v", err)
	}
	officeID, err := db.UpsertCollection(ctx, conn, "The Office", "show", "manual")
	if err != nil {
		t.Fatalf("upsert Office collection: %v", err)
	}
	movieID, err := db.UpsertCollection(ctx, conn, "A Movie", "movie", "manual")
	if err != nil {
		t.Fatalf("upsert movie collection: %v", err)
	}

	if _, err := conn.Exec(`
		INSERT INTO media (
			id, path, directory, title, collection_id, season_number, episode_number,
			duration_ms, container, video_codec, video_height, audio_codec,
			codec_check_passed, ingested_at_ms
		) VALUES
			('mm-1', '/tmp/mm-1.mkv', '/tmp', 'Misleading S09E09', ?, 1, 1, 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
			('mm-2', '/tmp/mm-2.mkv', '/tmp', 'Episode Two',       ?, 1, 2, 7000, 'mkv', 'h264', 1080, 'aac', 1, 0),
			('mm-3', '/tmp/mm-3.mkv', '/tmp', 'Episode Three',     ?, 2, 1, 8000, 'mkv', 'h264', 1080, 'aac', 1, 0),
			('mm-bad', '/tmp/mm-bad.mkv', '/tmp', 'Unsupported',   ?, 3, 1, 9000, 'mkv', 'hevc', 2160, 'aac', 0, 0),
			('office-1', '/tmp/office-1.mkv', '/tmp', 'Pilot',     ?, NULL, NULL, 10000, 'mkv', 'h264', 1080, 'aac', 1, 0),
			('movie-1', '/tmp/movie-1.mkv', '/tmp', 'A Movie',     ?, NULL, NULL, 11000, 'mkv', 'h264', 1080, 'aac', 1, 0),
			('filler-1', '/tmp/filler-1.mkv', '/tmp', 'Bumper',    ?, NULL, NULL, 1000, 'mkv', 'h264', 1080, 'aac', 1, 0)
	`, madMenID, madMenID, madMenID, madMenID, officeID, movieID, officeID); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO filler_assets (id, media_id, label, kind, enabled, created_at_ms)
		VALUES ('filler-asset-1', 'filler-1', 'Bumper', 'filler', 1, 1)
	`); err != nil {
		t.Fatalf("insert filler asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/media/shows", nil)
	res := httptest.NewRecorder()
	app.handleMediaShows(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Shows []mediaShowSummary `json:"shows"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Shows) != 2 {
		t.Fatalf("shows=%+v, want two show collections", body.Shows)
	}

	madMen := body.Shows[0]
	if madMen.CollectionID != madMenID || madMen.Name != "Mad Men" {
		t.Fatalf("first show=%+v, want Mad Men collection", madMen)
	}
	if madMen.EpisodeCount != 3 || madMen.DurationMs != 21000 || madMen.SeasonCount != 2 {
		t.Fatalf("Mad Men totals=%+v, want 3 episodes over two stored seasons", madMen)
	}
	if len(madMen.Seasons) != 2 ||
		madMen.Seasons[0].SeasonNumber == nil || *madMen.Seasons[0].SeasonNumber != 1 ||
		madMen.Seasons[0].EpisodeCount != 2 ||
		madMen.Seasons[1].SeasonNumber == nil || *madMen.Seasons[1].SeasonNumber != 2 {
		t.Fatalf("Mad Men seasons=%+v, want stored seasons 1 and 2", madMen.Seasons)
	}

	office := body.Shows[1]
	if office.CollectionID != officeID || office.Name != "The Office" ||
		office.EpisodeCount != 1 || office.SeasonCount != 1 {
		t.Fatalf("second show=%+v, want one unsorted Office episode", office)
	}
	if len(office.Seasons) != 1 || office.Seasons[0].SeasonNumber != nil {
		t.Fatalf("Office seasons=%+v, want one null-season summary", office.Seasons)
	}
}
