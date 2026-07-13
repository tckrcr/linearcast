package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func TestScheduleServiceUpsertEntryReturnsDomainErrorsDirectly(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)
	insertMedia(t, conn, "m2", 12000)

	_, err := app.schedule.UpsertEntry(context.Background(), "ch", scheduleEntryWriteRequest{
		MediaID: "m2",
		StartMs: 18000,
	})
	if !errors.Is(err, errMediaNotInChannel) {
		t.Fatalf("err=%v, want errMediaNotInChannel", err)
	}

	if _, err := db.AddChannelMedia(context.Background(), conn, "ch", "m2", 0); err != nil {
		t.Fatalf("add channel media: %v", err)
	}
	_, err = app.schedule.UpsertEntry(context.Background(), "ch", scheduleEntryWriteRequest{
		MediaID: "m2",
		StartMs: 18000,
	})
	if !errors.Is(err, errPackageNotReady) {
		t.Fatalf("err=%v, want errPackageNotReady", err)
	}
}

// insertFillerGapFixture creates channel 'ch' with primaries m1..m4 leaving
// three equal 42000ms gaps (gap A 18000..60000, gap B 78000..120000,
// gap C 138000..180000) and attaches a 90000ms filler asset "bumper" whose
// package is longer than any single gap so the rotation can advance before it
// has to wrap.
func insertFillerGapFixture(t *testing.T, conn *sql.DB) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	entries := make([]db.ScheduleEntry, 0, 4)
	for i, id := range []string{"m1", "m2", "m3", "m4"} {
		insertMedia(t, conn, id, 18000)
		entries = append(entries, db.ScheduleEntry{
			ChannelID:   "ch",
			StartMs:     int64(i) * 60000, // 0, 60000, 120000, 180000
			MediaID:     id,
			DurationMs:  18000,
			CreatedAtMs: 0,
		})
	}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, entries); err != nil {
		t.Fatalf("insert schedule entries: %v", err)
	}

	insertMedia(t, conn, "bumper", 90000)
	insertReadyPackage(t, conn, "bumper", 90000)
	asset, err := db.UpsertFillerAsset(context.Background(), conn, db.FillerAsset{
		MediaID: "bumper", Label: "Bumper", Kind: db.FillerKindBumper, Enabled: true, CreatedAtMs: 0,
	})
	if err != nil {
		t.Fatalf("upsert filler asset: %v", err)
	}
	if err := db.AttachChannelFillerAsset(context.Background(), conn, "ch", asset.ID, 1, true); err != nil {
		t.Fatalf("attach filler asset: %v", err)
	}
}

func TestScheduleServiceFillGapSequentialRotation(t *testing.T) {
	app, conn := testAdminApp(t)
	insertFillerGapFixture(t, conn)

	fillAt := func(startMs int64) int64 {
		t.Helper()
		res, err := app.schedule.FillGap(context.Background(), "ch", scheduleGapFillRequest{
			MediaID: "bumper", StartMs: startMs, OffsetMode: "sequential",
		})
		if err != nil {
			t.Fatalf("fill gap at %d: %v", startMs, err)
		}
		return res.OffsetMs
	}

	if got := fillAt(18000); got != 0 {
		t.Fatalf("gap A offset=%d, want 0 (first placement starts at the asset head)", got)
	}
	if got := fillAt(78000); got != 42000 {
		t.Fatalf("gap B offset=%d, want 42000 (continues where gap A left off)", got)
	}
	if got := fillAt(138000); got != 0 {
		t.Fatalf("gap C offset=%d, want 0 (wraps when continuation would overrun the asset)", got)
	}
}

func TestScheduleServiceFillGapZeroModeHonorsClientOffset(t *testing.T) {
	app, conn := testAdminApp(t)
	insertFillerGapFixture(t, conn)

	// Default mode leaves the explicit offset untouched.
	res, err := app.schedule.FillGap(context.Background(), "ch", scheduleGapFillRequest{
		MediaID: "bumper", StartMs: 18000, OffsetMs: 12000,
	})
	if err != nil {
		t.Fatalf("fill gap: %v", err)
	}
	if res.OffsetMs != 12000 {
		t.Fatalf("offset=%d, want 12000 (zero mode honors client offset)", res.OffsetMs)
	}
}

// insertSlotGridRecomposeFixture creates a slot_grid channel 'ch' (30m slots)
// with two ready primaries that each leave a 10m trailing gap and one attached,
// ready 10m filler asset long enough to tile those gaps.
func insertSlotGridRecomposeFixture(t *testing.T, conn *sql.DB) int64 {
	t.Helper()
	slotMs := int64(30 * 60 * 1000)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, schedule_mode, slot_duration_ms
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 'slot_grid', ?)`, slotMs); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	for _, id := range []string{"e1", "e2"} {
		insertMedia(t, conn, id, 20*60*1000)
		insertReadyPackage(t, conn, id, 20*60*1000)
		if _, err := db.AddChannelMedia(context.Background(), conn, "ch", id, 0); err != nil {
			t.Fatalf("add channel media %s: %v", id, err)
		}
	}
	insertMedia(t, conn, "bumper", 10*60*1000)
	insertReadyPackage(t, conn, "bumper", 10*60*1000)
	asset, err := db.UpsertFillerAsset(context.Background(), conn, db.FillerAsset{
		MediaID: "bumper", Label: "Bumper", Kind: db.FillerKindBumper, Enabled: true, CreatedAtMs: 0,
	})
	if err != nil {
		t.Fatalf("upsert filler asset: %v", err)
	}
	if err := db.AttachChannelFillerAsset(context.Background(), conn, "ch", asset.ID, 1, true); err != nil {
		t.Fatalf("attach filler asset: %v", err)
	}
	return slotMs
}

func TestScheduleServiceRecomposeSlotGridRejectsNonSlotGrid(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true) // 'ch' is back_to_back

	_, err := app.schedule.RecomposeSlotGridFuture(context.Background(), "ch")
	if !errors.Is(err, errNotSlotGrid) {
		t.Fatalf("err=%v, want errNotSlotGrid", err)
	}
}

func TestScheduleServiceRecomposeSlotGridFillsFutureGapFree(t *testing.T) {
	app, conn := testAdminApp(t)
	insertSlotGridRecomposeFixture(t, conn)

	res, err := app.schedule.RecomposeSlotGridFuture(context.Background(), "ch")
	if err != nil {
		t.Fatalf("recompose: %v", err)
	}
	if res.Inserted == 0 {
		t.Fatalf("recompose inserted 0 entries")
	}
	if res.Gappy {
		t.Fatalf("recompose reported a gappy future despite attached ready filler")
	}

	gaps, err := db.ScheduleGaps(context.Background(), conn, "ch", res.FromMs, res.LastEndMs)
	if err != nil {
		t.Fatalf("schedule gaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("recomposed future has %d gaps: %+v", len(gaps), gaps)
	}

	// Every persisted entry must carry the right entry_kind: bumper is filler,
	// e1/e2 are primary.
	rows, err := conn.Query(`SELECT media_id, entry_kind FROM schedule_entries WHERE channel_id = 'ch'`)
	if err != nil {
		t.Fatalf("query entry_kind: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID, kind string
		if err := rows.Scan(&mediaID, &kind); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := "primary"
		if mediaID == "bumper" {
			want = "filler"
		}
		if kind != want {
			t.Fatalf("entry media=%s entry_kind=%q, want %q", mediaID, kind, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
}

func TestScheduleServiceRecomposeSlotGridPreservesInProgressEntry(t *testing.T) {
	app, conn := testAdminApp(t)
	insertSlotGridRecomposeFixture(t, conn)

	nowMs := time.Now().UTC().UnixMilli()
	startMs := scheduler.Align6s(nowMs) - 6*60*1000 // started 6 minutes ago
	durMs := int64(30 * 60 * 1000)                  // still playing now
	if _, err := db.InsertScheduleEntries(context.Background(), conn, []db.ScheduleEntry{
		{ID: "live", ChannelID: "ch", StartMs: startMs, MediaID: "e1", OffsetMs: 0, DurationMs: durMs, Kind: "primary"},
	}); err != nil {
		t.Fatalf("insert in-progress entry: %v", err)
	}

	res, err := app.schedule.RecomposeSlotGridFuture(context.Background(), "ch")
	if err != nil {
		t.Fatalf("recompose: %v", err)
	}
	if want := startMs + durMs; res.FromMs != want {
		t.Fatalf("fromMs=%d, want %d (end of the in-progress entry)", res.FromMs, want)
	}
	got, err := db.ScheduleEntryByID(context.Background(), conn, "live")
	if err != nil {
		t.Fatalf("lookup live entry: %v", err)
	}
	if got == nil {
		t.Fatalf("recompose wiped the in-progress entry instead of preserving it")
	}
}

func TestScheduleServiceErrorMapsSentinels(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "media not in channel",
			err:        errMediaNotInChannel,
			wantStatus: http.StatusBadRequest,
			wantCode:   "media_not_in_channel",
		},
		{
			name:       "package not ready",
			err:        errPackageNotReady,
			wantStatus: http.StatusConflict,
			wantCode:   "package_not_ready",
		},
		{
			name:       "stale entry",
			err:        errEntryNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := httptest.NewRecorder()

			scheduleServiceError(res, tt.err)

			if res.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", res.Code, res.Body.String(), tt.wantStatus)
			}
			var body map[string]string
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["error"] != tt.wantCode {
				t.Fatalf("error=%q, want %q", body["error"], tt.wantCode)
			}
			if body["hint"] == "" {
				t.Fatalf("hint is empty for %s: %+v", tt.name, body)
			}
		})
	}
}

func TestWithImmediateTxRollsBackOnError(t *testing.T) {
	_, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	err := db.WithImmediateTx(context.Background(), conn, func(tx db.Execer) error {
		if _, clearErr := db.ClearScheduleAfter(context.Background(), tx, "ch", 0); clearErr != nil {
			return clearErr
		}
		return scheduleServiceTestErr("after clear")
	})
	if !errors.Is(err, scheduleServiceTestErr("after clear")) {
		t.Fatalf("err=%v, want after clear", err)
	}
	assertCount(t, conn, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = 'ch'`, 1)
}

type scheduleServiceTestErr string

func (e scheduleServiceTestErr) Error() string { return string(e) }
