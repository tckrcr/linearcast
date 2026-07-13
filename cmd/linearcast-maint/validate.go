package main

// validate.go implements `linearcast-maint validate-segments`: a pre-flight that
// confirms the ready packages backing the upcoming schedule actually present
// decodable segments. `check` (schedcheck) audits schedule structure and flags
// missing/not-ready packages; this command takes the packages that ARE ready and
// hands their bytes to ffprobe (see packager.ProbePackageDecodable) so a package
// whose init.mp4/seg*.m4s no longer combine into a decodable stream is caught
// before it airs. Report-only by default; --requeue marks failures pending for
// re-encode via the same repair path the integrity sweep uses.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func cmdValidateSegments(conn *sql.DB, args []string) {
	positional, flagArgs := splitArgs(args)
	if len(positional) != 0 {
		log.Fatal("validate-segments does not accept positional arguments")
	}

	fs := flag.NewFlagSet("validate-segments", flag.ExitOnError)
	channelID := fs.String("channel", "", "validate only this channel")
	hours := fs.Int("hours", 48, "future window to inspect")
	fromStr := fs.String("from", "", "window start time (default: now)")
	all := fs.Bool("all", false, "include disabled channels")
	requeue := fs.Bool("requeue", false, "mark failing packages pending for re-encode")
	if err := fs.Parse(flagArgs); err != nil {
		log.Fatalf("parse flags: %v", err)
	}
	if *hours <= 0 {
		log.Fatal("--hours must be positive")
	}

	ctx := context.Background()
	nowMs := time.Now().UTC().UnixMilli()
	fromMs := scheduler.AlignToGrid(nowMs)
	if strings.TrimSpace(*fromStr) != "" {
		parsed, err := parseISO8601(*fromStr)
		if err != nil {
			log.Fatalf("parse --from: %v", err)
		}
		fromMs = parsed
	}
	toMs := fromMs + int64(*hours)*3600*1000

	channels, err := resolveValidateChannels(ctx, conn, strings.TrimSpace(*channelID), *all)
	if err != nil {
		log.Fatalf("resolve channels: %v", err)
	}

	var probed, failures, requeued int
	seenPackage := map[string]bool{} // dedup across channels by package ID

	for _, ch := range channels {
		// On-demand channels schedule without ready packages, so
		// there is nothing to probe — same rule schedcheck applies.
		if !ch.RequiresReadyPackages() {
			continue
		}
		profileName := strings.TrimSpace(ch.RequiredPackageProfile)
		if profileName == "" {
			profileName = db.DefaultPackageProfile
		}
		profile, err := db.GetPackageProfile(ctx, conn, profileName)
		if err != nil || profile == nil {
			log.Printf("channel=%s: skip, package profile %q unavailable: %v", ch.ID, profileName, err)
			continue
		}

		entries, err := db.ScheduleWindow(ctx, conn, ch.ID, fromMs, toMs)
		if err != nil {
			log.Fatalf("schedule window %s: %v", ch.ID, err)
		}
		seenMedia := map[string]bool{}
		for _, e := range entries {
			if seenMedia[e.MediaID] {
				continue
			}
			seenMedia[e.MediaID] = true

			pkg, err := db.ReadyMediaPackage(ctx, conn, e.MediaID, profileName)
			if err != nil {
				log.Fatalf("lookup ready package media=%s profile=%s: %v", e.MediaID, profileName, err)
			}
			if pkg == nil {
				continue // package-not-ready is `check`'s job, not this one's
			}
			if seenPackage[pkg.ID] {
				continue
			}
			seenPackage[pkg.ID] = true

			rep := packager.ProbePackageDecodable(ctx, *pkg, *profile)
			probed++
			if rep.OK {
				continue
			}
			failures++
			fmt.Printf("probe-fail channel=%s media=%s profile=%s package=%s reason=%q\n",
				ch.ID, rep.MediaID, rep.Profile, rep.PackageID, rep.Reason)
			if *requeue {
				reason := "segment probe failed: " + rep.Reason
				changed, err := db.MarkReadyPackagePendingForReencode(ctx, conn, pkg.ID, nowMs, reason)
				if err != nil {
					log.Fatalf("requeue package %s: %v", pkg.ID, err)
				}
				if changed {
					requeued++
				}
			}
		}
	}

	summary := fmt.Sprintf("segment validation: from=%d (%s UTC) to=%d (%s UTC) probed=%d failures=%d",
		fromMs, timeFromMs(fromMs).Format(time.RFC3339),
		toMs, timeFromMs(toMs).Format(time.RFC3339),
		probed, failures)
	if *requeue {
		summary += fmt.Sprintf(" requeued=%d", requeued)
	}
	fmt.Println(summary)
	if failures > 0 {
		os.Exit(2)
	}
}

func resolveValidateChannels(ctx context.Context, conn *sql.DB, channelID string, all bool) ([]db.Channel, error) {
	if channelID != "" {
		ch, err := db.ChannelByID(ctx, conn, channelID)
		if err != nil {
			return nil, fmt.Errorf("lookup channel %s: %w", channelID, err)
		}
		if ch == nil {
			return nil, fmt.Errorf("channel %q not found", channelID)
		}
		return []db.Channel{*ch}, nil
	}
	if all {
		return db.AllChannelsOrderedByDisplayName(ctx, conn)
	}
	return db.EnabledChannels(ctx, conn)
}
