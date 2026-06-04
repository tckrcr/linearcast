package packageprofile

import (
	"strings"
	"testing"
)

func TestBuiltInProfiles(t *testing.T) {
	main, ok := Lookup(DefaultName)
	if !ok {
		t.Fatalf("default profile missing")
	}
	if main.Video.Mode != VideoModeTranscode || main.Video.Codec != "libx264" {
		t.Fatalf("default video settings = %+v", main.Video)
	}
	if main.Video.Level != "4.1" || main.Video.VideoMaxBitrate != "8000k" {
		t.Fatalf("default video caps = %+v", main.Video)
	}
	if main.MediaKind != MediaKindVideo {
		t.Fatalf("default media kind = %q", main.MediaKind)
	}
	if main.Audio.Mode != AudioModeTranscode || main.Audio.Codec != "aac" || main.Audio.Channels != 2 {
		t.Fatalf("default audio settings = %+v", main.Audio)
	}

	music, ok := Lookup(MusicName)
	if !ok {
		t.Fatalf("music profile missing")
	}
	if music.MediaKind != MediaKindMusic || music.Audio.Codec != "aac" || music.Audio.Channels != 2 {
		t.Fatalf("music settings = %+v", music)
	}

	if len(BuiltIns()) != 2 {
		t.Fatalf("built-ins=%v, want video and music profiles", Names())
	}
}

func TestNamesIncludesSeedProfiles(t *testing.T) {
	got := strings.Join(Names(), ",")
	if got != "h264-main-1080p,music-aac-720p" {
		t.Fatalf("names=%q", got)
	}
}
