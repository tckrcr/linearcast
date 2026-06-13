package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrMediaNotInChannel is returned when a write operation targets a
// (channel_id, media_id) pair that is not a member of the channel.
var ErrMediaNotInChannel = errors.New("media not in channel")

// ErrInvalidMove is returned when a move target is the moving row itself
// (would create a self-anchor cycle).
var ErrInvalidMove = errors.New("invalid move: cannot anchor to self")

// ChannelMediaList returns the channel_media rows for a channel in
// linked-list chain order (head first).
func ChannelMediaList(ctx context.Context, conn Execer, channelID string) ([]ChannelMediaRow, error) {
	order, err := ChannelMediaOrdered(ctx, conn, channelID)
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}
	rows, err := queryRows(ctx, conn, scanChannelMediaRow, `
        SELECT channel_id, media_id, added_at_ms
        FROM channel_media
        WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ChannelMediaRow, len(order))
	for _, r := range rows {
		byID[r.MediaID] = r
	}
	out := make([]ChannelMediaRow, 0, len(byID))
	for _, mediaID := range order {
		if r, ok := byID[mediaID]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// ChannelMediaOrdered walks the linked-list chain (anchor_media_id) and
// returns media IDs in chain order, head first. Empty channels return nil.
//
// Builds an in-memory anchor→child map then walks from the head. O(n) reads,
// O(n) memory, channel-sized. The unique partial indexes guarantee at most
// one head and at most one successor per anchor.
func ChannelMediaOrdered(ctx context.Context, conn Execer, channelID string) ([]string, error) {
	rows, err := queryRows(ctx, conn, scanChannelMediaOrderRow, `SELECT media_id, anchor_media_id FROM channel_media WHERE channel_id = ?`,
		channelID,
	)
	if err != nil {
		return nil, err
	}

	var head string
	headFound := false
	successor := map[string]string{}
	count := 0
	for _, row := range rows {
		count++
		if !row.anchor.Valid {
			head = row.mediaID
			headFound = true
			continue
		}
		successor[row.anchor.String] = row.mediaID
	}
	if count == 0 {
		return nil, nil
	}
	if !headFound {
		return nil, fmt.Errorf("channel %s: no head row (chain corrupt)", channelID)
	}

	out := make([]string, 0, count)
	out = append(out, head)
	cur := head
	for {
		next, ok := successor[cur]
		if !ok {
			break
		}
		out = append(out, next)
		cur = next
		if len(out) > count {
			return nil, fmt.Errorf("channel %s: chain longer than row count (cycle)", channelID)
		}
	}
	if len(out) != count {
		return nil, fmt.Errorf("channel %s: chain length %d != row count %d (broken chain)", channelID, len(out), count)
	}
	return out, nil
}

// ChannelMediaPackageList returns each channel_media row joined with its
// (optional) media_package for renditionProfile, in linked-list chain order.
// Rows whose media is missing from the chain (shouldn't happen — chain and
// channel_media are the same set) are silently skipped.
func ChannelMediaPackageList(ctx context.Context, conn Execer, channelID, renditionProfile string) ([]ChannelMediaPackageRow, error) {
	order, err := ChannelMediaOrdered(ctx, conn, channelID)
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}
	rows, err := queryRows(ctx, conn, scanChannelMediaPackageRow, `
        SELECT cm.channel_id, cm.media_id, cm.added_at_ms,
               m.path, m.title, m.scheduling_group, m.duration_ms,
               m.codec_check_passed, m.codec_check_reason,
               p.id, p.status, p.packaged_duration_ms, p.error
        FROM channel_media cm
        JOIN media m ON m.id = cm.media_id
        LEFT JOIN media_packages p
          ON p.media_id = m.id
         AND p.rendition_profile = ?
        WHERE cm.channel_id = ?`, renditionProfile, channelID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ChannelMediaPackageRow, len(order))
	for _, r := range rows {
		byID[r.MediaID] = r
	}
	out := make([]ChannelMediaPackageRow, 0, len(byID))
	for _, mediaID := range order {
		if r, ok := byID[mediaID]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func ChannelMediaExists(ctx context.Context, conn Execer, channelID, mediaID string) (bool, error) {
	row := conn.QueryRowContext(ctx, `SELECT 1 FROM channel_media WHERE channel_id = ? AND media_id = ? LIMIT 1`, channelID, mediaID)
	var n int
	if err := row.Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// EligibleChannelMedia returns the Media rows that belong to channelID
// (via channel_media) AND pass codec checks, ordered by the linked-list
// chain (anchor_media_id). This is what the scheduler iterates over.
// Chain entries whose media row fails codec_check are silently skipped.
func EligibleChannelMedia(ctx context.Context, conn Execer, channelID string) ([]Media, error) {
	order, err := ChannelMediaOrdered(ctx, conn, channelID)
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}
	rows, err := queryRows(ctx, conn, scanValue(scanMedia), `SELECT `+mediaColumns("m.")+`
        FROM channel_media cm
        JOIN media m ON m.id = cm.media_id
        JOIN channels c ON c.id = cm.channel_id
        WHERE cm.channel_id = ?
          AND m.codec_check_passed = 1
          AND COALESCE(m.media_kind, 'video') = c.media_kind`, channelID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Media, len(order))
	for _, m := range rows {
		byID[m.ID] = m
	}
	out := make([]Media, 0, len(byID))
	for _, mediaID := range order {
		if m, ok := byID[mediaID]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// EligibleReadyPackagedChannelMedia returns channel media that pass codec
// checks and have a ready package for renditionProfile, ordered by the
// linked-list chain. DurationMs is the packaged playable duration so schedule
// entries line up with packaged media. Chain entries that don't pass codec
// checks or don't have a ready package are silently skipped.
func EligibleReadyPackagedChannelMedia(ctx context.Context, conn Execer, channelID, renditionProfile string) ([]Media, error) {
	order, err := ChannelMediaOrdered(ctx, conn, channelID)
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}
	rows, err := queryRows(ctx, conn, scanValue(scanMedia), `
        SELECT m.id, m.path, m.directory, m.title, m.scheduling_group, m.user_preference,
               COALESCE(p.packaged_duration_ms, m.duration_ms) AS duration_ms,
               m.container, m.video_codec, m.video_width, m.video_height,
               m.color_transfer, m.color_primaries, m.audio_codec,
               m.codec_check_passed, m.codec_check_reason, m.ingested_at_ms, m.media_kind, m.source_ref
        FROM channel_media cm
        JOIN media m ON m.id = cm.media_id
        JOIN channels c ON c.id = cm.channel_id
        JOIN media_packages p ON p.media_id = m.id
        WHERE cm.channel_id = ?
          AND m.codec_check_passed = 1
          AND COALESCE(m.media_kind, 'video') = c.media_kind
          AND p.rendition_profile = ?
          AND p.status = ?
          AND p.packaged_duration_ms IS NOT NULL`,
		channelID, renditionProfile, string(PackageStatusReady))
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Media, len(order))
	for _, m := range rows {
		byID[m.ID] = m
	}
	out := make([]Media, 0, len(byID))
	for _, mediaID := range order {
		if m, ok := byID[mediaID]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// ChannelPackageCoverageMs returns the total packaged_duration_ms of all
// ready packages for a channel's media that would be eligible for scheduling.
// Returns 0 if no eligible ready packages exist.
func ChannelPackageCoverageMs(ctx context.Context, conn Execer, channelID, renditionProfile string) (int64, error) {
	var total int64
	err := conn.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(p.packaged_duration_ms), 0)
			FROM channel_media cm
			JOIN media m ON m.id = cm.media_id
			JOIN channels c ON c.id = cm.channel_id
			JOIN media_packages p ON p.media_id = m.id
			WHERE cm.channel_id = ?
			  AND m.codec_check_passed = 1
			  AND COALESCE(m.media_kind, 'video') = c.media_kind
			  AND p.rendition_profile = ?
			  AND p.status = ?
			  AND p.packaged_duration_ms IS NOT NULL`,
		channelID, renditionProfile, string(PackageStatusReady)).Scan(&total)
	return total, err
}

// ChannelPackageReadyCount returns the number of distinct media items in a
// channel that have a ready package for the given profile.
func ChannelPackageReadyCount(ctx context.Context, conn Execer, channelID, renditionProfile string) (int, error) {
	var n int
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT cm.media_id)
		FROM channel_media cm
		JOIN media m ON m.id = cm.media_id
		JOIN channels c ON c.id = cm.channel_id
		JOIN media_packages p ON p.media_id = m.id
		WHERE cm.channel_id = ?
		  AND m.codec_check_passed = 1
		  AND COALESCE(m.media_kind, 'video') = c.media_kind
		  AND p.rendition_profile = ?
		  AND p.status = ?`,
		channelID, renditionProfile, string(PackageStatusReady)).Scan(&n)
	return n, err
}

func scanChannelMediaRow(row scanner) (ChannelMediaRow, error) {
	var r ChannelMediaRow
	err := row.Scan(&r.ChannelID, &r.MediaID, &r.AddedAtMs)
	return r, err
}

type channelMediaOrderRow struct {
	mediaID string
	anchor  sql.NullString
}

func scanChannelMediaOrderRow(row scanner) (channelMediaOrderRow, error) {
	var r channelMediaOrderRow
	err := row.Scan(&r.mediaID, &r.anchor)
	return r, err
}

func scanChannelMediaPackageRow(row scanner) (ChannelMediaPackageRow, error) {
	var r ChannelMediaPackageRow
	var passed int64
	var title, group, codecReason sql.NullString
	var pkgID, pkgStatus, pkgError sql.NullString
	var pkgDurationMs sql.NullInt64
	if err := row.Scan(&r.ChannelID, &r.MediaID, &r.AddedAtMs,
		&r.Path, &title, &group, &r.DurationMs, &passed, &codecReason,
		&pkgID, &pkgStatus, &pkgDurationMs, &pkgError); err != nil {
		return ChannelMediaPackageRow{}, err
	}
	r.CodecCheckPassed = passed == 1
	r.Title = title.String
	r.SchedulingGroup = group.String
	r.CodecCheckReason = codecReason.String
	if pkgID.Valid {
		v := pkgID.String
		r.PackageID = &v
	}
	if pkgStatus.Valid {
		v := pkgStatus.String
		r.PackageStatus = &v
	}
	if pkgDurationMs.Valid {
		v := pkgDurationMs.Int64
		r.PackagedDurationMs = &v
	}
	if pkgError.Valid {
		v := pkgError.String
		r.PackageError = &v
	}
	return r, nil
}

// AddChannelMedia inserts a single (channel, media) membership at the tail of
// the linked-list chain. Returns (true, nil) if a new row was added, (false,
// nil) if it already existed.
func AddChannelMedia(ctx context.Context, conn *sql.DB, channelID, mediaID string, nowMs int64) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM channel_media WHERE channel_id = ? AND media_id = ? LIMIT 1`,
		channelID, mediaID,
	).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	tail, err := findTail(ctx, tx, channelID, "")
	if err != nil {
		return false, err
	}
	var anchor sql.NullString
	if tail != "" {
		anchor = sql.NullString{String: tail, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		 VALUES (?, ?, ?, ?)`,
		channelID, mediaID, anchor, nowMs,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// AddChannelMediaAfter inserts a single (channel, media) membership at a
// specific position in the linked-list chain. Pass afterMediaID == "" to
// insert at the head. Returns (true, nil) if a new row was added, (false, nil)
// if mediaID is already a member (position is NOT changed in that case — use
// MoveChannelMediaAfter to reposition existing rows).
func AddChannelMediaAfter(ctx context.Context, conn *sql.DB, channelID, mediaID, afterMediaID string, nowMs int64) (bool, error) {
	if mediaID == "" {
		return false, fmt.Errorf("media id required")
	}
	if mediaID == afterMediaID {
		return false, ErrInvalidMove
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM channel_media WHERE channel_id = ? AND media_id = ? LIMIT 1`,
		channelID, mediaID,
	).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	if afterMediaID != "" {
		var anchorExists int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM channel_media WHERE channel_id = ? AND media_id = ?`,
			channelID, afterMediaID,
		).Scan(&anchorExists)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrMediaNotInChannel
		}
		if err != nil {
			return false, err
		}
	}

	// Open a slot: whoever currently anchors to afterMediaID (or is the
	// current head if afterMediaID == "") will instead anchor to mediaID.
	if afterMediaID == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_media SET anchor_media_id = ?
			 WHERE channel_id = ? AND anchor_media_id IS NULL`,
			mediaID, channelID,
		); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_media SET anchor_media_id = ?
			 WHERE channel_id = ? AND anchor_media_id = ?`,
			mediaID, channelID, afterMediaID,
		); err != nil {
			return false, err
		}
	}

	var anchor sql.NullString
	if afterMediaID != "" {
		anchor = sql.NullString{String: afterMediaID, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		 VALUES (?, ?, ?, ?)`,
		channelID, mediaID, anchor, nowMs,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveChannelMedia deletes a (channel, media) membership and stitches the
// linked-list chain. Callers that need the two writes to be atomic should call
// this inside WithTx or WithImmediateTx.
func RemoveChannelMedia(ctx context.Context, conn Execer, channelID, mediaID string) (int64, error) {
	var oldAnchor sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT anchor_media_id FROM channel_media WHERE channel_id = ? AND media_id = ?`,
		channelID, mediaID,
	).Scan(&oldAnchor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM channel_media WHERE channel_id = ? AND media_id = ?`,
		channelID, mediaID,
	); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE channel_media SET anchor_media_id = ?
		 WHERE channel_id = ? AND anchor_media_id = ?`,
		oldAnchor, channelID, mediaID,
	); err != nil {
		return 0, err
	}
	return 1, nil
}

// MoveChannelMediaAfter repositions mediaID so its predecessor in the chain
// becomes afterMediaID. Pass afterMediaID == "" to move to the head. The move
// is implemented as delete-and-reinsert inside one transaction so the unique
// partial indexes are never violated mid-operation.
func MoveChannelMediaAfter(ctx context.Context, conn *sql.DB, channelID, mediaID, afterMediaID string) error {
	if mediaID == "" {
		return fmt.Errorf("media id required")
	}
	if mediaID == afterMediaID {
		return ErrInvalidMove
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var addedAtMs int64
	var oldAnchor sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT added_at_ms, anchor_media_id
		 FROM channel_media WHERE channel_id = ? AND media_id = ?`,
		channelID, mediaID,
	).Scan(&addedAtMs, &oldAnchor)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMediaNotInChannel
	}
	if err != nil {
		return err
	}

	if afterMediaID != "" {
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM channel_media WHERE channel_id = ? AND media_id = ?`,
			channelID, afterMediaID,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMediaNotInChannel
		}
		if err != nil {
			return err
		}
	}

	// 1. Pull mediaID out of the chain.
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_media WHERE channel_id = ? AND media_id = ?`,
		channelID, mediaID,
	); err != nil {
		return err
	}
	// 2. Stitch the hole: whoever pointed at mediaID now points at mediaID's old anchor.
	if _, err := tx.ExecContext(ctx, `UPDATE channel_media SET anchor_media_id = ?
		 WHERE channel_id = ? AND anchor_media_id = ?`,
		oldAnchor, channelID, mediaID,
	); err != nil {
		return err
	}
	// 3. Open a slot at the new position: whoever currently anchors to afterMediaID
	//    (or is the current head, if afterMediaID == "") will instead anchor to mediaID.
	if afterMediaID == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_media SET anchor_media_id = ?
			 WHERE channel_id = ? AND anchor_media_id IS NULL`,
			mediaID, channelID,
		); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_media SET anchor_media_id = ?
			 WHERE channel_id = ? AND anchor_media_id = ?`,
			mediaID, channelID, afterMediaID,
		); err != nil {
			return err
		}
	}
	// 4. Reinsert mediaID at its new position.
	var newAnchor sql.NullString
	if afterMediaID != "" {
		newAnchor = sql.NullString{String: afterMediaID, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		 VALUES (?, ?, ?, ?)`,
		channelID, mediaID, newAnchor, addedAtMs,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceChannelMedia replaces the channel_media membership of channelID with
// the supplied rows. added_at_ms comes from the caller. anchor_media_id is
// assigned by slice position (rows[0] is the head, rows[i].anchor =
// rows[i-1].media_id). Callers should wrap this in WithTx or WithImmediateTx.
func ReplaceChannelMedia(ctx context.Context, conn Execer, channelID string, rows []ChannelMediaRow) error {
	if _, err := conn.ExecContext(ctx, `DELETE FROM channel_media WHERE channel_id = ?`, channelID); err != nil {
		return err
	}
	stmt, err := conn.PrepareContext(ctx, `INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	var prev string
	for i, r := range rows {
		var anchor sql.NullString
		if i > 0 {
			anchor = sql.NullString{String: prev, Valid: true}
		}
		if _, err := stmt.ExecContext(ctx, channelID, r.MediaID, anchor, r.AddedAtMs); err != nil {
			return err
		}
		prev = r.MediaID
	}
	return nil
}

// findTail returns the media_id whose row in channelID has no successor (no
// other row anchors to it). Empty string means the channel has no rows. The
// `excludingMediaID` parameter is set during in-progress writes where the
// caller has already deleted a moving row and wants the tail of the remainder.
func findTail(ctx context.Context, tx *sql.Tx, channelID, excludingMediaID string) (string, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT cm.media_id FROM channel_media cm
		WHERE cm.channel_id = ?
		  AND cm.media_id != COALESCE(?, '')
		  AND NOT EXISTS (
			SELECT 1 FROM channel_media child
			WHERE child.channel_id = cm.channel_id
			  AND child.anchor_media_id = cm.media_id
			  AND child.media_id != COALESCE(?, '')
		  )
		LIMIT 1`,
		channelID, excludingMediaID, excludingMediaID,
	)
	var tail string
	err := row.Scan(&tail)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return tail, nil
}
