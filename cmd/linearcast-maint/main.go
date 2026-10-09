// cmd/linearcast-maint contains maintenance-only database and repair tools.
//
// Operator channel and playlist writes belong to the admin API/UI. This binary
// intentionally keeps only recovery, bootstrap, and diagnostic commands.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/linearcastlog"
)

func main() {
	linearcastlog.SetupJSON()

	if len(os.Args) < 2 {
		usage()
	}
	sub := os.Args[1]

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}

	// Migration, backup, and restore manage their own database access. In
	// particular, migrate must inspect and snapshot an old schema before opening
	// it read-write; no command may migrate schema as a side effect of startup.
	switch sub {
	case "migrate":
		cmdMigrate(dbPath)
		return
	case "backup":
		cmdBackup(dbPath, os.Args[2:])
		return
	case "restore":
		cmdRestore(dbPath, os.Args[2:])
		return
	case "delete-encode", "audit-duration", "backfill-package-bytes":
		runPackageMaint(os.Args[1:])
		return
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	if err := db.VerifySchema(context.Background(), conn); err != nil {
		log.Fatalf("verify schema: %v", err)
	}

	switch sub {
	case "check":
		cmdCheck(conn, os.Args[2:])
	case "validate-segments":
		cmdValidateSegments(conn, os.Args[2:])
	case "set-group":
		cmdSetGroup(conn, os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: linearcast-maint <maintenance-command> [args]")
	fmt.Fprintln(os.Stderr, "  check [--channel <id>] [--hours N] [--from <iso8601>] [--gap-ms N] [--all]")
	fmt.Fprintln(os.Stderr, "  validate-segments [--channel <id>] [--hours N] [--from <iso8601>] [--all] [--requeue]")
	fmt.Fprintln(os.Stderr, "  migrate")
	fmt.Fprintln(os.Stderr, "  set-group <media-path> <group | ->")
	fmt.Fprintln(os.Stderr, "  backup [--dir <dir>] [--keep N]")
	fmt.Fprintln(os.Stderr, "  restore [--confirm] <snapshot.db>")
	fmt.Fprintln(os.Stderr, "  delete-encode <mediaID> [--profile <profile>] [--force]")
	fmt.Fprintln(os.Stderr, "  audit-duration [--fix]")
	fmt.Fprintln(os.Stderr, "  backfill-package-bytes [--dry-run]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Operator channel/playlist/Plex writes were removed; use the admin API/UI.")
	fmt.Fprintln(os.Stderr, "Env: LINEARCAST_DB")
	os.Exit(1)
}

// splitArgs separates leading positional tokens from flag tokens. Go's stdlib
// flag.Parse stops at the first non-flag token, so callers must put flags
// after positionals for commands that use positional arguments.
func splitArgs(args []string) (positional, flagArgs []string) {
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		positional = append(positional, args[i])
		i++
	}
	return positional, args[i:]
}

func cmdMigrate(dbPath string) {
	ctx := context.Background()
	plan, err := db.PlanMigrations(ctx, dbPath)
	if err != nil {
		log.Fatalf("plan migrations: %v", err)
	}

	var snapshot string
	if plan.NeedsMigration() {
		snapshot, err = createMigrationSnapshot(ctx, dbPath, plan, time.Now())
		if err != nil {
			log.Fatalf("migration snapshot: %v", err)
		}
		log.Printf("migration snapshot: wrote and verified %s (schema v%d)", snapshot, plan.CurrentVersion)
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db for migration: %v", err)
	}
	defer conn.Close()
	if err := db.Migrate(ctx, conn); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if snapshot != "" {
		if _, err := db.PruneBackups(filepath.Dir(snapshot), 14); err != nil {
			log.Printf("migration snapshot: prune: %v", err)
		}
	}
	fmt.Printf("schema ok; version=%d", db.SchemaVersion)
	if snapshot != "" {
		fmt.Printf("; snapshot=%s", snapshot)
	}
	fmt.Println()
}

func createMigrationSnapshot(ctx context.Context, dbPath string, plan db.MigrationPlan, now time.Time) (string, error) {
	if !plan.NeedsMigration() {
		return "", nil
	}
	backupDir := defaultBackupDir(dbPath)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create dir %s: %w", backupDir, err)
	}
	snapshot := filepath.Join(backupDir, db.BackupFileName(now))
	if err := db.Backup(ctx, dbPath, snapshot); err != nil {
		return "", err
	}
	if err := db.VerifyBackupVersion(ctx, snapshot, plan.CurrentVersion); err != nil {
		_ = os.Remove(snapshot)
		return "", fmt.Errorf("verify %s: %w", snapshot, err)
	}
	return snapshot, nil
}

// cmdSetGroup overrides the collection on a single media row. Pass "-" (or
// the literal string "null") to clear the value, exposing it again to the next
// ingest's automatic derivation.
func cmdSetGroup(conn *sql.DB, args []string) {
	positional, _ := splitArgs(args)
	if len(positional) != 2 {
		log.Fatal("set-group requires <media-path> <group|->")
	}
	mediaPath := positional[0]
	group := positional[1]

	pathAbs, err := filepath.Abs(mediaPath)
	if err != nil {
		log.Fatalf("resolve path: %v", err)
	}
	m, err := db.MediaByPath(context.Background(), conn, pathAbs)
	if err != nil {
		log.Fatalf("lookup media: %v", err)
	}
	if m == nil {
		log.Fatalf("media row not found for %q", pathAbs)
	}

	clear := group == "-" || strings.EqualFold(group, "null")
	if clear {
		if err := db.SetMediaSchedulingGroup(context.Background(), conn, m.ID, sql.NullString{}); err != nil {
			log.Fatalf("clear: %v", err)
		}
		fmt.Printf("cleared collection for media=%s (%s)\n", m.ID, pathAbs)
		return
	}
	if err := db.SetMediaSchedulingGroup(context.Background(), conn, m.ID, sql.NullString{String: group, Valid: true}); err != nil {
		log.Fatalf("set: %v", err)
	}
	fmt.Printf("set: media=%s collection=%q\n", m.ID, group)
}

func parseISO8601(s string) (int64, error) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04Z07:00",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("cannot parse %q as ISO8601", s)
}

func timeFromMs(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}
