package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

//go:embed schema.sql
var SchemaSQL string

const SchemaVersion = 21

// ApplySchema executes the embedded schema, runs version migrations, and
// seeds reference data. Idempotent — writers may call it on every startup.
// The linearcast read-only service must not (it would attempt to write).
func ApplySchema(ctx context.Context, conn *sql.DB) error {
	if _, err := conn.ExecContext(ctx, SchemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateV1toV2(conn); err != nil {
		return fmt.Errorf("migrate v1->v2: %w", err)
	}
	if err := migrateV2toV3(conn); err != nil {
		return fmt.Errorf("migrate v2->v3: %w", err)
	}
	if err := migrateV3toV4(conn); err != nil {
		return fmt.Errorf("migrate v3->v4: %w", err)
	}
	if err := migrateV4toV5(conn); err != nil {
		return fmt.Errorf("migrate v4->v5: %w", err)
	}
	if err := migrateV5toV6(conn); err != nil {
		return fmt.Errorf("migrate v5->v6: %w", err)
	}
	if err := migrateV6toV7(conn); err != nil {
		return fmt.Errorf("migrate v6->v7: %w", err)
	}
	if err := migrateV7toV8(conn); err != nil {
		return fmt.Errorf("migrate v7->v8: %w", err)
	}
	if err := migrateV8toV9(conn); err != nil {
		return fmt.Errorf("migrate v8->v9: %w", err)
	}
	if err := migrateV9toV10(conn); err != nil {
		return fmt.Errorf("migrate v9->v10: %w", err)
	}
	if err := migrateV10toV11(conn); err != nil {
		return fmt.Errorf("migrate v10->v11: %w", err)
	}
	if err := migrateV11toV12(conn); err != nil {
		return fmt.Errorf("migrate v11->v12: %w", err)
	}
	if err := migrateV12toV13(conn); err != nil {
		return fmt.Errorf("migrate v12->v13: %w", err)
	}
	if err := migrateV13toV14(conn); err != nil {
		return fmt.Errorf("migrate v13->v14: %w", err)
	}
	if err := migrateV14toV15(conn); err != nil {
		return fmt.Errorf("migrate v14->v15: %w", err)
	}
	if err := migrateV15toV16(conn); err != nil {
		return fmt.Errorf("migrate v15->v16: %w", err)
	}
	if err := migrateV16toV17(conn); err != nil {
		return fmt.Errorf("migrate v16->v17: %w", err)
	}
	if err := migrateV17toV18(conn); err != nil {
		return fmt.Errorf("migrate v17->v18: %w", err)
	}
	if err := migrateV18toV19(conn); err != nil {
		return fmt.Errorf("migrate v18->v19: %w", err)
	}
	if err := migrateV19toV20(conn); err != nil {
		return fmt.Errorf("migrate v19->v20: %w", err)
	}
	if err := migrateV20toV21(conn); err != nil {
		return fmt.Errorf("migrate v20->v21: %w", err)
	}
	if err := ensurePackageProfileLifecycleColumns(ctx, conn); err != nil {
		return fmt.Errorf("migrate package profile lifecycle: %w", err)
	}
	if err := pruneRemovedBuiltinProfiles(ctx, conn); err != nil {
		return fmt.Errorf("prune removed builtin profiles: %w", err)
	}
	if err := seedBuiltinProfiles(ctx, conn); err != nil {
		return fmt.Errorf("seed builtin profiles: %w", err)
	}
	return nil
}

func requireTable(ctx context.Context, conn *sql.DB, name string) error {
	var got string
	err := conn.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if err != nil {
		return fmt.Errorf("table %q not found: %w", name, err)
	}
	return nil
}

func seedBuiltinProfiles(ctx context.Context, conn *sql.DB) error {
	nowMs := time.Now().UTC().UnixMilli()
	for _, p := range packageprofile.BuiltIns() {
		jsonBytes, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("marshal profile %s: %w", p.Name, err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO package_profiles (name, is_builtin, disabled, profile_json, created_at_ms, updated_at_ms)
			 VALUES (?, 1, 0, ?, ?, ?)
			 ON CONFLICT(name) DO UPDATE SET
				is_builtin = 1,
				profile_json = excluded.profile_json,
				updated_at_ms = excluded.updated_at_ms`,
			p.Name, string(jsonBytes), nowMs, nowMs,
		); err != nil {
			return fmt.Errorf("seed profile %s: %w", p.Name, err)
		}
	}
	return nil
}

func pruneRemovedBuiltinProfiles(ctx context.Context, conn *sql.DB) error {
	names, err := queryRows(ctx, conn, scanString, `SELECT name FROM package_profiles WHERE is_builtin = 1`)
	if err != nil {
		return err
	}

	var removed []string
	for _, name := range names {
		if !packageprofile.Known(name) {
			removed = append(removed, name)
		}
	}
	for _, name := range removed {
		if _, err := conn.ExecContext(ctx, `DELETE FROM package_profiles WHERE name = ? AND is_builtin = 1`, name); err != nil {
			return err
		}
	}
	return nil
}

func ensurePackageProfileLifecycleColumns(ctx context.Context, conn *sql.DB) error {
	hasDisabled, err := tableHasColumn(ctx, conn, "package_profiles", "disabled")
	if err != nil {
		return err
	}
	if !hasDisabled {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE package_profiles ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_package_profiles_disabled ON package_profiles(disabled)`); err != nil {
		return err
	}
	return nil
}

func tableHasColumn(ctx context.Context, conn *sql.DB, table, column string) (bool, error) {
	columns, err := queryRows(ctx, conn, scanTableColumnName, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	for _, name := range columns {
		if name == column {
			return true, nil
		}
	}
	return false, nil
}

func scanTableColumnName(row scanner) (string, error) {
	var cid int
	var name, typ string
	var notNull int
	var defaultValue sql.NullString
	var pk int
	err := row.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk)
	return name, err
}

// VerifySchema confirms the embedded schema version matches the meta row.
func VerifySchema(ctx context.Context, conn *sql.DB) error {
	var v string
	row := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`)
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("schema_version row missing — initialize the database first")
		}
		return fmt.Errorf("read schema version: %w", err)
	}
	if v != fmt.Sprintf("%d", SchemaVersion) {
		return fmt.Errorf("schema version mismatch: db=%s expected=%d", v, SchemaVersion)
	}
	return nil
}
