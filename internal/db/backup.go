package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot file naming: a fixed-width, zero-padded UTC timestamp so a plain
// lexical sort of the names is also chronological order.
const (
	backupFilePrefix = "linearcast-"
	backupFileSuffix = ".db"
	backupTimeLayout = "20060102-150405"
)

// BackupFileName returns the conventional snapshot file name for t.
func BackupFileName(t time.Time) string {
	return backupFilePrefix + t.UTC().Format(backupTimeLayout) + backupFileSuffix
}

// Backup writes a consistent snapshot of the SQLite database at srcPath to
// destPath using VACUUM INTO. The source is opened read-only and is never
// modified; VACUUM INTO reads a transactional snapshot (correct even while the
// database is in WAL mode with live writers) and writes a fresh, defragmented
// database file with no WAL sidecar. destPath must not already exist.
//
// The source is integrity-checked before the snapshot and the resulting
// snapshot is verified afterward, so a successful return means the file on disk
// is a restorable database at the current schema version. A snapshot that fails
// verification is removed rather than left behind.
func Backup(ctx context.Context, srcPath, destPath string) error {
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("backup destination already exists: %s", destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat backup destination %s: %w", destPath, err)
	}

	src, err := openReadOnlyNoWAL(srcPath)
	if err != nil {
		return fmt.Errorf("open source database: %w", err)
	}
	defer src.Close()

	if err := quickCheck(ctx, src); err != nil {
		return fmt.Errorf("source integrity check: %w", err)
	}

	// VACUUM INTO does not accept bound parameters for the target, so the path
	// is embedded as a single-quoted SQL string literal with quotes escaped.
	// VACUUM cannot run inside a transaction; a bare ExecContext does not wrap
	// it in one.
	literal := "'" + strings.ReplaceAll(destPath, "'", "''") + "'"
	if _, err := src.ExecContext(ctx, "VACUUM INTO "+literal); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destPath, err)
	}

	if err := VerifyBackup(ctx, destPath); err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("verify snapshot %s: %w", destPath, err)
	}
	return nil
}

// VerifyBackup opens a snapshot read-only and confirms it passes an integrity
// check and carries the current schema version. It is the restorability gate:
// if this returns nil the file is a database that the running code can open.
func VerifyBackup(ctx context.Context, path string) error {
	snap, err := openReadOnlyNoWAL(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer snap.Close()
	if err := quickCheck(ctx, snap); err != nil {
		return fmt.Errorf("snapshot integrity check: %w", err)
	}
	return VerifySchema(ctx, snap)
}

// openReadOnlyNoWAL opens a database read-only without requesting
// journal_mode(WAL). It reads a WAL database (the live source) fine — WAL is
// detected from the file — and it also reads a rollback-mode database such as a
// fresh VACUUM INTO snapshot. OpenReadOnly forces journal_mode(WAL), which on a
// read-only connection to a non-WAL file attempts a write ("attempt to write a
// readonly database"); a snapshot converts to WAL on its first read-write open,
// which is what restore does.
func openReadOnlyNoWAL(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=foreign_keys(on)", url.PathEscape(path))
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func quickCheck(ctx context.Context, conn *sql.DB) error {
	var result string
	if err := conn.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("quick_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("quick_check reported: %s", result)
	}
	return nil
}

// PruneBackups removes the oldest snapshot files in dir, keeping the newest
// keep. Only files matching the snapshot naming convention are considered;
// anything else in the directory is ignored. keep <= 0 prunes nothing. The
// paths actually removed are returned.
func PruneBackups(dir string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read backup dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, backupFilePrefix) && strings.HasSuffix(n, backupFileSuffix) {
			names = append(names, n)
		}
	}
	if len(names) <= keep {
		return nil, nil
	}
	sort.Strings(names) // lexical == chronological for the fixed-width layout
	var removed []string
	for _, n := range names[:len(names)-keep] {
		p := filepath.Join(dir, n)
		if err := os.Remove(p); err != nil {
			return removed, fmt.Errorf("remove %s: %w", p, err)
		}
		removed = append(removed, p)
	}
	return removed, nil
}
