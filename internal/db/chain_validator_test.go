package db

import (
	"context"
	"testing"
)

// TestValidateChannelMediaChains injects each known invariant violation and
// confirms the validator reports it. The partial unique indexes normally
// prevent multiple-heads / multiple-successors, so the test drops them
// before inserting violating rows.
func TestValidateChannelMediaChains(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	for _, id := range []string{"ok", "nohead", "multihead", "multisucc", "selfanchor", "orphan", "cycle"} {
		if _, err := rw.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
			VALUES (?, ?, '/tmp', 'alphabetical', 1, 0)`, id, id); err != nil {
			t.Fatalf("insert channel %s: %v", id, err)
		}
	}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if _, err := rw.Exec(`INSERT INTO media (id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', 1000, 'mp4', 'h264', 1080, 'aac', 1, 0)`, id, "/tmp/"+id); err != nil {
			t.Fatalf("insert media %s: %v", id, err)
		}
	}

	// "ok": healthy chain a → b → c.
	for i, id := range []string{"a", "b", "c"} {
		if _, err := AddChannelMedia(context.Background(), rw, "ok", id, int64(i)); err != nil {
			t.Fatalf("add ok/%s: %v", id, err)
		}
	}

	// Drop the partial unique indexes so we can fabricate the violations
	// those indexes normally prevent. The validator's whole purpose is to
	// catch state that bypassed those guards (manual edits, future bugs).
	for _, idx := range []string{"idx_channel_media_head", "idx_channel_media_anchor"} {
		if _, err := rw.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			t.Fatalf("drop %s: %v", idx, err)
		}
	}

	// "nohead": two rows, both with anchors → no head exists, both rows unreachable.
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES
		('nohead', 'a', 'b', 0), ('nohead', 'b', 'a', 0)`); err != nil {
		t.Fatalf("insert nohead: %v", err)
	}

	// "multihead": three rows, two of them headed.
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES
		('multihead', 'a', NULL, 0), ('multihead', 'b', NULL, 0), ('multihead', 'c', 'a', 0)`); err != nil {
		t.Fatalf("insert multihead: %v", err)
	}

	// "multisucc": head a, both b and c anchor to a.
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES
		('multisucc', 'a', NULL, 0), ('multisucc', 'b', 'a', 0), ('multisucc', 'c', 'a', 0)`); err != nil {
		t.Fatalf("insert multisucc: %v", err)
	}

	// "selfanchor": head a, b anchors to itself.
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES
		('selfanchor', 'a', NULL, 0), ('selfanchor', 'b', 'b', 0)`); err != nil {
		t.Fatalf("insert selfanchor: %v", err)
	}

	// "orphan": head a, b anchors to a media_id not in this channel.
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES
		('orphan', 'a', NULL, 0), ('orphan', 'b', 'zzz', 0)`); err != nil {
		t.Fatalf("insert orphan: %v", err)
	}

	// "cycle": head a → b (healthy reachable chain) plus an isolated
	// 3-cycle c → d → e → c (each anchors to the previous). With one
	// anchor per row, cycles are always self-contained — no head reaches
	// them — so the validator's second walk pass catches them.
	if _, err := rw.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms) VALUES
		('cycle', 'a', NULL, 0),
		('cycle', 'b', 'a', 0),
		('cycle', 'c', 'e', 0),
		('cycle', 'd', 'c', 0),
		('cycle', 'e', 'd', 0)`); err != nil {
		t.Fatalf("insert cycle: %v", err)
	}

	issues, err := ValidateChannelMediaChains(context.Background(), rw)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	got := map[string]map[ChainIssueKind]int{}
	for _, iss := range issues {
		if got[iss.ChannelID] == nil {
			got[iss.ChannelID] = map[ChainIssueKind]int{}
		}
		got[iss.ChannelID][iss.Kind]++
	}

	if _, ok := got["ok"]; ok {
		t.Errorf("healthy channel reported issues: %#v", got["ok"])
	}
	expect := map[string]ChainIssueKind{
		"nohead":     ChainIssueNoHead,
		"multihead":  ChainIssueMultipleHeads,
		"multisucc":  ChainIssueMultipleSuccessors,
		"selfanchor": ChainIssueSelfAnchor,
		"orphan":     ChainIssueOrphanAnchor,
		"cycle":      ChainIssueCycle,
	}
	for ch, kind := range expect {
		if got[ch][kind] == 0 {
			t.Errorf("channel %s: missing %s issue (got %#v)", ch, kind, got[ch])
		}
	}
}

// TestValidateChannelMediaChainsEmpty: clean DB returns nil.
func TestValidateChannelMediaChainsEmpty(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()
	issues, err := ValidateChannelMediaChains(context.Background(), rw)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues on empty db, got %#v", issues)
	}
}
