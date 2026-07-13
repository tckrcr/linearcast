package db

import (
	"database/sql"
	"fmt"
)

// backfillScheduleEntryAnchorsForChannel walks one channel's schedule entries in
// (start_ms, id) order and sets anchor_schedule_entry_id to the predecessor's
// id, leaving the head row NULL. It only writes rows whose anchor is still NULL,
// so re-running is a no-op for channels already populated.
func backfillScheduleEntryAnchorsForChannel(db *sql.DB, channelID string) error {
	rows, err := db.Query(
		`SELECT id FROM schedule_entries
		 WHERE channel_id = ?
		 ORDER BY start_ms, id`,
		channelID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ordered []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ordered = append(ordered, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, entryID := range ordered {
		var anchor sql.NullString
		if i > 0 {
			anchor = sql.NullString{String: ordered[i-1], Valid: true}
		}
		if _, err := tx.Exec(
			`UPDATE schedule_entries
			 SET anchor_schedule_entry_id = ?
			 WHERE channel_id = ? AND id = ? AND anchor_schedule_entry_id IS NULL`,
			anchor, channelID, entryID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BackfillScheduleEntryAnchorsForChannel populates anchor_schedule_entry_id for
// one channel from the current start_ms ordering, only touching rows still NULL.
// It is intended for fixture setup and one-off repair scripts, not as a normal
// runtime dependency.
func BackfillScheduleEntryAnchorsForChannel(db *sql.DB, channelID string) error {
	return backfillScheduleEntryAnchorsForChannel(db, channelID)
}

// RepairScheduleEntryAnchorsForChannel rewrites anchor_schedule_entry_id for
// every row in a channel from the current start_ms ordering. Unlike the
// backfill helper it overwrites existing anchors, so it recovers a channel
// after raw SQL mutations. Intended for fixture setup and one-off repair
// scripts, not as a normal runtime dependency.
func RepairScheduleEntryAnchorsForChannel(db *sql.DB, channelID string) error {
	rows, err := db.Query(
		`SELECT id FROM schedule_entries
		 WHERE channel_id = ?
		 ORDER BY start_ms, id`,
		channelID,
	)
	if err != nil {
		return fmt.Errorf("list schedule entry rows: %w", err)
	}
	defer rows.Close()

	var ordered []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ordered = append(ordered, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, entryID := range ordered {
		var anchor sql.NullString
		if i > 0 {
			anchor = sql.NullString{String: ordered[i-1], Valid: true}
		}
		if _, err := tx.Exec(
			`UPDATE schedule_entries
			 SET anchor_schedule_entry_id = ?
			 WHERE channel_id = ? AND id = ?`,
			anchor, channelID, entryID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
