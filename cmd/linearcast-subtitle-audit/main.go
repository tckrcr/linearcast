// cmd/linearcast-subtitle-audit reports subtitle coverage across the library for
// each configured preferred language.
//
// Env:
//
//	LINEARCAST_DB  (required) path to linearcast.db
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

func main() {
	log.SetFlags(0)

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: linearcast-subtitle-audit [flags]")
		fmt.Fprintln(os.Stderr, "  Env: LINEARCAST_DB")
		flag.PrintDefaults()
	}
	missingOnly := flag.Bool("missing", false, "only list media with at least one missing preferred language")
	flag.Parse()

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}

	conn, err := db.OpenReadOnly(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	prefs, err := db.GetSubtitleLanguagePreference(context.Background(), conn)
	if err != nil {
		log.Fatalf("read preferences: %v", err)
	}
	fmt.Printf("subtitle audit — preferences: %v\n\n", prefs)

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
	sort.Strings(mediaIDs)

	type langStats struct {
		satisfied int
		missing   int
	}
	stats := make(map[string]*langStats, len(prefs))
	for _, lang := range prefs {
		stats[lang] = &langStats{}
	}

	type mediaReport struct {
		id      string
		path    string
		missing []string
	}
	var reports []mediaReport

	for _, mediaID := range mediaIDs {
		media, err := db.MediaByID(context.Background(), conn, mediaID)
		if err != nil || media == nil {
			continue
		}

		var missingLangs []string
		for _, lang := range prefs {
			has, _ := db.HasSubtitleTrackForLang(context.Background(), conn, mediaID, lang)
			if has {
				stats[lang].satisfied++
				continue
			}
			stats[lang].missing++
			missingLangs = append(missingLangs, lang)
		}

		if len(missingLangs) > 0 {
			reports = append(reports, mediaReport{
				id:      mediaID,
				path:    media.Path,
				missing: missingLangs,
			})
		}
	}

	fmt.Printf("Total packaged media: %d\n\n", len(mediaIDs))
	fmt.Printf("Coverage per language:\n")
	for _, lang := range prefs {
		s := stats[lang]
		fmt.Printf("  %s: %d satisfied, %d missing\n", lang, s.satisfied, s.missing)
	}

	if !*missingOnly {
		return
	}

	if len(reports) == 0 {
		fmt.Println("\nAll preferred languages covered.")
		return
	}

	fmt.Printf("\nMedia with missing preferred subtitle languages (%d items):\n", len(reports))
	for _, r := range reports {
		fmt.Printf("  [%s] missing=[%s]  %s\n", r.id, strings.Join(r.missing, ","), r.path)
	}
}
