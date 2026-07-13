package packageprofile

import "testing"

func TestBuiltInProfiles(t *testing.T) {
	main, ok := Lookup(DefaultName)
	if !ok {
		t.Fatalf("default profile missing")
	}
	if main.Video.Mode != VideoModeTranscode || main.Video.Codec != "libx264" {
		t.Fatalf("default video settings = %+v", main.Video)
	}
	if main.Name != "h264-1080p-8mbps" || main.Label != "broad compatibility - 1080p h.264" {
		t.Fatalf("default identity = %q / %q", main.Name, main.Label)
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

	high1080, ok := Lookup(H2641080p20MbpsName)
	if !ok {
		t.Fatalf("1080p 20mbps profile missing")
	}
	if high1080.Video.Mode != VideoModeTranscode || high1080.Video.Codec != "libx264" ||
		high1080.Video.ScaleHeight != 1080 || high1080.Video.VideoMaxBitrate != "20000k" {
		t.Fatalf("1080p 20mbps settings = %+v", high1080.Video)
	}

	high2160, ok := Lookup(HEVC2160p40MbpsHDRName)
	if !ok {
		t.Fatalf("2160p profile missing")
	}
	if high2160.Video.Mode != VideoModeTranscode || high2160.Video.Codec != "libx265" ||
		high2160.Video.Profile != "main10" || high2160.Video.ScaleHeight != 2160 ||
		high2160.Video.PixelFormat != "yuv420p10le" || high2160.Video.VideoMaxBitrate != "40000k" {
		t.Fatalf("2160p settings = %+v", high2160.Video)
	}
	if len(high2160.Tags) != 1 || high2160.Tags[0] != "hdr" {
		t.Fatalf("2160p tags = %+v", high2160.Tags)
	}

	hdr1080, ok := Lookup(HEVC1080p16MbpsHDRName)
	if !ok {
		t.Fatalf("1080p HDR profile missing")
	}
	if hdr1080.Video.Mode != VideoModeTranscode || hdr1080.Video.Codec != "libx265" ||
		hdr1080.Video.Profile != "main10" || hdr1080.Video.ScaleHeight != 1080 ||
		hdr1080.Video.PixelFormat != "yuv420p10le" || hdr1080.Video.VideoMaxBitrate != "16000k" {
		t.Fatalf("1080p HDR settings = %+v", hdr1080.Video)
	}
	if len(hdr1080.Tags) != 1 || hdr1080.Tags[0] != "hdr" {
		t.Fatalf("1080p HDR tags = %+v", hdr1080.Tags)
	}

	copySource, ok := Lookup(HEVCCopySourceName)
	if !ok {
		t.Fatalf("HEVC copy-source profile missing")
	}
	if copySource.Video.Mode != VideoModeCopy || copySource.Video.CodecRequired != "hevc" || copySource.Audio.Mode != AudioModeTranscode {
		t.Fatalf("HEVC copy-source settings = %+v", copySource)
	}

	if _, ok := Lookup(DefaultName); !ok {
		t.Fatal("default profile lookup failed")
	}
}
