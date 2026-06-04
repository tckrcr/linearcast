package db

import "context"

// ScheduleGaps returns all gaps (periods with no schedule entry) for a
// channel between startMs and endMs. Each gap reports the gap start, gap end,
// and gap duration in ms. Episode-to-episode transitions are not reported
// as gaps; only periods where startMs[n] > endMs[n-1] + 30s are included.
func ScheduleGaps(ctx context.Context, conn Execer, channelID string, startMs, endMs int64) ([]Gap, error) {
	return queryRows(ctx, conn, scanGap, `
		WITH entries AS (
			SELECT start_ms, start_ms + duration_ms AS end_ms
			FROM schedule_entries
			WHERE channel_id = ?
			  AND start_ms + duration_ms > ?
			  AND start_ms < ?
			ORDER BY start_ms
		),
		with_prev AS (
			SELECT
				start_ms,
				end_ms,
				LAG(end_ms) OVER (ORDER BY start_ms) AS prev_end
			FROM entries
		)
		SELECT prev_end, start_ms, start_ms - prev_end AS gap_ms
		FROM with_prev
		WHERE prev_end IS NOT NULL
		  AND start_ms - prev_end > 30000
		ORDER BY prev_end`, channelID, startMs, endMs)
}

type Gap struct {
	StartMs    int64 `json:"startMs"`
	EndMs      int64 `json:"endMs"`
	DurationMs int64 `json:"durationMs"`
}

func scanGap(row scanner) (Gap, error) {
	var g Gap
	err := row.Scan(&g.StartMs, &g.EndMs, &g.DurationMs)
	return g, err
}
