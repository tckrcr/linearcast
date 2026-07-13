// cmd/linearcast-ingest probes media files under MEDIA_DIR and inserts/updates
// rows in the linearcast SQLite database.
//
// Lives next to (not on top of) cmd/ingest, which belongs to the main
// linearcast service.
//
// Usage:
//
//	LINEARCAST_DB=/path/to/linearcast.db MEDIA_DIR=/data/media/.../show linearcast-ingest
//
// Pass -init the first time to apply the embedded schema.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/lcingest"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: linearcast-ingest [-dir <path>] [-music] [-retitle]")
		fmt.Fprintln(os.Stderr, "  Env: LINEARCAST_DB, MEDIA_DIR")
		flag.PrintDefaults()
	}
	// Kept for backwards compatibility; schema is now always applied on startup.
	_ = flag.Bool("init", false, "deprecated no-op (schema is applied automatically)")
	dirFlag := flag.String("dir", "", "media directory (overrides MEDIA_DIR)")
	musicFlag := flag.Bool("music", false, "ingest audio files (.flac, .mp3, .wav, …) instead of video files")
	retitleFlag := flag.Bool("retitle", false, "re-derive titles for all existing media rows from their stored paths and exit (no media scan, no ffprobe)")
	flag.Parse()

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *retitleFlag {
		scanned, updated, err := lcingest.RetitleAll(ctx, conn, log.Default())
		if err != nil {
			log.Fatalf("retitle: %v", err)
		}
		fmt.Printf("\nretitle summary: scanned=%d updated=%d\n", scanned, updated)
		return
	}

	mediaDir := *dirFlag
	if mediaDir == "" {
		mediaDir = os.Getenv("MEDIA_DIR")
	}
	if mediaDir == "" {
		log.Fatal("MEDIA_DIR is required (or pass -dir)")
	}

	var res lcingest.Result
	if *musicFlag {
		res, err = lcingest.IngestMusic(ctx, conn, mediaDir, log.Default())
	} else {
		res, err = lcingest.Ingest(ctx, conn, mediaDir, log.Default())
	}
	if err != nil {
		log.Fatalf("ingest: %v", err)
	}

	fmt.Printf("\ningest summary: total=%d passed=%d failed=%d\n", res.Total, res.Passed, res.Failed)
	reasons := make([]string, 0, len(res.FailureReasons))
	for reason := range res.FailureReasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		count := res.FailureReasons[reason]
		fmt.Printf("  - %s: %d\n", reason, count)
	}
}
