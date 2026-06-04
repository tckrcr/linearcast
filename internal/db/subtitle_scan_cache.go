package db

import (
	"context"
	"database/sql"
	"errors"
)

// SaveSubtitleScanCache persists a completed scan result. showsJSON is the
// marshalled []ScannedShow blob; the caller owns serialisation.
func SaveSubtitleScanCache(ctx context.Context, conn *sql.DB, scannedAtMs int64, status string, showsJSON []byte) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO subtitle_scan_cache (id, scanned_at_ms, status, shows_json)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   scanned_at_ms = excluded.scanned_at_ms,
		   status        = excluded.status,
		   shows_json    = excluded.shows_json`,
		scannedAtMs, status, string(showsJSON),
	)
	return err
}

// LoadSubtitleScanCache returns the most recent persisted scan. Returns
// (0, "", nil, nil) if no row exists.
func LoadSubtitleScanCache(ctx context.Context, conn *sql.DB) (scannedAtMs int64, status string, showsJSON []byte, err error) {
	var raw string
	row := conn.QueryRowContext(ctx, `SELECT scanned_at_ms, status, shows_json FROM subtitle_scan_cache WHERE id = 1`)
	if scanErr := row.Scan(&scannedAtMs, &status, &raw); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, "", nil, nil
		}
		return 0, "", nil, scanErr
	}
	return scannedAtMs, status, []byte(raw), nil
}
