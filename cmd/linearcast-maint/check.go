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

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

const defaultCheckGapMs int64 = 30000

type integrityOptions struct {
	ChannelID       string
	IncludeDisabled bool
	FromMs          int64
	ToMs            int64
	GapMs           int64
}

type integrityIssue struct {
	ChannelID string
	Kind      string
	StartMs   int64
	EndMs     int64
	MediaID   string
	Message   string
}

func cmdCheck(conn *sql.DB, args []string) {
	positional, flagArgs := splitArgs(args)
	if len(positional) != 0 {
		log.Fatal("check does not accept positional arguments")
	}

	fs := flag.NewFlagSet("check", flag.ExitOnError)
	channelID := fs.String("channel", "", "check only this channel")
	hours := fs.Int("hours", 48, "future window to inspect")
	fromStr := fs.String("from", "", "window start time (default: now)")
	gapMs := fs.Int64("gap-ms", defaultCheckGapMs, "minimum gap size to report")
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
	fromMs := scheduler.Align6s(nowMs)
	if strings.TrimSpace(*fromStr) != "" {
		parsed, err := parseISO8601(*fromStr)
		if err != nil {
			log.Fatalf("parse --from: %v", err)
		}
		fromMs = parsed
	}
	opts := integrityOptions{
		ChannelID:       strings.TrimSpace(*channelID),
		IncludeDisabled: *all,
		FromMs:          fromMs,
		ToMs:            fromMs + int64(*hours)*3600*1000,
		GapMs:           *gapMs,
	}

	issues, err := checkScheduleIntegrity(conn, opts)
	if err != nil {
		log.Fatalf("check schedule integrity: %v", err)
	}
	fmt.Printf("schedule integrity: from=%d (%s UTC) to=%d (%s UTC) gap_ms=%d issues=%d\n",
		opts.FromMs, timeFromMs(opts.FromMs).Format(time.RFC3339),
		opts.ToMs, timeFromMs(opts.ToMs).Format(time.RFC3339),
		opts.GapMs, len(issues))
	for _, issue := range issues {
		fmt.Printf("issue channel=%s kind=%s", issue.ChannelID, issue.Kind)
		if issue.StartMs != 0 || issue.EndMs != 0 {
			fmt.Printf(" start=%d end=%d", issue.StartMs, issue.EndMs)
		}
		if issue.MediaID != "" {
			fmt.Printf(" media=%s", issue.MediaID)
		}
		fmt.Printf(" %s\n", issue.Message)
	}
	if len(issues) > 0 {
		os.Exit(2)
	}
}

func checkScheduleIntegrity(conn *sql.DB, opts integrityOptions) ([]integrityIssue, error) {
	if opts.GapMs < 0 {
		return nil, fmt.Errorf("gap_ms must be non-negative")
	}
	if opts.ToMs <= opts.FromMs {
		return nil, fmt.Errorf("invalid window: to_ms must be greater than from_ms")
	}

	channels, err := integrityChannels(conn, opts)
	if err != nil {
		return nil, err
	}

	var issues []integrityIssue
	for _, ch := range channels {
		channelIssues, err := checkChannelScheduleIntegrity(conn, ch, opts)
		if err != nil {
			return nil, err
		}
		issues = append(issues, channelIssues...)
	}
	return issues, nil
}

func integrityChannels(conn *sql.DB, opts integrityOptions) ([]db.Channel, error) {
	if opts.ChannelID != "" {
		ch, err := db.ChannelByID(context.Background(), conn, opts.ChannelID)
		if err != nil {
			return nil, fmt.Errorf("lookup channel %s: %w", opts.ChannelID, err)
		}
		if ch == nil {
			return nil, fmt.Errorf("channel %q not found", opts.ChannelID)
		}
		return []db.Channel{*ch}, nil
	}
	if opts.IncludeDisabled {
		return db.AllChannelsOrderedByDisplayName(context.Background(), conn)
	}
	return db.EnabledChannels(context.Background(), conn)
}

type integrityScheduleRow struct {
	StartMs       int64
	MediaID       string
	OffsetMs      int64
	DurationMs    int64
	MediaDuration sql.NullInt64
	ReadyPackage  sql.NullString
}

func checkChannelScheduleIntegrity(conn *sql.DB, ch db.Channel, opts integrityOptions) ([]integrityIssue, error) {
	profile := db.DefaultPackageProfile
	if strings.TrimSpace(ch.RequiredPackageProfile) != "" {
		profile = strings.TrimSpace(ch.RequiredPackageProfile)
	}

	rows, err := conn.Query(`
		SELECT se.start_ms, se.media_id, se.offset_ms, se.duration_ms,
		       m.duration_ms,
		       p.id
		FROM schedule_entries se
		LEFT JOIN media m ON m.id = se.media_id
		LEFT JOIN media_packages p
		  ON p.media_id = se.media_id
		 AND p.rendition_profile = ?
		 AND p.status = ?
		 AND p.packaged_duration_ms IS NOT NULL
		WHERE se.channel_id = ?
		  AND se.start_ms + se.duration_ms > ?
		  AND se.start_ms < ?
		ORDER BY se.start_ms, se.media_id`,
		profile, string(db.PackageStatusReady), ch.ID, opts.FromMs, opts.ToMs)
	if err != nil {
		return nil, fmt.Errorf("query schedule %s: %w", ch.ID, err)
	}
	defer rows.Close()

	var issues []integrityIssue
	var prev *integrityScheduleRow
	count := 0
	for rows.Next() {
		var row integrityScheduleRow
		if err := rows.Scan(
			&row.StartMs, &row.MediaID, &row.OffsetMs, &row.DurationMs,
			&row.MediaDuration, &row.ReadyPackage,
		); err != nil {
			return nil, fmt.Errorf("scan schedule %s: %w", ch.ID, err)
		}
		count++
		rowEnd := row.StartMs + row.DurationMs

		if count == 1 && row.StartMs > opts.FromMs && row.StartMs-opts.FromMs > opts.GapMs {
			issues = append(issues, integrityIssue{
				ChannelID: ch.ID,
				Kind:      "gap",
				StartMs:   opts.FromMs,
				EndMs:     row.StartMs,
				Message:   fmt.Sprintf("initial schedule gap is %dms", row.StartMs-opts.FromMs),
			})
		}
		if prev != nil {
			prevEnd := prev.StartMs + prev.DurationMs
			switch {
			case row.StartMs < prevEnd:
				issues = append(issues, integrityIssue{
					ChannelID: ch.ID,
					Kind:      "overlap",
					StartMs:   row.StartMs,
					EndMs:     prevEnd,
					MediaID:   row.MediaID,
					Message:   fmt.Sprintf("entry overlaps previous entry ending at %d", prevEnd),
				})
			case row.StartMs-prevEnd > opts.GapMs:
				issues = append(issues, integrityIssue{
					ChannelID: ch.ID,
					Kind:      "gap",
					StartMs:   prevEnd,
					EndMs:     row.StartMs,
					Message:   fmt.Sprintf("schedule gap is %dms", row.StartMs-prevEnd),
				})
			}
		}

		if row.StartMs%6000 != 0 || row.DurationMs%6000 != 0 || row.DurationMs <= 0 || row.OffsetMs < 0 {
			issues = append(issues, integrityIssue{
				ChannelID: ch.ID,
				Kind:      "invalid_alignment",
				StartMs:   row.StartMs,
				EndMs:     rowEnd,
				MediaID:   row.MediaID,
				Message:   fmt.Sprintf("start_ms=%d duration_ms=%d offset_ms=%d violates schedule grid", row.StartMs, row.DurationMs, row.OffsetMs),
			})
		}
		if !row.MediaDuration.Valid {
			issues = append(issues, integrityIssue{
				ChannelID: ch.ID,
				Kind:      "missing_media",
				StartMs:   row.StartMs,
				EndMs:     rowEnd,
				MediaID:   row.MediaID,
				Message:   "schedule entry references a missing media row",
			})
		} else if row.OffsetMs+row.DurationMs > row.MediaDuration.Int64 {
			issues = append(issues, integrityIssue{
				ChannelID: ch.ID,
				Kind:      "media_bounds",
				StartMs:   row.StartMs,
				EndMs:     rowEnd,
				MediaID:   row.MediaID,
				Message:   fmt.Sprintf("entry offset+duration=%d exceeds media duration=%d", row.OffsetMs+row.DurationMs, row.MediaDuration.Int64),
			})
		}
		if !row.ReadyPackage.Valid {
			issues = append(issues, integrityIssue{
				ChannelID: ch.ID,
				Kind:      "package_not_ready",
				StartMs:   row.StartMs,
				EndMs:     rowEnd,
				MediaID:   row.MediaID,
				Message:   fmt.Sprintf("no ready package for profile %s", profile),
			})
		}

		copied := row
		prev = &copied
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schedule %s: %w", ch.ID, err)
	}
	if count == 0 && ch.Enabled {
		issues = append(issues, integrityIssue{
			ChannelID: ch.ID,
			Kind:      "no_schedule",
			StartMs:   opts.FromMs,
			EndMs:     opts.ToMs,
			Message:   "enabled channel has no schedule entries in the check window",
		})
	}
	return issues, nil
}
