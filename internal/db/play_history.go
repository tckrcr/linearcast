package db

import (
	"context"
	"database/sql"
	"fmt"
)

// RecordPlayHistory inserts one history row for a schedule entry. It is
// idempotent for repeated manifest requests within the same entry.
func RecordPlayHistory(ctx context.Context, conn *sql.DB, entry ScheduleEntry) (bool, error) {
	res, err := conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO play_history
		  (channel_id, schedule_entry_id, media_id, started_at, ended_at, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?)`,
		entry.ChannelID,
		entry.ID,
		entry.MediaID,
		entry.StartMs,
		entry.StartMs+entry.DurationMs,
		entry.DurationMs,
	)
	if err != nil {
		return false, fmt.Errorf("insert play_history channel=%s entry=%s: %w", entry.ChannelID, entry.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func PlayHistorySince(ctx context.Context, conn *sql.DB, channelID string, sinceMs int64) ([]PlayHistoryEntry, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT
			ph.id,
			ph.channel_id,
			ph.schedule_entry_id,
			ph.media_id,
			ph.started_at,
			ph.ended_at,
			ph.duration_ms,
			m.title,
			m.path
		FROM play_history ph
		LEFT JOIN media m ON m.id = ph.media_id
		WHERE ph.channel_id = ?
		  AND ph.started_at >= ?
		ORDER BY ph.started_at DESC, ph.id DESC`,
		channelID, sinceMs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PlayHistoryEntry
	for rows.Next() {
		var e PlayHistoryEntry
		var mediaTitle, mediaPath sql.NullString
		if err := rows.Scan(
			&e.ID,
			&e.ChannelID,
			&e.ScheduleEntryID,
			&e.MediaID,
			&e.StartedAtMs,
			&e.EndedAtMs,
			&e.DurationMs,
			&mediaTitle,
			&mediaPath,
		); err != nil {
			return nil, err
		}
		e.MediaTitle = mediaTitle.String
		e.MediaPath = mediaPath.String
		out = append(out, e)
	}
	return out, rows.Err()
}
