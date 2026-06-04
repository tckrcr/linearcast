// cmd/linearcast-subtitle-extract backfills WebVTT subtitle sidecars for media
// that already has a ready package, without re-encoding video. Checks per-language
// whether a track already exists before extracting.
//
// Env:
//
//	LINEARCAST_DB            (required) path to linearcast.db
//	LINEARCAST_PACKAGE_ROOT  package output root (or CACHE_DIR/packages)
//	CACHE_DIR                fallback for output root
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: linearcast-subtitle-extract [flags]")
		fmt.Fprintln(os.Stderr, "  Env: LINEARCAST_DB, LINEARCAST_PACKAGE_ROOT (or CACHE_DIR)")
		flag.PrintDefaults()
	}
	force := flag.Bool("force", false, "re-extract embedded subs even if tracks already exist")
	flag.Parse()

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}
	outputRoot := os.Getenv("LINEARCAST_PACKAGE_ROOT")
	if outputRoot == "" {
		cacheDir := os.Getenv("CACHE_DIR")
		if cacheDir == "" {
			log.Fatal("LINEARCAST_PACKAGE_ROOT or CACHE_DIR is required")
		}
		outputRoot = cacheDir + "/packages"
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	if err := db.ApplySchema(context.Background(), conn); err != nil {
		log.Fatalf("apply schema: %v", err)
	}

	prefs, err := db.GetSubtitleLanguagePreference(context.Background(), conn)
	if err != nil {
		log.Fatalf("read subtitle preferences: %v", err)
	}
	if len(prefs) == 0 {
		log.Fatal("no subtitle language preferences configured")
	}
	log.Printf("subtitle preferences: %v", prefs)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	packages, err := db.ReadyMediaPackages(context.Background(), conn)
	if err != nil {
		log.Fatalf("query packages: %v", err)
	}

	seen := make(map[string]bool)
	var mediaIDs []string
	for _, p := range packages {
		if !seen[p.MediaID] {
			seen[p.MediaID] = true
			mediaIDs = append(mediaIDs, p.MediaID)
		}
	}
	log.Printf("found %d packaged media items", len(mediaIDs))

	var skipped, extracted, failed int

	for _, mediaID := range mediaIDs {
		if ctx.Err() != nil {
			break
		}

		result, err := packager.FetchSubtitlesForMedia(ctx, conn, mediaID, "", outputRoot, prefs)
		if err != nil {
			log.Printf("ERROR media=%s: %v", mediaID, err)
			failed++
			continue
		}
		if result.Skipped && !*force {
			skipped++
			continue
		}
		if result.EmbeddedExtracted > 0 {
			extracted++
		}
	}

	fmt.Printf("\nsubtitle backfill: extracted=%d skipped=%d failed=%d\n", extracted, skipped, failed)
}
