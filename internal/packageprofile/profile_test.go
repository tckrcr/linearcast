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

	copySource, ok := Lookup(H264CopySourceName)
	if !ok {
		t.Fatalf("copy-source profile missing")
	}
	if copySource.Video.Mode != VideoModeCopy || copySource.Video.CodecRequired != "h264" || copySource.Audio.Mode != AudioModeTranscode {
		t.Fatalf("copy-source settings = %+v", copySource)
	}

	p720, ok := Lookup(H264Main720pName)
	if !ok {
		t.Fatalf("720p profile missing")
	}
	if p720.Video.ScaleHeight != 720 || p720.Video.VideoMaxBitrate != "4000k" {
		t.Fatalf("720p settings = %+v", p720.Video)
	}

	p480, ok := Lookup(H264Main480pName)
	if !ok {
		t.Fatalf("480p profile missing")
	}
	if p480.Video.ScaleHeight != 480 || p480.Video.VideoMaxBitrate != "2000k" {
		t.Fatalf("480p settings = %+v", p480.Video)
	}

	if len(BuiltIns()) != 10 {
		t.Fatalf("built-ins=%v, want all built-in profiles including hevc-copy-source", Names())
	}
}

func TestNamesIncludesSeedProfiles(t *testing.T) {
	got := strings.Join(Names(), ",")
	if got != "h264-copy-source,h264-main-1080p,h264-main-480p,h264-main-720p,h264-nvenc-copy-source,h264-nvenc-main-1080p,h264-nvenc-main-480p,h264-nvenc-main-720p,hevc-copy-source,music-aac-720p" {
		t.Fatalf("names=%q", got)
	}
}
