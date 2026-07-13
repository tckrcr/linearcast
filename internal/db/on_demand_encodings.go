package db

import (
	"context"
	"database/sql"
)

// OnDemandEncoding is the admin-visible state of one ephemeral ffmpeg encoding.
// Rows are owned by linearcast playback and are deleted when the encoding is
// torn down; this is current process state, not history.
type OnDemandEncoding struct {
	EncodingID       string
	ChannelID        string
	ChannelName      string
	ScheduleEntryID  string
	MediaID          string
	MediaTitle       string
	Profile          string
	State            string
	ProcessRunning   bool
	SpawnedAtMs      int64
	FirstSegmentAtMs int64
	LastProgressMs   int64
	SegmentCount     int
	UpdatedAtMs      int64
	LastError        string
}

func ClearOnDemandEncodings(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM on_demand_encodings`)
	return err
}

func UpsertOnDemandEncoding(ctx context.Context, conn *sql.DB, s OnDemandEncoding) error {
	running := 0
	if s.ProcessRunning {
		running = 1
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO on_demand_encodings (
			encoding_id, channel_id, schedule_entry_id, media_id, profile, state,
			process_running, spawned_at_ms, first_segment_at_ms, last_progress_ms,
			segment_count, updated_at_ms, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))
		ON CONFLICT(encoding_id) DO UPDATE SET
			channel_id = excluded.channel_id,
			schedule_entry_id = excluded.schedule_entry_id,
			media_id = excluded.media_id,
			profile = excluded.profile,
			state = excluded.state,
			process_running = excluded.process_running,
			spawned_at_ms = excluded.spawned_at_ms,
			first_segment_at_ms = excluded.first_segment_at_ms,
			last_progress_ms = excluded.last_progress_ms,
			segment_count = excluded.segment_count,
			updated_at_ms = excluded.updated_at_ms,
			last_error = excluded.last_error`,
		s.EncodingID, s.ChannelID, s.ScheduleEntryID, s.MediaID, s.Profile, s.State,
		running, s.SpawnedAtMs, s.FirstSegmentAtMs, s.LastProgressMs,
		s.SegmentCount, s.UpdatedAtMs, s.LastError)
	return err
}

func DeleteOnDemandEncoding(ctx context.Context, conn *sql.DB, encodingID string) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM on_demand_encodings WHERE encoding_id = ?`, encodingID)
	return err
}

func ListOnDemandEncodings(ctx context.Context, conn *sql.DB) ([]OnDemandEncoding, error) {
	return queryRows(ctx, conn, scanOnDemandEncoding, `
		SELECT s.encoding_id, s.channel_id, COALESCE(c.display_name, ''),
		       s.schedule_entry_id, s.media_id, COALESCE(m.title, ''),
		       s.profile, s.state, s.process_running, s.spawned_at_ms,
		       s.first_segment_at_ms, s.last_progress_ms, s.segment_count,
		       s.updated_at_ms, COALESCE(s.last_error, '')
		  FROM on_demand_encodings s
		  LEFT JOIN channels c ON c.id = s.channel_id
		  LEFT JOIN media m ON m.id = s.media_id
		 ORDER BY s.spawned_at_ms, s.encoding_id`)
}

func scanOnDemandEncoding(row scanner) (OnDemandEncoding, error) {
	var s OnDemandEncoding
	var running int
	if err := row.Scan(
		&s.EncodingID,
		&s.ChannelID,
		&s.ChannelName,
		&s.ScheduleEntryID,
		&s.MediaID,
		&s.MediaTitle,
		&s.Profile,
		&s.State,
		&running,
		&s.SpawnedAtMs,
		&s.FirstSegmentAtMs,
		&s.LastProgressMs,
		&s.SegmentCount,
		&s.UpdatedAtMs,
		&s.LastError,
	); err != nil {
		return OnDemandEncoding{}, err
	}
	s.ProcessRunning = running == 1
	return s, nil
}
