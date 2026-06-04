package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ScheduleChainIssueKind classifies a schedule linked-list invariant violation.
// The chain metadata is introduced ahead of the read-path swap, so the
// validator gives us a way to inspect drift once the new column is backfilled.
type ScheduleChainIssueKind string

const (
	ScheduleChainIssueNoHead             ScheduleChainIssueKind = "no_head"
	ScheduleChainIssueMultipleHeads      ScheduleChainIssueKind = "multiple_heads"
	ScheduleChainIssueMultipleSuccessors ScheduleChainIssueKind = "multiple_successors"
	ScheduleChainIssueSelfAnchor         ScheduleChainIssueKind = "self_anchor"
	ScheduleChainIssueOrphanAnchor       ScheduleChainIssueKind = "orphan_anchor"
	ScheduleChainIssueCycle              ScheduleChainIssueKind = "cycle"
	ScheduleChainIssueUnreachable        ScheduleChainIssueKind = "unreachable"
)

// ScheduleChainIssue describes one invariant violation on a single channel.
// EntryIDs lists the rows directly implicated; Detail is a short note.
type ScheduleChainIssue struct {
	ChannelID string                 `json:"channelId"`
	Kind      ScheduleChainIssueKind `json:"kind"`
	EntryIDs  []string               `json:"entryIds,omitempty"`
	Detail    string                 `json:"detail"`
}

// ValidateScheduleEntryChains walks every channel's schedule_entries chain and
// reports invariant violations. The new anchor metadata is introduced before
// the read-path swap, so this check is primarily for backfills, manual SQL
// edits, and future write-path regressions.
func ValidateScheduleEntryChains(ctx context.Context, conn *sql.DB) ([]ScheduleChainIssue, error) {
	channelIDs, err := queryRows(ctx, conn, scanString, `SELECT id FROM channels ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}

	var out []ScheduleChainIssue
	for _, channelID := range channelIDs {
		issues, err := validateScheduleEntryChain(ctx, conn, channelID)
		if err != nil {
			return nil, fmt.Errorf("channel %s: %w", channelID, err)
		}
		out = append(out, issues...)
	}
	return out, nil
}

func validateScheduleEntryChain(ctx context.Context, conn *sql.DB, channelID string) ([]ScheduleChainIssue, error) {
	all, err := queryRows(ctx, conn, scanScheduleChainAnchorRow, `SELECT id, anchor_schedule_entry_id FROM schedule_entries WHERE channel_id = ?`,
		channelID,
	)
	if err != nil {
		return nil, err
	}
	member := map[string]bool{}
	for _, r := range all {
		member[r.id] = true
	}
	if len(all) == 0 {
		return nil, nil
	}

	var issues []ScheduleChainIssue
	visited := map[string]bool{}
	var heads []string
	successors := map[string][]string{}
	for _, r := range all {
		if !r.anchor.Valid {
			heads = append(heads, r.id)
			continue
		}
		if r.anchor.String == r.id {
			issues = append(issues, ScheduleChainIssue{
				ChannelID: channelID,
				Kind:      ScheduleChainIssueSelfAnchor,
				EntryIDs:  []string{r.id},
				Detail:    "row anchors to itself",
			})
			visited[r.id] = true
			continue
		}
		if !member[r.anchor.String] {
			issues = append(issues, ScheduleChainIssue{
				ChannelID: channelID,
				Kind:      ScheduleChainIssueOrphanAnchor,
				EntryIDs:  []string{r.id},
				Detail:    fmt.Sprintf("anchor_schedule_entry_id %q is not a member of this channel", r.anchor.String),
			})
			visited[r.id] = true
			continue
		}
		successors[r.anchor.String] = append(successors[r.anchor.String], r.id)
	}

	switch {
	case len(heads) == 0:
		issues = append(issues, ScheduleChainIssue{
			ChannelID: channelID,
			Kind:      ScheduleChainIssueNoHead,
			Detail:    fmt.Sprintf("%d rows but no head (no row with anchor_schedule_entry_id IS NULL)", len(all)),
		})
	case len(heads) > 1:
		sort.Strings(heads)
		issues = append(issues, ScheduleChainIssue{
			ChannelID: channelID,
			Kind:      ScheduleChainIssueMultipleHeads,
			EntryIDs:  heads,
			Detail:    fmt.Sprintf("%d rows have anchor_schedule_entry_id IS NULL", len(heads)),
		})
	}

	for anchor, kids := range successors {
		if len(kids) > 1 {
			sorted := append([]string(nil), kids...)
			sort.Strings(sorted)
			issues = append(issues, ScheduleChainIssue{
				ChannelID: channelID,
				Kind:      ScheduleChainIssueMultipleSuccessors,
				EntryIDs:  sorted,
				Detail:    fmt.Sprintf("multiple rows anchor to %q", anchor),
			})
		}
	}

	walk := func(start string) {
		cur := start
		localPath := map[string]bool{}
		for {
			if localPath[cur] {
				issues = append(issues, ScheduleChainIssue{
					ChannelID: channelID,
					Kind:      ScheduleChainIssueCycle,
					EntryIDs:  []string{cur},
					Detail:    "chain walk re-encountered an already-visited row",
				})
				return
			}
			if visited[cur] {
				return
			}
			localPath[cur] = true
			visited[cur] = true
			kids := successors[cur]
			if len(kids) == 0 {
				return
			}
			cur = kids[0]
		}
	}
	for _, head := range heads {
		walk(head)
	}
	for _, r := range all {
		if !visited[r.id] {
			walk(r.id)
		}
	}

	var unreachable []string
	for _, r := range all {
		if !visited[r.id] {
			unreachable = append(unreachable, r.id)
		}
	}
	if len(unreachable) > 0 {
		sort.Strings(unreachable)
		issues = append(issues, ScheduleChainIssue{
			ChannelID: channelID,
			Kind:      ScheduleChainIssueUnreachable,
			EntryIDs:  unreachable,
			Detail:    fmt.Sprintf("%d rows unreachable from any head", len(unreachable)),
		})
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Kind != issues[j].Kind {
			return issues[i].Kind < issues[j].Kind
		}
		return strings.Join(issues[i].EntryIDs, ",") < strings.Join(issues[j].EntryIDs, ",")
	})

	return issues, nil
}

// ScheduleEntriesOrdered walks the schedule linked-list chain (if present) and
// returns the rows in chain order, head first.
func ScheduleEntriesOrdered(ctx context.Context, conn Execer, channelID string) ([]ScheduleEntry, error) {
	rows, err := queryRows(ctx, conn, scanScheduleEntryChainRow, `SELECT id, channel_id, start_ms, media_id, offset_ms, duration_ms,
		        anchor_schedule_entry_id, created_at_ms
		 FROM schedule_entries WHERE channel_id = ?`,
		channelID,
	)
	if err != nil {
		return nil, err
	}
	byID := map[string]chainRow{}
	for _, r := range rows {
		byID[r.entry.ID] = r
	}
	if len(byID) == 0 {
		return nil, nil
	}

	var head string
	successor := map[string]string{}
	for _, r := range byID {
		if !r.anchor.Valid {
			if head != "" {
				return nil, fmt.Errorf("channel %s: multiple heads in schedule chain", channelID)
			}
			head = r.entry.ID
			continue
		}
		if _, ok := byID[r.anchor.String]; !ok {
			return nil, fmt.Errorf("channel %s: anchor %q is not a member of this channel", channelID, r.anchor.String)
		}
		if existing, ok := successor[r.anchor.String]; ok {
			return nil, fmt.Errorf("channel %s: multiple rows anchor to %q (%s, %s)", channelID, r.anchor.String, existing, r.entry.ID)
		}
		successor[r.anchor.String] = r.entry.ID
	}
	if head == "" {
		return nil, fmt.Errorf("channel %s: no head row (chain corrupt)", channelID)
	}

	out := make([]ScheduleEntry, 0, len(byID))
	out = append(out, byID[head].entry)
	cur := head
	seen := map[string]bool{head: true}
	for {
		next, ok := successor[cur]
		if !ok {
			break
		}
		if seen[next] {
			return nil, fmt.Errorf("channel %s: cycle detected at %q", channelID, next)
		}
		out = append(out, byID[next].entry)
		seen[next] = true
		cur = next
	}
	if len(out) != len(byID) {
		return nil, fmt.Errorf("channel %s: chain length %d != row count %d (broken chain)", channelID, len(out), len(byID))
	}
	return out, nil
}

type scheduleChainAnchorRow struct {
	id     string
	anchor sql.NullString
}

func scanScheduleChainAnchorRow(row scanner) (scheduleChainAnchorRow, error) {
	var r scheduleChainAnchorRow
	err := row.Scan(&r.id, &r.anchor)
	return r, err
}

type chainRow struct {
	entry  ScheduleEntry
	anchor sql.NullString
}

func scanScheduleEntryChainRow(row scanner) (chainRow, error) {
	var r chainRow
	if err := row.Scan(&r.entry.ID, &r.entry.ChannelID, &r.entry.StartMs, &r.entry.MediaID, &r.entry.OffsetMs, &r.entry.DurationMs, &r.anchor, &r.entry.CreatedAtMs); err != nil {
		return chainRow{}, err
	}
	if r.anchor.Valid {
		v := r.anchor.String
		r.entry.AnchorScheduleEntryID = &v
	}
	return r, nil
}
