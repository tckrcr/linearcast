package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const scheduleEntryShiftTempMs = ScheduleGridMs * 1000000000

func CountScheduleEntries(ctx context.Context, conn *sql.DB, channelID string) (int, error) {
	var n int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_entries WHERE channel_id = ?`, channelID).Scan(&n)
	return n, err
}

// FirstScheduleEntryForMediaAtOrAfter returns the start_ms of the earliest
// schedule entry for (channelID, mediaID) at or after atMs. Returns (0, nil)
// when no matching entry exists.
func FirstScheduleEntryForMediaAtOrAfter(ctx context.Context, conn *sql.DB, channelID, mediaID string, atMs int64) (int64, error) {
	var startMs int64
	err := conn.QueryRowContext(ctx, `
		SELECT start_ms FROM schedule_entries
		WHERE channel_id = ? AND media_id = ? AND start_ms >= ?
		ORDER BY start_ms ASC LIMIT 1`, channelID, mediaID, atMs).Scan(&startMs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return startMs, nil
}

func ClearScheduleAfter(ctx context.Context, conn Execer, channelID string, afterMs int64) (int64, error) {
	res, err := conn.ExecContext(ctx, `DELETE FROM schedule_entries WHERE channel_id = ? AND start_ms >= ?`, channelID, afterMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func ClearSchedule(ctx context.Context, conn Execer, channelID string) (int64, error) {
	res, err := conn.ExecContext(ctx, `DELETE FROM schedule_entries WHERE channel_id = ?`, channelID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteScheduleEntry removes a single entry by its composite PK.
// Returns (true, nil) if the row existed and was deleted, (false, nil) if not found.
func DeleteScheduleEntry(ctx context.Context, conn *sql.DB, channelID string, startMs int64) (bool, error) {
	var found bool
	err := WithTx(ctx, conn, func(tx Execer) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM schedule_entries WHERE channel_id = ? AND start_ms = ?`,
			channelID, startMs,
		).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var deleteErr error
		found, deleteErr = DeleteScheduleEntryByID(ctx, tx, id)
		return deleteErr
	})
	return found, err
}

// ScheduleEntryByID returns the schedule entry with the given stable ID,
// or nil if no such entry exists.
func ScheduleEntryByID(ctx context.Context, conn Execer, id string) (*ScheduleEntry, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms,
		       anchor_schedule_entry_id, created_at_ms
		FROM schedule_entries WHERE id = ?`, id)
	var e ScheduleEntry
	var anchor sql.NullString
	if err := row.Scan(&e.ID, &e.ChannelID, &e.StartMs, &e.MediaID, &e.OffsetMs, &e.DurationMs, &anchor, &e.CreatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if anchor.Valid {
		v := anchor.String
		e.AnchorScheduleEntryID = &v
	}
	return &e, nil
}

// DeleteScheduleEntryByID removes a single entry by its stable ID and stitches
// the surrounding schedule chain. Callers that need the delete and stitch to be
// atomic should call this inside WithTx or WithImmediateTx. Returns
// (true, nil) if the row existed and was deleted, (false, nil) if not found.
func DeleteScheduleEntryByID(ctx context.Context, conn Execer, id string) (bool, error) {
	var channelID string
	var oldAnchor sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT channel_id, anchor_schedule_entry_id FROM schedule_entries WHERE id = ?`,
		id,
	).Scan(&channelID, &oldAnchor)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM schedule_entries WHERE id = ?`,
		id,
	); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE schedule_entries SET anchor_schedule_entry_id = ?
		 WHERE channel_id = ? AND anchor_schedule_entry_id = ?`,
		oldAnchor, channelID, id,
	); err != nil {
		return false, err
	}
	return true, nil
}

// InsertScheduleEntries writes entries using conn. Callers that need batch
// atomicity should call this inside WithTx or WithImmediateTx.
func InsertScheduleEntries(ctx context.Context, conn Execer, entries []ScheduleEntry) (int, error) {
	var inserted int
	var prevID string
	for _, e := range entries {
		if e.StartMs%ScheduleGridMs != 0 || e.DurationMs%ScheduleGridMs != 0 {
			return 0, fmt.Errorf("entry start_ms=%d or duration_ms=%d not aligned to %dms", e.StartMs, e.DurationMs, ScheduleGridMs)
		}
		id := e.ID
		if id == "" {
			id = uuid.New().String()
		}
		anchor := e.AnchorScheduleEntryID
		if anchor == nil && prevID != "" {
			p := prevID
			anchor = &p
		}
		kind := e.Kind
		if kind == "" {
			kind = "primary"
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, anchor_schedule_entry_id, created_at_ms, entry_kind)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, e.ChannelID, e.StartMs, e.MediaID, e.OffsetMs, e.DurationMs, anchor, e.CreatedAtMs, kind); err != nil {
			return 0, err
		}
		inserted++
		prevID = id
	}
	return inserted, nil
}

// ShiftScheduleEntriesStartAtOrAfter moves every schedule row for a channel at
// or after fromMs forward by deltaMs. The temporary offset avoids
// UNIQUE(channel_id, start_ms) collisions while the suffix is being shifted.
// Callers should wrap this in WithTx or WithImmediateTx.
func ShiftScheduleEntriesStartAtOrAfter(ctx context.Context, conn Execer, channelID string, fromMs, deltaMs int64) error {
	if deltaMs == 0 {
		return nil
	}
	if fromMs%ScheduleGridMs != 0 || deltaMs%ScheduleGridMs != 0 {
		return fmt.Errorf("from_ms=%d or delta_ms=%d not aligned to %dms", fromMs, deltaMs, ScheduleGridMs)
	}
	if deltaMs < 0 {
		return fmt.Errorf("delta_ms must be non-negative")
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE schedule_entries
		SET start_ms = start_ms + ?
		WHERE channel_id = ? AND start_ms >= ?`,
		scheduleEntryShiftTempMs, channelID, fromMs,
	); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE schedule_entries
		SET start_ms = start_ms + ?
		WHERE channel_id = ? AND start_ms >= ?`,
		deltaMs-scheduleEntryShiftTempMs, channelID, fromMs+scheduleEntryShiftTempMs,
	); err != nil {
		return err
	}
	return nil
}

// DeleteScheduleRangeIntersect removes entries that intersect [fromMs, toMs)
// and stitches the surrounding schedule chain. Callers that need the delete and
// stitch to be atomic should call this inside WithTx or WithImmediateTx.
func DeleteScheduleRangeIntersect(ctx context.Context, conn Execer, channelID string, fromMs, toMs int64) (int64, error) {
	var prevID sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT id FROM schedule_entries
		 WHERE channel_id = ? AND start_ms + duration_ms <= ?
		 ORDER BY start_ms DESC LIMIT 1`,
		channelID, fromMs,
	).Scan(&prevID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var nextID sql.NullString
	err = conn.QueryRowContext(ctx, `SELECT id FROM schedule_entries
		 WHERE channel_id = ? AND start_ms >= ?
		 ORDER BY start_ms ASC LIMIT 1`,
		channelID, toMs,
	).Scan(&nextID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := conn.ExecContext(ctx, `
		DELETE FROM schedule_entries
		WHERE channel_id = ?
		  AND start_ms < ?
		  AND start_ms + duration_ms > ?`, channelID, toMs, fromMs)
	if err != nil {
		return 0, err
	}
	if nextID.Valid {
		if _, err := conn.ExecContext(ctx, `UPDATE schedule_entries SET anchor_schedule_entry_id = ?
			 WHERE channel_id = ? AND id = ?`,
			prevID, channelID, nextID.String,
		); err != nil {
			return 0, err
		}
	}
	return res.RowsAffected()
}

// DeleteScheduleRangeIntersectForRewrite removes rows intersecting [fromMs,
// toMs) without stitching the successor to the predecessor. Callers use this
// when they are about to insert replacement rows and need to reconnect the
// successor to the last replacement instead of to the old predecessor. Callers
// should wrap this in WithTx or WithImmediateTx.
func DeleteScheduleRangeIntersectForRewrite(ctx context.Context, conn Execer, channelID string, fromMs, toMs int64) (int64, sql.NullString, sql.NullString, error) {
	var prevID sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT id FROM schedule_entries
		 WHERE channel_id = ? AND start_ms + duration_ms <= ?
		 ORDER BY start_ms DESC LIMIT 1`,
		channelID, fromMs,
	).Scan(&prevID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, sql.NullString{}, sql.NullString{}, err
	}

	var nextID sql.NullString
	err = conn.QueryRowContext(ctx, `SELECT id FROM schedule_entries
		 WHERE channel_id = ? AND start_ms >= ?
		 ORDER BY start_ms ASC LIMIT 1`,
		channelID, toMs,
	).Scan(&nextID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, sql.NullString{}, sql.NullString{}, err
	}

	res, err := conn.ExecContext(ctx, `
		DELETE FROM schedule_entries
		WHERE channel_id = ?
		  AND start_ms < ?
		  AND start_ms + duration_ms > ?`, channelID, toMs, fromMs)
	if err != nil {
		return 0, sql.NullString{}, sql.NullString{}, err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return 0, sql.NullString{}, sql.NullString{}, err
	}
	return rowsAffected, prevID, nextID, nil
}

func scheduleEntriesInWindow(entries []ScheduleEntry, fromMs, toMs int64) []ScheduleEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]ScheduleEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.StartMs < toMs && entry.StartMs+entry.DurationMs > fromMs {
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ScheduleWindowEnriched returns schedule entries that intersect [fromMs, toMs),
// joined with their media rows. Entries are ordered by schedule chain order.
func ScheduleWindowEnriched(ctx context.Context, conn *sql.DB, channelID string, fromMs, toMs int64) ([]ScheduleEntryEnriched, error) {
	ordered, err := ScheduleEntriesOrdered(ctx, conn, channelID)
	if err != nil {
		return nil, err
	}
	ordered = scheduleEntriesInWindow(ordered, fromMs, toMs)
	if len(ordered) == 0 {
		return nil, nil
	}

	mediaIDs := make([]string, 0, len(ordered))
	for _, entry := range ordered {
		mediaIDs = append(mediaIDs, entry.MediaID)
	}
	mediaByID, err := MediaByIDs(ctx, conn, mediaIDs)
	if err != nil {
		return nil, err
	}

	out := make([]ScheduleEntryEnriched, 0, len(ordered))
	for _, entry := range ordered {
		item := ScheduleEntryEnriched{
			ID:          entry.ID,
			ChannelID:   entry.ChannelID,
			StartMs:     entry.StartMs,
			MediaID:     entry.MediaID,
			OffsetMs:    entry.OffsetMs,
			DurationMs:  entry.DurationMs,
			CreatedAtMs: entry.CreatedAtMs,
		}
		if media, ok := mediaByID[entry.MediaID]; ok {
			item.Path = media.Path
			item.Title = media.Title
			item.CollectionName = media.CollectionName
			item.Description = media.Description
			item.ThumbPath = media.ThumbPath
			item.ContentRating = media.ContentRating
			item.Genres = append([]string(nil), media.Genres...)
			item.SeasonNumber = media.SeasonNumber
			item.EpisodeNumber = media.EpisodeNumber
		}
		out = append(out, item)
	}
	return out, nil
}

// ScheduleWindow returns schedule entries that intersect [fromMs, toMs).
// Entries are ordered by schedule chain order.
func ScheduleWindow(ctx context.Context, conn *sql.DB, channelID string, fromMs, toMs int64) ([]ScheduleEntry, error) {
	ordered, err := ScheduleEntriesOrdered(ctx, conn, channelID)
	if err != nil {
		return nil, err
	}
	return scheduleEntriesInWindow(ordered, fromMs, toMs), nil
}

// NextScheduleEntryAfter returns the first schedule entry strictly after t.
// Returns (nil, nil) if none.
func NextScheduleEntryAfter(ctx context.Context, conn *sql.DB, channelID string, afterMs int64) (*ScheduleEntry, error) {
	row := conn.QueryRowContext(ctx, `
        SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms
        FROM schedule_entries
        WHERE channel_id = ? AND start_ms > ?
        ORDER BY start_ms ASC LIMIT 1`, channelID, afterMs)
	var e ScheduleEntry
	if err := row.Scan(&e.ID, &e.ChannelID, &e.StartMs, &e.MediaID, &e.OffsetMs, &e.DurationMs, &e.CreatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// LastScheduleEntry returns the tail entry in the schedule chain for a
// channel. Returns (nil, nil) if the channel has no entries.
func LastScheduleEntry(ctx context.Context, conn Execer, channelID string) (*ScheduleEntry, error) {
	ordered, err := ScheduleEntriesOrdered(ctx, conn, channelID)
	if err != nil {
		return nil, err
	}
	if len(ordered) == 0 {
		return nil, nil
	}
	return &ordered[len(ordered)-1], nil
}

// LastScheduleEntryBefore returns the latest entry whose start is before
// beforeMs. Returns (nil, nil) if the channel has no earlier entries.
func LastScheduleEntryBefore(ctx context.Context, conn *sql.DB, channelID string, beforeMs int64) (*ScheduleEntry, error) {
	row := conn.QueryRowContext(ctx, `
        SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms
        FROM schedule_entries
        WHERE channel_id = ? AND start_ms < ?
        ORDER BY start_ms DESC LIMIT 1`, channelID, beforeMs)
	var e ScheduleEntry
	if err := row.Scan(&e.ID, &e.ChannelID, &e.StartMs, &e.MediaID, &e.OffsetMs, &e.DurationMs, &e.CreatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// FirstScheduleEntryEndingAfter returns the earliest schedule entry whose end
// (start_ms + duration_ms) is after afterMs — the entry currently airing or the
// next one in the future. The anchor is included so callers can splice the
// schedule chain when inserting ahead of it. Returns (nil, nil) when the
// channel has no such entry.
func FirstScheduleEntryEndingAfter(ctx context.Context, conn Execer, channelID string, afterMs int64) (*ScheduleEntry, error) {
	row := conn.QueryRowContext(ctx, `
        SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms,
               anchor_schedule_entry_id, created_at_ms
        FROM schedule_entries
        WHERE channel_id = ? AND start_ms + duration_ms > ?
        ORDER BY start_ms ASC LIMIT 1`, channelID, afterMs)
	var e ScheduleEntry
	var anchor sql.NullString
	if err := row.Scan(&e.ID, &e.ChannelID, &e.StartMs, &e.MediaID, &e.OffsetMs, &e.DurationMs, &anchor, &e.CreatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if anchor.Valid {
		v := anchor.String
		e.AnchorScheduleEntryID = &v
	}
	return &e, nil
}

// SetScheduleEntryAnchor sets (anchor non-nil) or clears (anchor nil) the chain
// anchor of a single schedule entry. Used to splice newly inserted entries into
// an existing chain.
func SetScheduleEntryAnchor(ctx context.Context, conn Execer, id string, anchor *string) error {
	_, err := conn.ExecContext(ctx, `UPDATE schedule_entries SET anchor_schedule_entry_id = ? WHERE id = ?`, anchor, id)
	return err
}

// LastEntryWithMediaBefore returns the most recent filler schedule entry for the
// given media on a channel whose start is before beforeMs. It is used to
// continue sequential filler rotation from where the previous placement of the
// same asset left off, so it only considers entry_kind='filler' rows — any
// incidental reuse of the same media as primary programming is ignored.
// Returns (nil, nil) when the media has no earlier filler placement.
func LastEntryWithMediaBefore(ctx context.Context, conn Execer, channelID, mediaID string, beforeMs int64) (*ScheduleEntry, error) {
	row := conn.QueryRowContext(ctx, `
        SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms
        FROM schedule_entries
        WHERE channel_id = ? AND media_id = ? AND start_ms < ? AND entry_kind = 'filler'
        ORDER BY start_ms DESC LIMIT 1`, channelID, mediaID, beforeMs)
	var e ScheduleEntry
	if err := row.Scan(&e.ID, &e.ChannelID, &e.StartMs, &e.MediaID, &e.OffsetMs, &e.DurationMs, &e.CreatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.Kind = "filler"
	return &e, nil
}

// LastPrimaryScheduleEntry returns the latest primary (non-filler) schedule
// entry, i.e. primary programming rather than attached filler. Extension uses
// it to position the episode-rotation cursor: once filler persists at the
// schedule tail, the plain tail entry's media would miss the primary list and
// reset rotation to the top. entry_kind is authoritative — a filler asset
// reused as primary programming is recorded as 'primary' on its entry.
// Returns (nil, nil) when the channel has no primary entries.
func LastPrimaryScheduleEntry(ctx context.Context, conn Execer, channelID string) (*ScheduleEntry, error) {
	row := conn.QueryRowContext(ctx, `
        SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms
        FROM schedule_entries
        WHERE channel_id = ? AND entry_kind = 'primary'
        ORDER BY start_ms DESC LIMIT 1`, channelID)
	var e ScheduleEntry
	if err := row.Scan(&e.ID, &e.ChannelID, &e.StartMs, &e.MediaID, &e.OffsetMs, &e.DurationMs, &e.CreatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.Kind = "primary"
	return &e, nil
}

// ChannelHasSchedule reports whether any schedule entry exists for the channel.
func ChannelHasSchedule(ctx context.Context, conn *sql.DB, channelID string) (bool, error) {
	row := conn.QueryRowContext(ctx, `SELECT 1 FROM schedule_entries WHERE channel_id = ? LIMIT 1`, channelID)
	var n int
	if err := row.Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// LoadGroupHistory returns one GroupCursor per collection label seen in
// the channel's schedule, plus the collection label of the entry with the
// highest start_ms (the "most recent group"). The recent-group string is
// "" when the channel has no schedule yet OR when the latest entry's media
// has no collection (treated as its own per-media group).
//
// Media with no collection are bucketed under "" (caller is
// responsible for treating "" as a per-media singleton group).
func LoadGroupHistory(ctx context.Context, conn Execer, channelID string) (map[string]GroupCursor, string, error) {
	return LoadGroupHistoryBefore(ctx, conn, channelID, 0)
}

// LoadGroupHistoryBefore returns group cursors from schedule rows before
// beforeMs. Pass beforeMs <= 0 to include the full schedule.
func LoadGroupHistoryBefore(ctx context.Context, conn Execer, channelID string, beforeMs int64) (map[string]GroupCursor, string, error) {
	whereBefore := ""
	args := []any{channelID}
	if beforeMs > 0 {
		whereBefore = " AND s.start_ms < ?"
		args = append(args, beforeMs)
	}
	rows, err := queryRows(ctx, conn, scanGroupHistoryRow, `
        SELECT s.start_ms, s.duration_ms, s.media_id,
               COALESCE(CASE WHEN c.kind = 'movie' THEN 'movie:' || c.name ELSE c.name END, m.scheduling_group, '')
        FROM schedule_entries s
        JOIN media m ON m.id = s.media_id
        LEFT JOIN collections c ON c.id = m.collection_id
        WHERE s.channel_id = ?
		`+whereBefore+`
        ORDER BY s.start_ms DESC`, args...)
	if err != nil {
		return nil, "", err
	}

	cursors := map[string]GroupCursor{}
	var recent string
	first := true
	for _, row := range rows {
		if first {
			recent = row.group
			first = false
		}
		if _, ok := cursors[row.group]; !ok {
			cursors[row.group] = GroupCursor{LastMediaID: row.mediaID, LastEndMs: row.startMs + row.durMs}
		}
	}
	return cursors, recent, nil
}

type groupHistoryRow struct {
	startMs int64
	durMs   int64
	mediaID string
	group   string
}

func scanGroupHistoryRow(row scanner) (groupHistoryRow, error) {
	var r groupHistoryRow
	err := row.Scan(&r.startMs, &r.durMs, &r.mediaID, &r.group)
	return r, err
}

// FindScheduleEntry returns the first entry in entries whose time window
// contains atMs ([StartMs, StartMs+DurationMs)), or nil if none matches.
// entries need not be sorted.
func FindScheduleEntry(entries []ScheduleEntry, atMs int64) *ScheduleEntry {
	for i := range entries {
		e := &entries[i]
		if e.StartMs <= atMs && atMs < e.StartMs+e.DurationMs {
			return e
		}
	}
	return nil
}
