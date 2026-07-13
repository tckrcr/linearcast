package db

import (
	"context"
	"testing"
)

func TestMediaByIDReadsSeasonEpisodeNumbers(t *testing.T) {
	rw, err := OpenReadWrite(newTestDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rw.Close()

	if err := ApplySchema(context.Background(), rw); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	if _, err := rw.Exec(`INSERT INTO media (id, path, directory, title, season_number, episode_number, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('ep1', '/tmp/show-s02e03.mkv', '/tmp', 'Show S02E03', 2, 3, 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	m, err := MediaByID(context.Background(), rw, "ep1")
	if err != nil {
		t.Fatalf("MediaByID: %v", err)
	}
	if m == nil {
		t.Fatal("media not found")
	}
	if m.SeasonNumber == nil || *m.SeasonNumber != 2 {
		t.Fatalf("SeasonNumber=%v, want 2", m.SeasonNumber)
	}
	if m.EpisodeNumber == nil || *m.EpisodeNumber != 3 {
		t.Fatalf("EpisodeNumber=%v, want 3", m.EpisodeNumber)
	}
}
