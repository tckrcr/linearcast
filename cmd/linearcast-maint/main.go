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
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if len(os.Args) < 2 {
		usage()
	}
	sub := os.Args[1]

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	if err := db.ApplySchema(context.Background(), conn); err != nil {
		log.Fatalf("apply schema: %v", err)
	}
	if err := db.VerifySchema(context.Background(), conn); err != nil {
		log.Fatalf("verify schema: %v", err)
	}

	switch sub {
	case "check":
		cmdCheck(conn, os.Args[2:])
	case "migrate":
		cmdMigrate(conn)
	case "set-group":
		cmdSetGroup(conn, os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: linearcast-maint <maintenance-command> [args]")
	fmt.Fprintln(os.Stderr, "  check [--channel <id>] [--hours N] [--from <iso8601>] [--gap-ms N] [--all]")
	fmt.Fprintln(os.Stderr, "  migrate")
	fmt.Fprintln(os.Stderr, "  set-group <media-path> <group | ->")
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

func cmdMigrate(conn *sql.DB) {
	n, err := db.NormalizeChannelsToPackaged(context.Background(), conn, db.DefaultPackageProfile)
	if err != nil {
		log.Fatalf("normalize playback policy: %v", err)
	}
	fmt.Printf("schema ok; normalized_channels=%d\n", n)
}

// cmdSetGroup overrides scheduling_group on a single media row. Pass "-"
// (or the literal string "null") to clear the value, exposing it again to
// the next ingest's automatic derivation.
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
		fmt.Printf("cleared scheduling_group for media=%s (%s)\n", m.ID, pathAbs)
		return
	}
	if err := db.SetMediaSchedulingGroup(context.Background(), conn, m.ID, sql.NullString{String: group, Valid: true}); err != nil {
		log.Fatalf("set: %v", err)
	}
	fmt.Printf("set: media=%s scheduling_group=%q\n", m.ID, group)
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
