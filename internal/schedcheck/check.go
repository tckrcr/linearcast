// Package schedcheck audits channel schedules for structural problems: gaps,
// overlaps, missing media, out-of-bounds offsets, grid-alignment violations,
// and missing ready packages. It is the shared core called by both the admin
// API handler and the linearcast-maint CLI.
package schedcheck

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

// Options controls the scope of a schedule check.
type Options struct {
	// ChannelID restricts the check to a single channel. Empty means all channels.
	ChannelID string
	// IncludeDisabled includes disabled channels when ChannelID is empty.
	IncludeDisabled bool
	// FromMs and ToMs are the check window in Unix milliseconds.
	FromMs int64
	ToMs   int64
	// GapMs is the minimum gap length to report. Zero reports all gaps.
	GapMs int64
}

// IssueKind classifies a schedule problem.
type IssueKind string

const (
	KindGap              IssueKind = "gap"
	KindOverlap          IssueKind = "overlap"
	KindInvalidAlignment IssueKind = "invalid_alignment"
	KindMissingMedia     IssueKind = "missing_media"
	KindMediaBounds      IssueKind = "media_bounds"
	KindPackageNotReady  IssueKind = "package_not_ready"
	KindNoSchedule       IssueKind = "no_schedule"
)

// Issue describes a single schedule problem.
type Issue struct {
	ChannelID string    `json:"channelId"`
	Kind      IssueKind `json:"kind"`
	StartMs   int64     `json:"startMs,omitempty"`
	EndMs     int64     `json:"endMs,omitempty"`
	MediaID   string    `json:"mediaId,omitempty"`
	Message   string    `json:"message"`
}

// Result holds the output of a Check call.
type Result struct {
	ChannelsChecked int
	Issues          []Issue
}

// Check audits every channel in scope and returns a Result. It is read-only:
// it never modifies the database.
func Check(ctx context.Context, conn *sql.DB, opts Options) (Result, error) {
	if opts.GapMs < 0 {
		return Result{}, fmt.Errorf("gap_ms must be non-negative")
	}
	if opts.ToMs <= opts.FromMs {
		return Result{}, fmt.Errorf("invalid window: to_ms must be greater than from_ms")
	}

	channels, err := resolveChannels(ctx, conn, opts)
	if err != nil {
		return Result{}, err
	}

	res := Result{ChannelsChecked: len(channels)}
	for _, ch := range channels {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		chIssues, err := checkChannel(ctx, conn, ch, opts)
		if err != nil {
			return res, err
		}
		res.Issues = append(res.Issues, chIssues...)
	}
	return res, nil
}

func resolveChannels(ctx context.Context, conn *sql.DB, opts Options) ([]db.Channel, error) {
	if opts.ChannelID != "" {
		ch, err := db.ChannelByID(ctx, conn, opts.ChannelID)
		if err != nil {
			return nil, fmt.Errorf("lookup channel %s: %w", opts.ChannelID, err)
		}
		if ch == nil {
			return nil, fmt.Errorf("channel %q not found", opts.ChannelID)
		}
		return []db.Channel{*ch}, nil
	}
	if opts.IncludeDisabled {
		return db.AllChannelsOrderedByDisplayName(ctx, conn)
	}
	return db.EnabledChannels(ctx, conn)
}

type scheduleRow struct {
	StartMs       int64
	MediaID       string
	OffsetMs      int64
	DurationMs    int64
	MediaDuration sql.NullInt64
	ReadyPackage  sql.NullString
}

func checkChannel(ctx context.Context, conn *sql.DB, ch db.Channel, opts Options) ([]Issue, error) {
	profile := db.DefaultPackageProfile
	if strings.TrimSpace(ch.RequiredPackageProfile) != "" {
		profile = strings.TrimSpace(ch.RequiredPackageProfile)
	}
	// On-demand channels schedule from codec-eligible media
	// without ready linearcast packages, so a missing ready package is expected,
	// not a problem. Only eager packaged channels get the package-not-ready check.
	requireReadyPackages := ch.RequiresReadyPackages()

	rows, err := conn.QueryContext(ctx, `
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

	var issues []Issue
	var prev *scheduleRow
	count := 0

	for rows.Next() {
		var row scheduleRow
		if err := rows.Scan(
			&row.StartMs, &row.MediaID, &row.OffsetMs, &row.DurationMs,
			&row.MediaDuration, &row.ReadyPackage,
		); err != nil {
			return nil, fmt.Errorf("scan schedule %s: %w", ch.ID, err)
		}
		count++
		rowEnd := row.StartMs + row.DurationMs

		if count == 1 && row.StartMs > opts.FromMs && row.StartMs-opts.FromMs > opts.GapMs {
			issues = append(issues, Issue{
				ChannelID: ch.ID,
				Kind:      KindGap,
				StartMs:   opts.FromMs,
				EndMs:     row.StartMs,
				Message:   fmt.Sprintf("initial schedule gap is %dms", row.StartMs-opts.FromMs),
			})
		}
		if prev != nil {
			prevEnd := prev.StartMs + prev.DurationMs
			switch {
			case row.StartMs < prevEnd:
				issues = append(issues, Issue{
					ChannelID: ch.ID,
					Kind:      KindOverlap,
					StartMs:   row.StartMs,
					EndMs:     prevEnd,
					MediaID:   row.MediaID,
					Message:   fmt.Sprintf("entry overlaps previous entry ending at %d", prevEnd),
				})
			case row.StartMs-prevEnd > opts.GapMs:
				issues = append(issues, Issue{
					ChannelID: ch.ID,
					Kind:      KindGap,
					StartMs:   prevEnd,
					EndMs:     row.StartMs,
					Message:   fmt.Sprintf("schedule gap is %dms", row.StartMs-prevEnd),
				})
			}
		}

		if row.StartMs%6000 != 0 || row.DurationMs%6000 != 0 || row.DurationMs <= 0 || row.OffsetMs < 0 {
			issues = append(issues, Issue{
				ChannelID: ch.ID,
				Kind:      KindInvalidAlignment,
				StartMs:   row.StartMs,
				EndMs:     rowEnd,
				MediaID:   row.MediaID,
				Message:   fmt.Sprintf("start_ms=%d duration_ms=%d offset_ms=%d violates schedule grid", row.StartMs, row.DurationMs, row.OffsetMs),
			})
		}
		if !row.MediaDuration.Valid {
			issues = append(issues, Issue{
				ChannelID: ch.ID,
				Kind:      KindMissingMedia,
				StartMs:   row.StartMs,
				EndMs:     rowEnd,
				MediaID:   row.MediaID,
				Message:   "schedule entry references a missing media row",
			})
		} else if row.OffsetMs+row.DurationMs > row.MediaDuration.Int64 {
			issues = append(issues, Issue{
				ChannelID: ch.ID,
				Kind:      KindMediaBounds,
				StartMs:   row.StartMs,
				EndMs:     rowEnd,
				MediaID:   row.MediaID,
				Message:   fmt.Sprintf("entry offset+duration=%d exceeds media duration=%d", row.OffsetMs+row.DurationMs, row.MediaDuration.Int64),
			})
		}
		if requireReadyPackages && !row.ReadyPackage.Valid {
			issues = append(issues, Issue{
				ChannelID: ch.ID,
				Kind:      KindPackageNotReady,
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
		issues = append(issues, Issue{
			ChannelID: ch.ID,
			Kind:      KindNoSchedule,
			StartMs:   opts.FromMs,
			EndMs:     opts.ToMs,
			Message:   "enabled channel has no schedule entries in the check window",
		})
	}
	return issues, nil
}
