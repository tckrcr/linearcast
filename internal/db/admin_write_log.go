package db

import (
	"context"
	"database/sql"
	"fmt"
)

// AdminWriteLog is a single row from the admin_write_log table.
type AdminWriteLog struct {
	ID          int64
	CreatedAtMs int64
	Method      string
	Path        string
	Action      *string
	TargetType  *string
	TargetID    *string
	Status      int
	DurationMs  int64
}

// InsertAdminWriteLog appends a write-action record. Errors are non-fatal to
// the caller; the caller should log but not surface them to the HTTP client.
func InsertAdminWriteLog(ctx context.Context, conn *sql.DB, e AdminWriteLog) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO admin_write_log
			(created_at_ms, method, path, action, target_type, target_id, status, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.CreatedAtMs, e.Method, e.Path,
		e.Action, e.TargetType, e.TargetID,
		e.Status, e.DurationMs,
	)
	if err != nil {
		return fmt.Errorf("insert admin_write_log: %w", err)
	}
	return nil
}

// RecentAdminWriteLogs returns up to limit rows ordered newest-first.
// limit is capped at 500.
func RecentAdminWriteLogs(ctx context.Context, conn *sql.DB, limit int) ([]AdminWriteLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := conn.QueryContext(ctx, `
		SELECT id, created_at_ms, method, path, action, target_type, target_id, status, duration_ms
		FROM admin_write_log
		ORDER BY created_at_ms DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query admin_write_log: %w", err)
	}
	defer rows.Close()

	var out []AdminWriteLog
	for rows.Next() {
		var e AdminWriteLog
		var action, targetType, targetID sql.NullString
		if err := rows.Scan(
			&e.ID, &e.CreatedAtMs, &e.Method, &e.Path,
			&action, &targetType, &targetID,
			&e.Status, &e.DurationMs,
		); err != nil {
			return nil, fmt.Errorf("scan admin_write_log: %w", err)
		}
		if action.Valid {
			e.Action = &action.String
		}
		if targetType.Valid {
			e.TargetType = &targetType.String
		}
		if targetID.Valid {
			e.TargetID = &targetID.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
