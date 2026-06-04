package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestChannelMediaCRUDAndEligible(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
        VALUES ('ch1', 'Channel One', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
        video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
        VALUES ('m1', '/tmp/m1.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
               ('m2', '/tmp/m2.mkv', '/tmp', 6000, 'mkv', 'h264', 1080, 'aac', 1, 0),
               ('mfail', '/tmp/mfail.mkv', '/tmp', 6000, 'mkv', 'hevc', 1080, 'aac', 0, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	added, err := AddChannelMedia(context.Background(), rw, "ch1", "m2", 100)
	if err != nil || !added {
		t.Fatalf("add m2: added=%v err=%v", added, err)
	}
	added, err = AddChannelMedia(context.Background(), rw, "ch1", "m1", 100)
	if err != nil || !added {
		t.Fatalf("add m1: added=%v err=%v", added, err)
	}
	added, err = AddChannelMedia(context.Background(), rw, "ch1", "mfail", 100)
	if err != nil || !added {
		t.Fatalf("add mfail: added=%v err=%v", added, err)
	}
	// idempotent
	again, err := AddChannelMedia(context.Background(), rw, "ch1", "m1", 100)
	if err != nil || again {
		t.Fatalf("re-add m1: again=%v err=%v", again, err)
	}

	// ChannelMediaList returns in linked-list chain order. Inserts happened in
	// the order m2, m1, mfail (each appended to the tail), so the chain is
	// m2 → m1 → mfail.
	members, err := ChannelMediaList(context.Background(), rw, "ch1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(members) != 3 || members[0].MediaID != "m2" || members[1].MediaID != "m1" || members[2].MediaID != "mfail" {
		t.Fatalf("ordering wrong: %+v", members)
	}

	// EligibleChannelMedia returns in linked-list chain order. AddChannelMedia
	// appends to the tail, so chain order matches insertion order: m2, m1, mfail.
	// mfail is excluded because codec_check_passed = 0.
	eligible, err := EligibleChannelMedia(context.Background(), rw, "ch1")
	if err != nil {
		t.Fatalf("eligible: %v", err)
	}
	if len(eligible) != 2 || eligible[0].ID != "m2" || eligible[1].ID != "m1" {
		t.Fatalf("expected codec-passing chain order [m2, m1]; got %+v", eligible)
	}

	n, err := RemoveChannelMedia(context.Background(), rw, "ch1", "m2")
	if err != nil || n != 1 {
		t.Fatalf("remove m2: n=%d err=%v", n, err)
	}
	members, _ = ChannelMediaList(context.Background(), rw, "ch1")
	if len(members) != 2 {
		t.Fatalf("after remove want 2, got %d", len(members))
	}

	// ReplaceChannelMedia clears and re-inserts.
	if err := ReplaceChannelMedia(context.Background(), rw, "ch1", []ChannelMediaRow{
		{ChannelID: "ch1", MediaID: "m2", AddedAtMs: 200},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	members, _ = ChannelMediaList(context.Background(), rw, "ch1")
	if len(members) != 1 || members[0].MediaID != "m2" {
		t.Fatalf("replace result wrong: %+v", members)
	}
}

// TestChannelMediaLinkedList exercises the linked-list write/read primitives:
// AddChannelMedia appends to the tail, RemoveChannelMedia stitches the chain,
// ReplaceChannelMedia assigns anchors by slice position, MoveChannelMediaAfter
// repositions without renumbering, and ChannelMediaOrdered walks the chain.
func TestChannelMediaLinkedList(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'C', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', 1000, 'mp4', 'h264', 1080, 'aac', 1, 0)`, id, "/tmp/"+id); err != nil {
			t.Fatalf("insert media %s: %v", id, err)
		}
	}

	// Append in order a, b, c, d. Chain should be a → b → c → d.
	for i, id := range []string{"a", "b", "c", "d"} {
		added, err := AddChannelMedia(context.Background(), rw, "ch", id, int64(i))
		if err != nil || !added {
			t.Fatalf("add %s: added=%v err=%v", id, added, err)
		}
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"a", "b", "c", "d"}; !slicesEqual(got, want) {
		t.Fatalf("after appends, got %v want %v", got, want)
	}

	// Re-add idempotent: second add of "a" returns false, chain unchanged.
	again, err := AddChannelMedia(context.Background(), rw, "ch", "a", 99)
	if err != nil || again {
		t.Fatalf("re-add a: again=%v err=%v", again, err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"a", "b", "c", "d"}; !slicesEqual(got, want) {
		t.Fatalf("after re-add, chain mutated: %v", got)
	}

	// Move b to after c (chain: a → c → b → d).
	if err := MoveChannelMediaAfter(context.Background(), rw, "ch", "b", "c"); err != nil {
		t.Fatalf("move b after c: %v", err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"a", "c", "b", "d"}; !slicesEqual(got, want) {
		t.Fatalf("after move b after c, got %v want %v", got, want)
	}

	// Move d to head (chain: d → a → c → b).
	if err := MoveChannelMediaAfter(context.Background(), rw, "ch", "d", ""); err != nil {
		t.Fatalf("move d to head: %v", err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"d", "a", "c", "b"}; !slicesEqual(got, want) {
		t.Fatalf("after move d to head, got %v want %v", got, want)
	}

	// Self-move rejected.
	if err := MoveChannelMediaAfter(context.Background(), rw, "ch", "a", "a"); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("self-move: want ErrInvalidMove, got %v", err)
	}

	// Move targeting a non-member.
	if err := MoveChannelMediaAfter(context.Background(), rw, "ch", "a", "zzz"); !errors.Is(err, ErrMediaNotInChannel) {
		t.Fatalf("move after non-member: want ErrMediaNotInChannel, got %v", err)
	}

	// Remove c from the middle (chain: d → a → b).
	if n, err := RemoveChannelMedia(context.Background(), rw, "ch", "c"); err != nil || n != 1 {
		t.Fatalf("remove c: n=%d err=%v", n, err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"d", "a", "b"}; !slicesEqual(got, want) {
		t.Fatalf("after remove c, got %v want %v", got, want)
	}

	// Remove head d (chain: a → b).
	if n, err := RemoveChannelMedia(context.Background(), rw, "ch", "d"); err != nil || n != 1 {
		t.Fatalf("remove d: n=%d err=%v", n, err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"a", "b"}; !slicesEqual(got, want) {
		t.Fatalf("after remove d, got %v want %v", got, want)
	}

	// Replace via caller-ordered slice: anchors are assigned by position.
	if err := ReplaceChannelMedia(context.Background(), rw, "ch", []ChannelMediaRow{
		{ChannelID: "ch", MediaID: "c", AddedAtMs: 1},
		{ChannelID: "ch", MediaID: "a", AddedAtMs: 2},
		{ChannelID: "ch", MediaID: "b", AddedAtMs: 3},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"c", "a", "b"}; !slicesEqual(got, want) {
		t.Fatalf("after replace, got %v want %v", got, want)
	}
}

// TestAddChannelMediaAfter exercises the insert-at-position helper:
// head insert, mid-chain insert, idempotent re-add, self-anchor rejection,
// and unknown-anchor rejection.
func TestAddChannelMediaAfter(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch', 'C', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', 1000, 'mp4', 'h264', 1080, 'aac', 1, 0)`, id, "/tmp/"+id); err != nil {
			t.Fatalf("insert media %s: %v", id, err)
		}
	}

	// Empty-channel head insert.
	added, err := AddChannelMediaAfter(context.Background(), rw, "ch", "a", "", 1)
	if err != nil || !added {
		t.Fatalf("insert a at head (empty): added=%v err=%v", added, err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"a"}; !slicesEqual(got, want) {
		t.Fatalf("after head insert into empty channel, got %v want %v", got, want)
	}

	// Insert b after a (tail).
	if _, err := AddChannelMediaAfter(context.Background(), rw, "ch", "b", "a", 2); err != nil {
		t.Fatalf("insert b after a: %v", err)
	}
	// Insert c at head.
	if _, err := AddChannelMediaAfter(context.Background(), rw, "ch", "c", "", 3); err != nil {
		t.Fatalf("insert c at head: %v", err)
	}
	// Insert d after c (mid-chain).
	if _, err := AddChannelMediaAfter(context.Background(), rw, "ch", "d", "c", 4); err != nil {
		t.Fatalf("insert d after c: %v", err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"c", "d", "a", "b"}; !slicesEqual(got, want) {
		t.Fatalf("after mixed inserts, got %v want %v", got, want)
	}

	// Idempotent: re-adding an existing member returns (false, nil), no shuffle.
	again, err := AddChannelMediaAfter(context.Background(), rw, "ch", "a", "b", 99)
	if err != nil || again {
		t.Fatalf("re-add a after b: again=%v err=%v", again, err)
	}
	if got, want := mustOrdered(t, rw, "ch"), []string{"c", "d", "a", "b"}; !slicesEqual(got, want) {
		t.Fatalf("after re-add, chain mutated: %v", got)
	}

	// Self-anchor rejected.
	if _, err := AddChannelMediaAfter(context.Background(), rw, "ch", "e", "e", 5); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("self-anchor: want ErrInvalidMove, got %v", err)
	}

	// Anchor not a member rejected.
	if _, err := AddChannelMediaAfter(context.Background(), rw, "ch", "e", "zzz", 5); !errors.Is(err, ErrMediaNotInChannel) {
		t.Fatalf("unknown anchor: want ErrMediaNotInChannel, got %v", err)
	}

	// Empty mediaID rejected.
	if _, err := AddChannelMediaAfter(context.Background(), rw, "ch", "", "a", 5); err == nil {
		t.Fatalf("empty mediaID: want error, got nil")
	}
}

func mustOrdered(t *testing.T, db *sql.DB, channelID string) []string {
	t.Helper()
	got, err := ChannelMediaOrdered(context.Background(), db, channelID)
	if err != nil {
		t.Fatalf("ChannelMediaOrdered: %v", err)
	}
	return got
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEligibleReadyPackagedChannelMedia(t *testing.T) {
	path := newTestDB(t)
	rw, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
        VALUES ('ch1', 'Channel One', '/tmp', 'alphabetical', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
        video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
        VALUES ('ready', '/tmp/ready.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
               ('pending', '/tmp/pending.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
               ('nopkg', '/tmp/nopkg.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	for _, mediaID := range []string{"ready", "pending", "nopkg"} {
		if _, err := AddChannelMedia(context.Background(), rw, "ch1", mediaID, 0); err != nil {
			t.Fatalf("add channel media %s: %v", mediaID, err)
		}
	}
	dur1 := int64(11011)
	if err := UpsertMediaPackage(context.Background(), rw, MediaPackage{
		ID:                 "pkg-ready",
		MediaID:            "ready",
		RenditionProfile:   "h264-main-1080p",
		Status:             PackageStatusReady,
		PackagedDurationMs: &dur1,
		CreatedAtMs:        0,
		UpdatedAtMs:        0,
	}); err != nil {
		t.Fatalf("upsert ready package: %v", err)
	}
	dur2 := int64(11011)
	if err := UpsertMediaPackage(context.Background(), rw, MediaPackage{
		ID:                 "pkg-pending",
		MediaID:            "pending",
		RenditionProfile:   "h264-main-1080p",
		Status:             PackageStatusPending,
		PackagedDurationMs: &dur2,
		CreatedAtMs:        0,
		UpdatedAtMs:        0,
	}); err != nil {
		t.Fatalf("upsert pending package: %v", err)
	}

	eligible, err := EligibleReadyPackagedChannelMedia(context.Background(), rw, "ch1", "h264-main-1080p")
	if err != nil {
		t.Fatalf("eligible packaged: %v", err)
	}
	if len(eligible) != 1 || eligible[0].ID != "ready" || eligible[0].DurationMs != 11011 {
		t.Fatalf("eligible packaged mismatch: %+v", eligible)
	}
}
