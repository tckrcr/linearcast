package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupRoundTrip(t *testing.T) {
	ctx := context.Background()
	srcPath := newTestDB(t)

	// Seed rows so we can prove the snapshot preserves data, not just structure.
	rw, err := OpenReadWrite(srcPath)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'One', '/tmp', 'alphabetical', 1, 0), ('ch2', 'Two', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	rw.Close()

	dest := filepath.Join(t.TempDir(), BackupFileName(time.Now()))
	if err := Backup(ctx, srcPath, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// A successful Backup means the snapshot already verified; re-check for clarity.
	if err := VerifyBackup(ctx, dest); err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}

	// VACUUM INTO produces a standalone file with no WAL sidecar.
	if _, err := os.Stat(dest + "-wal"); !os.IsNotExist(err) {
		t.Errorf("snapshot has unexpected -wal sidecar")
	}

	// Data round-trips: the snapshot holds the same rows as the source.
	snap, err := openReadOnlyNoWAL(dest)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	var got int
	if err := snap.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&got); err != nil {
		t.Fatalf("count channels in snapshot: %v", err)
	}
	if got != 2 {
		t.Fatalf("snapshot channels = %d, want 2", got)
	}
}

func TestBackupRefusesExistingDestination(t *testing.T) {
	ctx := context.Background()
	srcPath := newTestDB(t)
	dest := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(dest, []byte("x"), 0o644); err != nil {
		t.Fatalf("pre-create dest: %v", err)
	}
	if err := Backup(ctx, srcPath, dest); err == nil {
		t.Fatal("Backup overwrote an existing destination; want error")
	}
}

func TestPruneBackupsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	times := []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	}
	for _, ts := range times {
		p := filepath.Join(dir, BackupFileName(ts))
		if err := os.WriteFile(p, []byte("snap"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// A non-snapshot file must be ignored by pruning.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write other: %v", err)
	}

	removed, err := PruneBackups(dir, 2)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %d, want 3", len(removed))
	}
	for _, ts := range times[3:] {
		if _, err := os.Stat(filepath.Join(dir, BackupFileName(ts))); err != nil {
			t.Errorf("expected newest snapshot kept: %v", err)
		}
	}
	for _, ts := range times[:3] {
		if _, err := os.Stat(filepath.Join(dir, BackupFileName(ts))); !os.IsNotExist(err) {
			t.Errorf("expected old snapshot %s pruned", BackupFileName(ts))
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-snapshot file must be kept: %v", err)
	}
}

func TestPruneBackupsNoopWhenUnderLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, BackupFileName(time.Now()))
	if err := os.WriteFile(p, []byte("snap"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	removed, err := PruneBackups(dir, 14)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %d, want 0", len(removed))
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("snapshot should remain: %v", err)
	}
}
