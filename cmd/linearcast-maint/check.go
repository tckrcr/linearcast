package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/schedcheck"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func cmdCheck(conn *sql.DB, args []string) {
	positional, flagArgs := splitArgs(args)
	if len(positional) != 0 {
		log.Fatal("check does not accept positional arguments")
	}

	fs := flag.NewFlagSet("check", flag.ExitOnError)
	channelID := fs.String("channel", "", "check only this channel")
	hours := fs.Int("hours", 48, "future window to inspect")
	fromStr := fs.String("from", "", "window start time (default: now)")
	gapMs := fs.Int64("gap-ms", 30000, "minimum gap size to report")
	all := fs.Bool("all", false, "include disabled channels")
	if err := fs.Parse(flagArgs); err != nil {
		log.Fatalf("parse flags: %v", err)
	}
	if *hours <= 0 {
		log.Fatal("--hours must be positive")
	}
	if *gapMs < 0 {
		log.Fatal("--gap-ms must be non-negative")
	}

	nowMs := time.Now().UTC().UnixMilli()
	fromMs := scheduler.AlignToGrid(nowMs)
	if strings.TrimSpace(*fromStr) != "" {
		parsed, err := parseISO8601(*fromStr)
		if err != nil {
			log.Fatalf("parse --from: %v", err)
		}
		fromMs = parsed
	}

	opts := schedcheck.Options{
		ChannelID:       strings.TrimSpace(*channelID),
		IncludeDisabled: *all,
		FromMs:          fromMs,
		ToMs:            fromMs + int64(*hours)*3600*1000,
		GapMs:           *gapMs,
	}

	result, err := schedcheck.Check(context.Background(), conn, opts)
	if err != nil {
		log.Fatalf("check schedule integrity: %v", err)
	}

	fmt.Printf("schedule integrity: from=%d (%s UTC) to=%d (%s UTC) gap_ms=%d issues=%d\n",
		opts.FromMs, timeFromMs(opts.FromMs).Format(time.RFC3339),
		opts.ToMs, timeFromMs(opts.ToMs).Format(time.RFC3339),
		opts.GapMs, len(result.Issues))
	for _, issue := range result.Issues {
		fmt.Printf("issue channel=%s kind=%s", issue.ChannelID, issue.Kind)
		if issue.StartMs != 0 || issue.EndMs != 0 {
			fmt.Printf(" start=%d end=%d", issue.StartMs, issue.EndMs)
		}
		if issue.MediaID != "" {
			fmt.Printf(" media=%s", issue.MediaID)
		}
		fmt.Printf(" %s\n", issue.Message)
	}
	if len(result.Issues) > 0 {
		os.Exit(2)
	}
}
