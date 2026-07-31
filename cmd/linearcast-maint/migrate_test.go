package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestCreateMigrationSnapshotWritesVerifiedPriorVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatalf("migrate fresh: %v", err)
	}
	if _, err := conn.Exec(`ALTER TABLE channels ADD COLUMN playback_mode TEXT NOT NULL DEFAULT 'packaged'`); err != nil {
		t.Fatalf("add v1 playback mode: %v", err)
	}
	if _, err := conn.Exec(`ALTER TABLE channels ADD COLUMN upstream_hls_url TEXT`); err != nil {
		t.Fatalf("add v1 upstream URL: %v", err)
	}
	// play_history was dropped in v4; a genuine v1 database still has it.
	if _, err := conn.Exec(`CREATE TABLE play_history (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id        TEXT NOT NULL,
		schedule_entry_id TEXT NOT NULL,
		media_id          TEXT NOT NULL,
		started_at        INTEGER NOT NULL,
		ended_at          INTEGER NOT NULL,
		duration_ms       INTEGER NOT NULL,
		UNIQUE (channel_id, schedule_entry_id)
	)`); err != nil {
		t.Fatalf("add v1 play_history: %v", err)
	}
	if _, err := conn.Exec(`UPDATE meta SET value = ? WHERE key = 'schema_version'`, db.BaselineSchemaVersion); err != nil {
		t.Fatalf("set prior version: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO settings (key, value) VALUES ('snapshot_test', '"kept"')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	plan, err := db.PlanMigrations(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("plan migrations: %v", err)
	}
	if plan.CurrentVersion != db.BaselineSchemaVersion || plan.TargetVersion != db.SchemaVersion || !plan.NeedsMigration() {
		t.Fatalf("migration plan=%+v", plan)
	}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	snapshot, err := createMigrationSnapshot(context.Background(), dbPath, plan, now)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	want := filepath.Join(filepath.Dir(dbPath), "backups", db.BackupFileName(now))
	if snapshot != want {
		t.Fatalf("snapshot=%q, want %q", snapshot, want)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if err := db.VerifyBackupVersion(context.Background(), snapshot, db.BaselineSchemaVersion); err != nil {
		t.Fatalf("verify snapshot: %v", err)
	}

	snap, err := db.OpenReadWrite(snapshot)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	var value string
	if err := snap.QueryRow(`SELECT value FROM settings WHERE key = 'snapshot_test'`).Scan(&value); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if value != `"kept"` {
		t.Fatalf("snapshot value=%q", value)
	}
}

func TestCreateMigrationSnapshotNoopWithoutPendingMigration(t *testing.T) {
	snapshot, err := createMigrationSnapshot(context.Background(), "/does/not/exist", db.MigrationPlan{
		CurrentVersion: db.SchemaVersion,
		TargetVersion:  db.SchemaVersion,
	}, time.Now())
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if snapshot != "" {
		t.Fatalf("snapshot=%q, want empty", snapshot)
	}
}
