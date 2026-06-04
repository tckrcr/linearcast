package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ChainIssueKind classifies a chain invariant violation detected by
// ValidateChannelMediaChains. The write paths and partial unique indexes
// prevent most of these in normal operation; the validator catches damage
// from manual SQL edits, migration bugs, or future write-path regressions.
type ChainIssueKind string

const (
	ChainIssueNoHead             ChainIssueKind = "no_head"
	ChainIssueMultipleHeads      ChainIssueKind = "multiple_heads"
	ChainIssueMultipleSuccessors ChainIssueKind = "multiple_successors"
	ChainIssueSelfAnchor         ChainIssueKind = "self_anchor"
	ChainIssueOrphanAnchor       ChainIssueKind = "orphan_anchor"
	ChainIssueCycle              ChainIssueKind = "cycle"
	ChainIssueUnreachable        ChainIssueKind = "unreachable"
)

// ChainIssue describes one invariant violation on a single channel. MediaIDs
// lists the rows directly implicated; Detail is a short human-readable note.
type ChainIssue struct {
	ChannelID string         `json:"channelId"`
	Kind      ChainIssueKind `json:"kind"`
	MediaIDs  []string       `json:"mediaIds,omitempty"`
	Detail    string         `json:"detail"`
}

// ValidateChannelMediaChains walks every channel's channel_media chain and
// reports invariant violations: missing/multiple heads, multiple successors,
// self-anchors, orphan anchors (pointing to non-member media), cycles, and
// rows unreachable from the head. Returns nil if all chains are intact.
//
// Cost is O(total channel_media rows): one scan of `channels`, then one scan
// of `channel_media` per channel. Safe to call concurrently with normal
// write traffic — the write paths are transactional, so a validator pass
// sees a single committed snapshot per channel, never a half-applied state.
func ValidateChannelMediaChains(ctx context.Context, conn *sql.DB) ([]ChainIssue, error) {
	channelIDs, err := queryRows(ctx, conn, scanString, `SELECT id FROM channels ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}

	var out []ChainIssue
	for _, channelID := range channelIDs {
		issues, err := validateChannelChain(ctx, conn, channelID)
		if err != nil {
			return nil, fmt.Errorf("channel %s: %w", channelID, err)
		}
		out = append(out, issues...)
	}
	return out, nil
}

func validateChannelChain(ctx context.Context, conn *sql.DB, channelID string) ([]ChainIssue, error) {
	all, err := queryRows(ctx, conn, scanChannelMediaOrderRow, `SELECT media_id, anchor_media_id FROM channel_media WHERE channel_id = ?`,
		channelID,
	)
	if err != nil {
		return nil, err
	}
	member := map[string]bool{}
	for _, r := range all {
		member[r.mediaID] = true
	}
	if len(all) == 0 {
		return nil, nil
	}

	var issues []ChainIssue
	// visited tracks rows we've accounted for, either by walking the chain
	// from a head or by reporting them as a self-anchor / orphan. Anything
	// still unvisited at the end is reported as unreachable exactly once.
	visited := map[string]bool{}

	var heads []string
	successors := map[string][]string{}
	for _, r := range all {
		if !r.anchor.Valid {
			heads = append(heads, r.mediaID)
			continue
		}
		if r.anchor.String == r.mediaID {
			issues = append(issues, ChainIssue{
				ChannelID: channelID,
				Kind:      ChainIssueSelfAnchor,
				MediaIDs:  []string{r.mediaID},
				Detail:    "row anchors to itself",
			})
			visited[r.mediaID] = true
			continue
		}
		if !member[r.anchor.String] {
			issues = append(issues, ChainIssue{
				ChannelID: channelID,
				Kind:      ChainIssueOrphanAnchor,
				MediaIDs:  []string{r.mediaID},
				Detail:    fmt.Sprintf("anchor_media_id %q is not a member of this channel", r.anchor.String),
			})
			visited[r.mediaID] = true
			continue
		}
		successors[r.anchor.String] = append(successors[r.anchor.String], r.mediaID)
	}

	switch {
	case len(heads) == 0:
		issues = append(issues, ChainIssue{
			ChannelID: channelID,
			Kind:      ChainIssueNoHead,
			Detail:    fmt.Sprintf("%d rows but no head (no row with anchor_media_id IS NULL)", len(all)),
		})
	case len(heads) > 1:
		sort.Strings(heads)
		issues = append(issues, ChainIssue{
			ChannelID: channelID,
			Kind:      ChainIssueMultipleHeads,
			MediaIDs:  heads,
			Detail:    fmt.Sprintf("%d rows have anchor_media_id IS NULL", len(heads)),
		})
	}

	for anchor, kids := range successors {
		if len(kids) > 1 {
			sorted := append([]string(nil), kids...)
			sort.Strings(sorted)
			issues = append(issues, ChainIssue{
				ChannelID: channelID,
				Kind:      ChainIssueMultipleSuccessors,
				MediaIDs:  sorted,
				Detail:    fmt.Sprintf("multiple rows anchor to %q", anchor),
			})
		}
	}

	// Walk forward from each head, then from any still-unvisited row. The
	// second pass catches isolated cycles: with one anchor per row, a cycle
	// is self-contained — every row in it has a non-NULL anchor, so no head
	// reaches it. Walking from any row inside the cycle re-encounters its
	// own starting point.
	walk := func(start string) {
		cur := start
		localPath := map[string]bool{}
		for {
			if localPath[cur] {
				issues = append(issues, ChainIssue{
					ChannelID: channelID,
					Kind:      ChainIssueCycle,
					MediaIDs:  []string{cur},
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
		if !visited[r.mediaID] {
			walk(r.mediaID)
		}
	}

	var unreachable []string
	for _, r := range all {
		if !visited[r.mediaID] {
			unreachable = append(unreachable, r.mediaID)
		}
	}
	if len(unreachable) > 0 {
		sort.Strings(unreachable)
		issues = append(issues, ChainIssue{
			ChannelID: channelID,
			Kind:      ChainIssueUnreachable,
			MediaIDs:  unreachable,
			Detail:    fmt.Sprintf("%d rows unreachable from any head", len(unreachable)),
		})
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Kind != issues[j].Kind {
			return issues[i].Kind < issues[j].Kind
		}
		return strings.Join(issues[i].MediaIDs, ",") < strings.Join(issues[j].MediaIDs, ",")
	})

	return issues, nil
}
