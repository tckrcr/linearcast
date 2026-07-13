// live-proxy-smoke-seed seeds a disposable database for scripts/live-proxy-smoke.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

const (
	externalChannelID = "smoke-external"
	smokeDurationMs   = 60_000
)

func main() {
	log.SetFlags(0)
	externalURL := flag.String("external-url", "", "external HLS master playlist URL")
	flag.Parse()

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}
	if *externalURL == "" {
		log.Fatal("--external-url is required")
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()
	if err := db.ApplySchema(ctx, conn); err != nil {
		log.Fatalf("apply schema: %v", err)
	}
	if err := db.VerifySchema(ctx, conn); err != nil {
		log.Fatalf("verify schema: %v", err)
	}

	now := time.Now().UTC().UnixMilli()
	created := now

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, media_kind, upstream_hls_url, prefill_mode)
		VALUES (?, 'Smoke External HLS', '', 'alphabetical', 1, ?, 'packaged', 'music', ?, 'eager')
		ON CONFLICT(id) DO UPDATE SET
			display_name = excluded.display_name,
			enabled = excluded.enabled,
			playback_mode = excluded.playback_mode,
			media_kind = excluded.media_kind,
			upstream_hls_url = excluded.upstream_hls_url,
			prefill_mode = excluded.prefill_mode`, externalChannelID, created, *externalURL); err != nil {
		log.Fatalf("seed external channel: %v", err)
	}

	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("seeded external=%s start_ms=%d\n", externalChannelID, now)
}
