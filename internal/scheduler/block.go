package scheduler

import (
	"fmt"
	"sort"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// BlockSize is the maximum number of consecutive episodes scheduled from
// one scheduling_group before the picker rotates to a different group.
// Blocks may be shorter when the group runs out (end-of-group truncation;
// next time the group is picked, the cursor wraps to the beginning).
const BlockSize = 4

// soloPrefix marks a synthetic group derived from a single media row when
// scheduling_group is NULL. Each NULL-group media becomes its own
// singleton group so the picker still rotates among them.
const soloPrefix = "_solo:"

// BuildEntriesBlock builds schedule entries using least-recently-played
// scheduling_group selection with up-to-BlockSize episodes per block. See
// docs/database.md for the scheduling data model.
//
// `cursors` and `recentGroup` are the snapshot returned by
// db.LoadGroupHistory; on a fresh channel they are empty/zero.
func BuildEntriesBlock(channelID string, media []db.Media, cursors map[string]db.GroupCursor, recentGroup string, startMs, wantEndMs int64) ([]db.ScheduleEntry, error) {
	groups := groupMedia(media)
	if len(groups) == 0 {
		return nil, nil
	}
	groupNames := make([]string, 0, len(groups))
	for g := range groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	// Translate "" (NULL scheduling_group) recentGroup hint into the solo
	// form the picker actually uses.
	if recentGroup == "" {
		// We don't know which solo:<id> was last; treat as no recent group.
		// Worst case: a singleton-only channel may pick the same singleton
		// twice in a row, which is identical to alphabetical loop.
	}

	out := make([]db.ScheduleEntry, 0, 64)
	cur := startMs
	prev := recentGroup

	for cur < wantEndMs {
		g := pickNextGroup(groupNames, cursors, prev)
		if g == "" {
			break // every group failed to fit (shouldn't happen but bail safely)
		}
		groupMedia := groups[g]
		startIdx := nextIndexAfter(groupMedia, cursorMediaID(cursors, g))

		blockCount := 0
		fitAny := false
		for blockCount < BlockSize && startIdx < len(groupMedia) && cur < wantEndMs {
			m := groupMedia[startIdx]
			dur := ClipTo6s(m.DurationMs)
			if dur <= 0 {
				startIdx++
				continue
			}
			if cur+dur > wantEndMs {
				// Block scheduling writes whole media items only. A partial
				// episode would make the derived group cursor point at media
				// that did not actually finish, which breaks continuation.
				break
			}
			if err := enforceCodecAllowlist(m); err != nil {
				return nil, fmt.Errorf("codec policy violated for %s: %w", m.ID, err)
			}
			out = append(out, db.ScheduleEntry{
				ChannelID:   channelID,
				StartMs:     cur,
				MediaID:     m.ID,
				OffsetMs:    0,
				DurationMs:  dur,
				CreatedAtMs: time.Now().UTC().UnixMilli(),
			})
			cur += dur
			cursors[g] = db.GroupCursor{LastMediaID: m.ID, LastEndMs: cur}
			startIdx++
			blockCount++
			fitAny = true
		}
		if !fitAny {
			// Nothing from this group fit. Under the current whole-item rule we
			// stop instead of partially scheduling media and corrupting group
			// continuation state.
			break
		}
		prev = g
	}
	return out, nil
}

// groupMedia buckets media by scheduling_group, with NULL-group items
// each given their own singleton group keyed by soloPrefix+mediaID. Bucket
// insertion order follows the input slice, which the caller pre-orders by
// channel_media's linked-list chain (see EligibleChannelMedia).
func groupMedia(media []db.Media) map[string][]db.Media {
	out := map[string][]db.Media{}
	for _, m := range media {
		g := m.SchedulingGroup
		if g == "" {
			g = soloPrefix + m.ID
		}
		out[g] = append(out[g], m)
	}
	return out
}

// pickNextGroup picks the group with the smallest LastEndMs (zero for
// never-played groups), excluding `prev` if any other group is available.
// Ties broken alphabetically.
func pickNextGroup(groupNames []string, cursors map[string]db.GroupCursor, prev string) string {
	type cand struct {
		name      string
		lastEndMs int64
	}
	cands := make([]cand, 0, len(groupNames))
	for _, g := range groupNames {
		c := cursors[g] // zero value for never-played
		cands = append(cands, cand{name: g, lastEndMs: c.LastEndMs})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].lastEndMs != cands[j].lastEndMs {
			return cands[i].lastEndMs < cands[j].lastEndMs
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) == 0 {
		return ""
	}
	if len(cands) == 1 || cands[0].name != prev {
		return cands[0].name
	}
	return cands[1].name
}

// cursorMediaID returns the last-played media ID for group g, or "" if
// the group has no recorded history.
func cursorMediaID(cursors map[string]db.GroupCursor, g string) string {
	return cursors[g].LastMediaID
}

// nextIndexAfter returns the index in `media` immediately after the row
// whose ID == lastID, wrapping to 0 once the cursor passes the final row.
// If lastID is "" or not found in this group's current ordering (e.g. the
// media was removed from channel_media), the cursor resets to 0.
func nextIndexAfter(media []db.Media, lastID string) int {
	if lastID == "" {
		return 0
	}
	for i, m := range media {
		if m.ID == lastID {
			return (i + 1) % len(media)
		}
	}
	return 0
}
