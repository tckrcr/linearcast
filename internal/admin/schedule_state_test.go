package admin

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

// These tests exercise schedule editing as a state machine. Each byte trace is
// replayable, and FuzzScheduleStateMachineProperties lets Go shrink a failing
// trace to the smallest sequence that violates an invariant.

const scheduleStateMediaDurationMs = int64(6 * time.Hour / time.Millisecond)

type scheduleStateRow struct {
	id         string
	startMs    int64
	mediaID    string
	offsetMs   int64
	durationMs int64
	anchorID   sql.NullString
	kind       string
}

func TestScheduleStateMachineProperties(t *testing.T) {
	for seed := int64(0); seed < 12; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			trace := make([]byte, 72)
			if _, err := rand.New(rand.NewSource(seed)).Read(trace); err != nil {
				t.Fatalf("generate trace: %v", err)
			}
			runScheduleStateTrace(t, trace)
		})
	}
}

func FuzzScheduleStateMachineProperties(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 1, 1, 2, 0, 3, 3, 1, 0})
	f.Add([]byte{3, 1, 0, 0, 2, 4, 4, 0, 0, 5, 0, 0})
	f.Add([]byte{8, 1, 0, 2, 3, 0, 7, 0, 2, 6, 0, 0})
	f.Fuzz(func(t *testing.T, trace []byte) {
		runScheduleStateTrace(t, trace)
	})
}

func runScheduleStateTrace(t *testing.T, trace []byte) {
	t.Helper()
	if len(trace) > 96 {
		trace = trace[:96]
	}
	t.Logf("schedule state trace: %x", trace)

	app, conn := testAdminApp(t)
	nowMs := scheduler.AlignToGrid(time.Now().UTC().UnixMilli())
	app.schedule.now = func() time.Time { return time.UnixMilli(nowMs).UTC() }
	startMs := nowMs + db.ScheduleGridMs
	insertScheduleStateFixture(t, conn, startMs)
	assertScheduleStateInvariants(t, conn)

	ctx := context.Background()
	for step := 0; step+2 < len(trace); step += 3 {
		rows := loadScheduleStateRows(t, conn)
		if len(rows) == 0 {
			t.Fatalf("step %d: state machine unexpectedly reached an empty schedule", step/3)
		}
		op, a, b := trace[step]%9, int(trace[step+1]), int(trace[step+2])
		t.Logf("step %d: op=%d a=%d b=%d rows=%d", step/3, op, a, b, len(rows))

		switch op {
		case 0, 1: // Insert before or after a stable entry ID.
			editable := scheduleStateEditableRows(rows, nowMs, op == 1)
			if len(editable) == 0 {
				continue
			}
			target := editable[a%len(editable)]
			mediaID := fmt.Sprintf("m%d", b%5)
			var err error
			if op == 0 {
				_, err = app.schedule.InsertEntryBefore(ctx, "ch", target.id, mediaID)
			} else {
				_, err = app.schedule.InsertEntryAfter(ctx, "ch", target.id, mediaID)
			}
			if err != nil {
				t.Fatalf("step %d: insert relative: %v", step/3, err)
			}
			assertScheduleStateExistingOrder(t, rows, loadScheduleStateRows(t, conn))

		case 2: // Move by saving the same entries in a different draft order.
			if len(rows) < 2 {
				continue
			}
			i, j := a%len(rows), b%len(rows)
			if i == j {
				j = (j + 1) % len(rows)
			}
			entries := make([]scheduleWindowSaveOrderedEntry, len(rows))
			for k, row := range rows {
				entries[k].MediaID = row.mediaID
			}
			entries[i], entries[j] = entries[j], entries[i]
			extendTail := false
			_, err := app.schedule.SaveWindowOrdered(ctx, "ch", scheduleWindowSaveOrderedRequest{
				FromMs: rows[0].startMs, ToMs: rows[len(rows)-1].startMs + rows[len(rows)-1].durationMs,
				TailMode: "preserve", ExtendTail: &extendTail, Entries: entries,
			})
			if err != nil {
				t.Fatalf("step %d: move draft entries: %v", step/3, err)
			}
			moved := loadScheduleStateRows(t, conn)
			if !reflect.DeepEqual(scheduleStateMediaCounts(moved), scheduleStateMediaCounts(rows)) {
				t.Fatalf("step %d: move changed media multiset\nbefore=%+v\nafter=%+v", step/3, rows, moved)
			}
			if scheduleStateDurationTotal(moved) != scheduleStateDurationTotal(rows) {
				t.Fatalf("step %d: move changed aggregate duration from %d to %d", step/3, scheduleStateDurationTotal(rows), scheduleStateDurationTotal(moved))
			}

		case 3: // Delete without rebuilding; the removed interval is a legal gap.
			if len(rows) < 3 {
				continue
			}
			target := rows[1+a%(len(rows)-2)]
			beforeGapMs := scheduleStateGapTotal(rows)
			if _, err := app.schedule.DeleteEntry(ctx, "ch", target.id, false); err != nil {
				t.Fatalf("step %d: delete without rebuild: %v", step/3, err)
			}
			after := loadScheduleStateRows(t, conn)
			afterGapMs := scheduleStateGapTotal(after)
			if afterGapMs != beforeGapMs+target.durationMs {
				t.Fatalf("step %d: legal gap total=%d, want %d after deleting %s", step/3, afterGapMs, beforeGapMs+target.durationMs, target.id)
			}
			wantIDs := make([]string, 0, len(rows)-1)
			for _, row := range rows {
				if row.id != target.id {
					wantIDs = append(wantIDs, row.id)
				}
			}
			if gotIDs := scheduleStateIDs(after); !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("step %d: delete removed the wrong row: got IDs %v, want %v", step/3, gotIDs, wantIDs)
			}

		case 4: // Delete and rebuild the suffix.
			target := rows[a%len(rows)]
			if _, err := app.schedule.DeleteEntry(ctx, "ch", target.id, true); err != nil {
				t.Fatalf("step %d: delete with rebuild: %v", step/3, err)
			}

		case 5: // Automatic extension.
			if _, err := scheduler.ExtendChannel(ctx, conn, "ch", scheduler.ServiceOptions{
				HorizonHours: 48, NowMs: nowMs,
			}); err != nil {
				t.Fatalf("step %d: extend: %v", step/3, err)
			}

		case 6: // A rejected stale-ID mutation must be atomic.
			before := loadScheduleStateRows(t, conn)
			if _, err := app.schedule.DeleteEntry(ctx, "ch", "missing-entry", b%2 == 0); err == nil {
				t.Fatalf("step %d: stale delete unexpectedly succeeded", step/3)
			}
			after := loadScheduleStateRows(t, conn)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("step %d: rejected stale delete mutated schedule\nbefore=%+v\nafter=%+v", step/3, before, after)
			}

		case 7: // Manual tail insertion followed by automatic continuation.
			last := rows[len(rows)-1]
			_, err := app.schedule.UpsertEntry(ctx, "ch", scheduleEntryWriteRequest{
				MediaID: fmt.Sprintf("m%d", b%5),
				StartMs: last.startMs + last.durationMs,
			})
			if err != nil {
				t.Fatalf("step %d: upsert tail: %v", step/3, err)
			}

		case 8: // Range deletion, with either an explicit gap or a rebuilt tail.
			if len(rows) < 3 {
				continue
			}
			first := 1 + a%(len(rows)-2)
			last := first
			if b%2 == 1 && first+1 < len(rows)-1 {
				last++
			}
			_, err := app.schedule.DeleteRange(ctx, "ch", rows[first].startMs,
				rows[last].startMs+rows[last].durationMs, trace[step+2]%2 == 0)
			if err != nil {
				t.Fatalf("step %d: delete range: %v", step/3, err)
			}
		}

		assertScheduleStateInvariants(t, conn)
	}
}

func TestSlotGridRecomposeStateProperties(t *testing.T) {
	for seed := int64(0); seed < 6; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			app, conn := testAdminApp(t)
			slotMs := insertSlotGridRecomposeFixture(t, conn)
			nowMs := scheduler.AlignToGrid(time.Now().UTC().UnixMilli())
			app.schedule.now = func() time.Time { return time.UnixMilli(nowMs).UTC() }
			rng := rand.New(rand.NewSource(seed))
			if _, err := app.schedule.RecomposeSlotGridFuture(context.Background(), "ch"); err != nil {
				t.Fatalf("initial recompose: %v", err)
			}

			for step := 0; step < 12; step++ {
				rows := loadScheduleStateRows(t, conn)
				switch rng.Intn(4) {
				case 0:
					res, err := app.schedule.RecomposeSlotGridFuture(context.Background(), "ch")
					if err != nil {
						t.Fatalf("step %d: recompose: %v", step, err)
					}
					assertScheduleStateContiguous(t, conn, res.FromMs, res.LastEndMs)
				case 1:
					if _, err := scheduler.ExtendChannel(context.Background(), conn, "ch", scheduler.ServiceOptions{
						HorizonHours: 30, NowMs: nowMs,
					}); err != nil {
						t.Fatalf("step %d: extend: %v", step, err)
					}
				case 2, 3:
					future := scheduleStateFutureRows(rows, nowMs)
					if len(future) > 2 {
						target := future[1+rng.Intn(len(future)-1)]
						if _, err := app.schedule.DeleteEntry(context.Background(), "ch", target.id, false); err != nil {
							t.Fatalf("step %d: delete slot-grid entry: %v", step, err)
						}
					}
				}
				assertScheduleStateInvariants(t, conn)
				assertSlotGridPrimaryAlignment(t, conn, slotMs)
			}

			res, err := app.schedule.RecomposeSlotGridFuture(context.Background(), "ch")
			if err != nil {
				t.Fatalf("final recompose: %v", err)
			}
			assertScheduleStateInvariants(t, conn)
			assertScheduleStateContiguous(t, conn, res.FromMs, res.LastEndMs)
			assertSlotGridPrimaryAlignment(t, conn, slotMs)
		})
	}
}

func insertScheduleStateFixture(t *testing.T, conn *sql.DB, startMs int64) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			required_package_profile
		)
		VALUES ('ch', 'State Machine', '/tmp', 'alphabetical', 1, 0, ?)`, db.DefaultPackageProfile); err != nil {
		t.Fatalf("insert state-machine channel: %v", err)
	}
	for i := 0; i < 5; i++ {
		mediaID := fmt.Sprintf("m%d", i)
		insertMedia(t, conn, mediaID, scheduleStateMediaDurationMs)
		insertReadyPackage(t, conn, mediaID, scheduleStateMediaDurationMs)
		if _, err := db.AddChannelMedia(context.Background(), conn, "ch", mediaID, 0); err != nil {
			t.Fatalf("add state-machine media %s: %v", mediaID, err)
		}
	}
	entries := make([]db.ScheduleEntry, 4)
	for i := range entries {
		entries[i] = db.ScheduleEntry{
			ID: fmt.Sprintf("seed-%d", i), ChannelID: "ch",
			StartMs: startMs + int64(i)*scheduleStateMediaDurationMs,
			MediaID: fmt.Sprintf("m%d", i), DurationMs: scheduleStateMediaDurationMs,
		}
	}
	if _, err := db.InsertScheduleEntries(context.Background(), conn, entries); err != nil {
		t.Fatalf("insert state-machine schedule: %v", err)
	}
}

func loadScheduleStateRows(t *testing.T, conn *sql.DB) []scheduleStateRow {
	t.Helper()
	rows, err := conn.Query(`
		SELECT id, start_ms, media_id, offset_ms, duration_ms,
		       anchor_schedule_entry_id, entry_kind
		FROM schedule_entries
		WHERE channel_id = 'ch'
		ORDER BY start_ms, id`)
	if err != nil {
		t.Fatalf("query schedule state: %v", err)
	}
	defer rows.Close()
	var out []scheduleStateRow
	for rows.Next() {
		var row scheduleStateRow
		if err := rows.Scan(&row.id, &row.startMs, &row.mediaID, &row.offsetMs,
			&row.durationMs, &row.anchorID, &row.kind); err != nil {
			t.Fatalf("scan schedule state: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schedule state: %v", err)
	}
	return out
}

func assertScheduleStateInvariants(t *testing.T, conn *sql.DB) {
	t.Helper()
	rows := loadScheduleStateRows(t, conn)
	if len(rows) == 0 {
		t.Fatal("schedule has no rows")
	}
	issues, err := db.ValidateScheduleEntryChains(context.Background(), conn)
	if err != nil {
		t.Fatalf("validate schedule chains: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("schedule chain issues: %+v", issues)
	}
	ordered, err := db.ScheduleEntriesOrdered(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("walk schedule chain: %v", err)
	}
	if len(ordered) != len(rows) {
		t.Fatalf("chain rows=%d, chronological rows=%d", len(ordered), len(rows))
	}
	for i, row := range rows {
		if ordered[i].ID != row.id {
			t.Fatalf("chain order differs from chronological order at %d: chain=%s chronological=%s", i, ordered[i].ID, row.id)
		}
		if row.startMs%db.ScheduleGridMs != 0 || row.durationMs%db.ScheduleGridMs != 0 || row.offsetMs%db.ScheduleGridMs != 0 {
			t.Fatalf("row %s is off the %dms grid: start=%d duration=%d offset=%d", row.id, db.ScheduleGridMs, row.startMs, row.durationMs, row.offsetMs)
		}
		if row.durationMs <= 0 || row.offsetMs < 0 {
			t.Fatalf("row %s has invalid duration/offset: duration=%d offset=%d", row.id, row.durationMs, row.offsetMs)
		}
		var mediaDurationMs int64
		if err := conn.QueryRow(`SELECT duration_ms FROM media WHERE id = ?`, row.mediaID).Scan(&mediaDurationMs); err != nil {
			t.Fatalf("lookup media %s: %v", row.mediaID, err)
		}
		if row.offsetMs+row.durationMs > mediaDurationMs {
			t.Fatalf("row %s exceeds media bounds: offset+duration=%d media=%d", row.id, row.offsetMs+row.durationMs, mediaDurationMs)
		}
		if i > 0 {
			prevEnd := rows[i-1].startMs + rows[i-1].durationMs
			if row.startMs < prevEnd {
				t.Fatalf("row %s starts at %d before predecessor ends at %d", row.id, row.startMs, prevEnd)
			}
		}
	}
}

func assertScheduleStateContiguous(t *testing.T, conn *sql.DB, fromMs, toMs int64) {
	t.Helper()
	rows := loadScheduleStateRows(t, conn)
	var previousEnd int64
	seen := false
	for _, row := range rows {
		endMs := row.startMs + row.durationMs
		if endMs <= fromMs || row.startMs >= toMs {
			continue
		}
		if !seen {
			if row.startMs > fromMs {
				t.Fatalf("schedule starts at %d after contiguous window start %d", row.startMs, fromMs)
			}
			seen = true
		} else if row.startMs != previousEnd {
			t.Fatalf("schedule gap [%d,%d) inside contiguous window [%d,%d)", previousEnd, row.startMs, fromMs, toMs)
		}
		previousEnd = endMs
	}
	if !seen || previousEnd < toMs {
		t.Fatalf("schedule covers through %d, want at least %d", previousEnd, toMs)
	}
}

func assertSlotGridPrimaryAlignment(t *testing.T, conn *sql.DB, slotMs int64) {
	t.Helper()
	for _, row := range loadScheduleStateRows(t, conn) {
		if row.kind == "primary" && row.startMs%slotMs != 0 {
			t.Fatalf("primary row %s starts at %d, off %dms slot grid", row.id, row.startMs, slotMs)
		}
	}
}

func scheduleStateGapTotal(rows []scheduleStateRow) int64 {
	var total int64
	for i := 1; i < len(rows); i++ {
		previousEnd := rows[i-1].startMs + rows[i-1].durationMs
		if rows[i].startMs > previousEnd {
			total += rows[i].startMs - previousEnd
		}
	}
	return total
}

func scheduleStateFutureRows(rows []scheduleStateRow, nowMs int64) []scheduleStateRow {
	var future []scheduleStateRow
	for _, row := range rows {
		if row.startMs > nowMs {
			future = append(future, row)
		}
	}
	return future
}

func scheduleStateEditableRows(rows []scheduleStateRow, nowMs int64, insertAfter bool) []scheduleStateRow {
	var editable []scheduleStateRow
	for _, row := range rows {
		boundaryMs := row.startMs
		if insertAfter {
			boundaryMs += row.durationMs
		}
		if boundaryMs > nowMs {
			editable = append(editable, row)
		}
	}
	return editable
}

func assertScheduleStateExistingOrder(t *testing.T, before, after []scheduleStateRow) {
	t.Helper()
	beforeSet := make(map[string]bool, len(before))
	for _, row := range before {
		beforeSet[row.id] = true
	}
	got := make([]string, 0, len(before))
	for _, row := range after {
		if beforeSet[row.id] {
			got = append(got, row.id)
		}
	}
	want := scheduleStateIDs(before)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relative insert reordered existing rows: got %v, want %v", got, want)
	}
}

func scheduleStateIDs(rows []scheduleStateRow) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.id
	}
	return ids
}

func scheduleStateMediaCounts(rows []scheduleStateRow) map[string]int {
	counts := make(map[string]int)
	for _, row := range rows {
		counts[row.mediaID]++
	}
	return counts
}

func scheduleStateDurationTotal(rows []scheduleStateRow) int64 {
	var total int64
	for _, row := range rows {
		total += row.durationMs
	}
	return total
}
