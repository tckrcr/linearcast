package scheduler

import (
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func mediaRow(id, group string, durMs int64) db.Media {
	m := db.Media{
		ID:               id,
		Path:             "/m/" + id + ".mkv",
		Directory:        "/m",
		DurationMs:       durMs,
		Container:        "mkv",
		VideoCodec:       "h264",
		VideoHeight:      1080,
		AudioCodec:       "aac",
		CodecCheckPassed: true,
	}
	if group != "" {
		m.CollectionName = group
	}
	return m
}

func int64ptr(v int64) *int64 {
	return &v
}

const ep = int64(24 * 60 * 1000) // 24 minutes, 6s-aligned

func TestBlockScheduler_LeastRecentlyPlayedAndCursorAdvances(t *testing.T) {
	media := []db.Media{
		mediaRow("a1", "Show A S01 H1", ep),
		mediaRow("a2", "Show A S01 H1", ep),
		mediaRow("a3", "Show A S01 H1", ep),
		mediaRow("a4", "Show A S01 H1", ep),
		mediaRow("a5", "Show A S01 H1", ep),
		mediaRow("b1", "Show B S01 H1", ep),
		mediaRow("b2", "Show B S01 H1", ep),
		mediaRow("b3", "Show B S01 H1", ep),
		mediaRow("b4", "Show B S01 H1", ep),
		mediaRow("b5", "Show B S01 H1", ep),
	}

	// Fresh channel, 4h horizon → expect alternating blocks of 4.
	cursors := map[string]db.GroupCursor{}
	entries, err := BuildEntriesBlock("ch", media, cursors, "", 0, 4*60*60*1000)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 4h / 24min = 10 entries.
	if len(entries) != 10 {
		t.Fatalf("want 10 entries, got %d", len(entries))
	}

	// First block: a1..a4 (Show A picked first by alpha tiebreak; both groups have lastEndMs=0).
	for i, want := range []string{"a1", "a2", "a3", "a4"} {
		if entries[i].MediaID != want {
			t.Errorf("entry %d: media=%s, want %s", i, entries[i].MediaID, want)
		}
	}
	// Second block: b1..b4 (Show B is now least-recently-played).
	for i, want := range []string{"b1", "b2", "b3", "b4"} {
		if entries[4+i].MediaID != want {
			t.Errorf("entry %d: media=%s, want %s", 4+i, entries[4+i].MediaID, want)
		}
	}
	// Block 3: Show A is now least-recently-played; cursor advances to a5
	// then end-of-group truncates the block. Block 4: Show B is older
	// again, plays b5 (end of group). End-of-group ALWAYS ends the current
	// block; wraparound happens on the *next* pick of that group.
	if entries[8].MediaID != "a5" || entries[9].MediaID != "b5" {
		t.Errorf("blocks 3+4 wrong: %s, %s (want a5, b5)", entries[8].MediaID, entries[9].MediaID)
	}
}

func TestBlockScheduler_NoBackToBackWhenAlternativesExist(t *testing.T) {
	media := []db.Media{
		mediaRow("a1", "Show A S01 H1", ep),
		mediaRow("a2", "Show A S01 H1", ep),
		mediaRow("b1", "Show B S01 H1", ep),
	}
	// Pretend Show A was just played — its cursor is fresher, but it's
	// also the recentGroup, so picker must rotate to Show B.
	cursors := map[string]db.GroupCursor{
		"Show A S01 H1": {LastMediaID: "a2", LastEndMs: 1000},
	}
	entries, err := BuildEntriesBlock("ch", media, cursors, "Show A S01 H1", 0, 30*60*1000)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(entries) == 0 || entries[0].MediaID != "b1" {
		t.Fatalf("expected first entry to be b1 (rotation), got %+v", entries)
	}
}

func TestBlockScheduler_EndOfGroupTruncatesBlock(t *testing.T) {
	// Show A has only 2 episodes; Show B has 4. First block = 2 (truncated),
	// second block = 4 (Show B), third block = 2 (Show A wraps to start).
	media := []db.Media{
		mediaRow("a1", "Show A S01 H1", ep),
		mediaRow("a2", "Show A S01 H1", ep),
		mediaRow("b1", "Show B S01 H1", ep),
		mediaRow("b2", "Show B S01 H1", ep),
		mediaRow("b3", "Show B S01 H1", ep),
		mediaRow("b4", "Show B S01 H1", ep),
	}
	cursors := map[string]db.GroupCursor{}
	entries, err := BuildEntriesBlock("ch", media, cursors, "", 0, 8*ep)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(entries) != 8 {
		t.Fatalf("want 8 entries, got %d", len(entries))
	}
	wantOrder := []string{"a1", "a2", "b1", "b2", "b3", "b4", "a1", "a2"}
	for i, w := range wantOrder {
		if entries[i].MediaID != w {
			t.Errorf("entry %d: got %s, want %s", i, entries[i].MediaID, w)
		}
	}
}

func TestBlockScheduler_NullGroupBecomesSoloBucket(t *testing.T) {
	// Three NULL-group items + one real group. Each NULL is its own
	// singleton, so we get round-robin across {solo:m1, solo:m2, solo:m3,
	// "Real Group"} ordered by alpha-tiebreak when all start at lastEndMs=0.
	media := []db.Media{
		mediaRow("m1", "", ep),
		mediaRow("m2", "", ep),
		mediaRow("m3", "", ep),
		mediaRow("g1", "Real Group", ep),
		mediaRow("g2", "Real Group", ep),
	}
	cursors := map[string]db.GroupCursor{}
	entries, err := BuildEntriesBlock("ch", media, cursors, "", 0, 5*ep)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("want 5 entries, got %d", len(entries))
	}
	// Real Group sorts before "_solo:..." because "R" < "_" is false in
	// ASCII (underscore=0x5F, R=0x52); R is smaller. So first pick is the
	// "Real Group" block (up to 4 = 2 here), then alternates among solos.
	// Just assert all five distinct media played in some valid order — the
	// key invariant is no duplicates within a 5-step window when 5 distinct
	// groups are available.
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.MediaID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("expected each of 5 medias to play once, got %v", seen)
	}
}

func TestBlockScheduler_TimeHorizonStopsBuild(t *testing.T) {
	media := []db.Media{
		mediaRow("a1", "Show A S01 H1", ep),
		mediaRow("a2", "Show A S01 H1", ep),
	}
	// Horizon shorter than a single episode → no entries.
	entries, err := BuildEntriesBlock("ch", media, map[string]db.GroupCursor{}, "", 0, 6000)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries within sub-episode horizon, got %d", len(entries))
	}
}

func TestBlockScheduler_CollectionPlaybackUsesSeasonEpisodeOrdering(t *testing.T) {
	media := []db.Media{
		mediaRow("e3", "Show A", ep),
		mediaRow("e1", "Show A", ep),
		mediaRow("e2", "Show A", ep),
	}
	media[0].SeasonNumber = int64ptr(1)
	media[0].EpisodeNumber = int64ptr(3)
	media[1].SeasonNumber = int64ptr(1)
	media[1].EpisodeNumber = int64ptr(1)
	media[2].SeasonNumber = int64ptr(1)
	media[2].EpisodeNumber = int64ptr(2)

	entries, err := BuildEntriesBlock("ch", media, map[string]db.GroupCursor{}, "", 0, 3*ep)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"e1", "e2", "e3"}
	if len(entries) != len(want) {
		t.Fatalf("want %d entries, got %d", len(want), len(entries))
	}
	for i, mediaID := range want {
		if entries[i].MediaID != mediaID {
			t.Fatalf("entry %d: got %s, want %s", i, entries[i].MediaID, mediaID)
		}
	}
}

func TestBlockScheduler_UnorderedMediaSortAfterEpisodes(t *testing.T) {
	media := []db.Media{
		mediaRow("movie-b", "Mixed Library", ep),
		mediaRow("ep2", "Mixed Library", ep),
		mediaRow("movie-a", "Mixed Library", ep),
		mediaRow("ep1", "Mixed Library", ep),
	}
	media[0].Title = "Movie B"
	media[1].SeasonNumber = int64ptr(1)
	media[1].EpisodeNumber = int64ptr(2)
	media[1].Title = "Episode 2"
	media[2].Title = "Movie A"
	media[3].SeasonNumber = int64ptr(1)
	media[3].EpisodeNumber = int64ptr(1)
	media[3].Title = "Episode 1"

	entries, err := BuildEntriesBlock("ch", media, map[string]db.GroupCursor{}, "", 0, 4*ep)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"ep1", "ep2", "movie-a", "movie-b"}
	if len(entries) != len(want) {
		t.Fatalf("want %d entries, got %d", len(want), len(entries))
	}
	for i, mediaID := range want {
		if entries[i].MediaID != mediaID {
			t.Fatalf("entry %d: got %s, want %s", i, entries[i].MediaID, mediaID)
		}
	}
}
