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

func TestHandleGuideReturnsTrimmedScheduleEntries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			required_package_profile, hidden_from_guide
		)
		VALUES ('vod one', 'VOD One', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps', 0),
		       ('hidden', 'Hidden', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps', 1)`); err != nil {
		t.Fatalf("insert channels: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms, title)
		VALUES ('m1', '/tmp/secret-path.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0, 'My Show')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('e1', 'vod one', 0, 'm1', 0, 18000, 0),
		       ('e2', 'hidden', 0, 'm1', 0, 18000, 0)`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "vod one"); err != nil {
		t.Fatalf("backfill vod one anchors: %v", err)
	}
	if err := db.BackfillScheduleEntryAnchorsForChannel(conn, "hidden"); err != nil {
		t.Fatalf("backfill hidden anchors: %v", err)
	}

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(6000).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/guide?from=0&hours=6", nil)
	res := httptest.NewRecorder()

	app.handleGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if got := res.Body.String(); strings.Contains(got, "secret-path.mkv") {
		t.Fatalf("response leaked filesystem path: %s", got)
	}
	var body guideResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.NowMs != 6000 || body.FromMs != 0 || body.ToMs != 6*3600*1000 {
		t.Fatalf("window fields=%+v", body)
	}
	if len(body.Channels) != 1 {
		t.Fatalf("channels=%+v, want one (hidden excluded)", body.Channels)
	}
	ch := body.Channels[0]
	if ch.ID != "vod one" {
		t.Fatalf("unexpected channel identity: %+v", ch)
	}
	if ch.ScheduleEndMs == nil || *ch.ScheduleEndMs != 18000 {
		t.Fatalf("scheduleEndMs=%v, want 18000", ch.ScheduleEndMs)
	}
	if len(ch.Entries) != 1 {
		t.Fatalf("entries=%+v, want one", ch.Entries)
	}
	e := ch.Entries[0]
	if e.EntryID != "e1" || e.MediaID != "m1" || e.Title != "My Show" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if e.StartMs != 0 || e.EndMs != 18000 || e.DurationMs != 18000 {
		t.Fatalf("unexpected entry timing: %+v", e)
	}
}

func TestHandleGuideClampsHours(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	app := New(Config{
		DB:  conn,
		Now: func() time.Time { return time.UnixMilli(0).UTC() },
	})
	req := httptest.NewRequest(http.MethodGet, "/api/guide?from=0&hours=9999", nil)
	res := httptest.NewRecorder()

	app.handleGuide(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body guideResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ToMs != guideMaxHours*3600*1000 {
		t.Fatalf("toMs=%d, want clamped to %d hours", body.ToMs, guideMaxHours)
	}
}
