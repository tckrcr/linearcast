package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// defaultBackupDir is the conventional snapshot directory beside the database.
func defaultBackupDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "backups")
}

// cmdBackup writes a verified, timestamped snapshot of the live database and
// prunes older snapshots to the retention limit. It does not run ApplySchema:
// a backup must never mutate the database it is protecting.
func cmdBackup(dbPath string, args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dir := fs.String("dir", "", "directory to write the snapshot into (default: <db-dir>/backups)")
	keep := fs.Int("keep", 14, "snapshots to retain; older ones are pruned (0 disables pruning)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	backupDir := *dir
	if backupDir == "" {
		backupDir = defaultBackupDir(dbPath)
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		log.Fatalf("backup: create dir %s: %v", backupDir, err)
	}

	dest := filepath.Join(backupDir, db.BackupFileName(time.Now()))
	ctx := context.Background()
	if err := db.Backup(ctx, dbPath, dest); err != nil {
		log.Fatalf("backup: %v", err)
	}
	log.Printf("backup: wrote and verified %s", dest)

	removed, err := db.PruneBackups(backupDir, *keep)
	if err != nil {
		log.Fatalf("backup: prune: %v", err)
	}
	for _, p := range removed {
		log.Printf("backup: pruned old snapshot %s", p)
	}
}

// cmdRestore validates a snapshot and swaps it into the live database path. It
// requires --confirm and assumes all linearcast services are stopped, since it
// overwrites the live database file.
func cmdRestore(dbPath string, args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	confirm := fs.Bool("confirm", false, "required to overwrite the live database")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		log.Fatal("usage: linearcast-maint restore [--confirm] <snapshot.db>")
	}
	snapshot := rest[0]
	ctx := context.Background()

	if err := db.VerifyBackup(ctx, snapshot); err != nil {
		log.Fatalf("restore: %q is not a valid restorable backup: %v", snapshot, err)
	}
	log.Printf("restore: snapshot %s verified (integrity + schema version)", snapshot)

	if !*confirm {
		log.Fatalf("restore: refusing without --confirm.\n"+
			"  STOP all linearcast services first (e.g. `docker compose down`), then re-run with --confirm.\n"+
			"  This will overwrite: %s", dbPath)
	}

	if err := restoreInPlace(snapshot, dbPath); err != nil {
		log.Fatalf("restore: %v", err)
	}
	if err := db.VerifyBackup(ctx, dbPath); err != nil {
		log.Fatalf("restore: restored database failed verification: %v", err)
	}
	log.Printf("restore: complete — %s now holds %s", dbPath, snapshot)
	log.Printf("restore: start services again (e.g. `docker compose up -d`).")
}

// restoreInPlace moves the current database and its WAL sidecars aside with a
// timestamped suffix, then copies the snapshot into place. Callers must ensure
// all linearcast services are stopped first. The moved-aside files are left
// behind so a botched restore can be reversed by hand.
func restoreInPlace(snapshot, dbPath string) error {
	stamp := time.Now().UTC().Format("20060102-150405")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := dbPath + suffix
		switch _, err := os.Stat(p); {
		case err == nil:
			aside := fmt.Sprintf("%s.pre-restore-%s", p, stamp)
			if err := os.Rename(p, aside); err != nil {
				return fmt.Errorf("move aside %s: %w", p, err)
			}
			log.Printf("restore: moved %s -> %s", p, aside)
		case os.IsNotExist(err):
			// nothing to move aside
		default:
			return fmt.Errorf("stat %s: %w", p, err)
		}
	}
	if err := copyFile(snapshot, dbPath); err != nil {
		return fmt.Errorf("copy snapshot into place: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
