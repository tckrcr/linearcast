package packager

import (
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestLiveSessionArgsSeekAndPacingBeforeInput(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	spec := LiveSessionSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		SeekMs:          1_815_500,
		LimitMs:         1_004_500,
		TargetSegmentMs: 6000,
		BurstSec:        90,
		Profile:         profile,
	}
	args, err := liveSessionArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live session args: %v", err)
	}
	joined := strings.Join(args, " ")
	inputAt := strings.Index(joined, "-i /in.mkv")
	if inputAt < 0 {
		t.Fatalf("args missing input: %s", joined)
	}
	for _, want := range []string{
		"-ss 1815.500",
		"-t 1004.500",
		"-readrate 1.0",
		"-readrate_initial_burst 90",
	} {
		at := strings.Index(joined, want)
		if at < 0 {
			t.Fatalf("args missing %q: %s", want, joined)
		}
		if at > inputAt {
			t.Fatalf("%q must precede -i (input option): %s", want, joined)
		}
	}
	// Output side must match package encodes: same codec chain and HLS layout.
	for _, want := range []string{
		"-c:v libx264",
		"-preset veryfast",
		"-force_key_frames expr:gte(t,n_forced*6)",
		"-hls_segment_type fmp4",
		"-hls_fmp4_init_filename init.mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}

func TestLiveSessionArgsZeroBurstAndLimitSkipFlags(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	spec := LiveSessionSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		TargetSegmentMs: 6000,
		Profile:         profile,
	}
	args, err := liveSessionArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live session args: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-readrate") {
		t.Fatalf("zero burst should skip readrate pacing: %s", joined)
	}
	if strings.Contains(joined, "-t ") {
		t.Fatalf("zero limit should skip -t: %s", joined)
	}
	if !strings.Contains(joined, "-ss 0 ") {
		t.Fatalf("seek 0 should still be explicit: %s", joined)
	}
}

func TestLiveSessionArgsAcceptsCopyProfile(t *testing.T) {
	profile := packageprofile.Profile{
		Name: "h264-copy-source",
		Video: packageprofile.VideoSettings{
			Mode:          packageprofile.VideoModeCopy,
			CodecRequired: "h264",
		},
		Audio: packageprofile.AudioSettings{
			Mode:  packageprofile.AudioModeTranscode,
			Codec: "aac",
		},
	}
	spec := LiveSessionSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		SeekMs:          30_500,
		LimitMs:         60_250,
		TargetSegmentMs: 6000,
		BurstSec:        90,
		Profile:         profile,
	}
	args, err := liveSessionArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("copy-mode profile must build live session args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-ss 30.500", "-t 60.250", "-c:v copy", "-c:a aac", "-hls_segment_type fmp4"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	// Copy mode cannot re-encode keyframes, so it must not try to force them.
	if strings.Contains(joined, "-force_key_frames") {
		t.Fatalf("copy mode must not force keyframes: %s", joined)
	}
}

func TestLiveSessionArgsCopyRejectsCodecMismatch(t *testing.T) {
	// A copy profile that requires hevc cannot serve an h264 source: -c:v copy
	// can't change the codec, so this must be rejected up front.
	profile := packageprofile.Profile{
		Name: "hevc-copy-source",
		Video: packageprofile.VideoSettings{
			Mode:          packageprofile.VideoModeCopy,
			CodecRequired: "hevc",
		},
		Audio: packageprofile.AudioSettings{Mode: packageprofile.AudioModeCopy},
	}
	spec := LiveSessionSpec{MediaPath: "/in.mkv", OutDir: "/sess", TargetSegmentMs: 6000, Profile: profile}
	if _, err := liveSessionArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe()); err == nil {
		t.Fatalf("copy profile with mismatched source codec must be rejected")
	}
}

func TestLiveSessionArgsBurnsSubtitleWithOverlayFilter(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	streamIndex := 3
	spec := LiveSessionSpec{
		MediaPath:               "/in.mkv",
		OutDir:                  "/sess",
		TargetSegmentMs:         6000,
		Profile:                 profile,
		BurnSubtitleStreamIndex: &streamIndex,
	}
	args, err := liveSessionArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live session args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-filter_complex [0:0][0:3]overlay=eof_action=pass,scale=-2:'min(ih,1080)',format=yuv420p[v]",
		"-map [v]",
		"-map 0:1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-vf ") {
		t.Fatalf("burn-in path should use filter_complex instead of -vf: %s", joined)
	}
}

func TestLiveSessionArgsCopyRejectsSubtitleBurn(t *testing.T) {
	profile := packageprofile.Profile{
		Name: "h264-copy-source",
		Video: packageprofile.VideoSettings{
			Mode:          packageprofile.VideoModeCopy,
			CodecRequired: "h264",
		},
		Audio: packageprofile.AudioSettings{Mode: packageprofile.AudioModeCopy},
	}
	streamIndex := 3
	spec := LiveSessionSpec{
		MediaPath:               "/in.mkv",
		OutDir:                  "/sess",
		TargetSegmentMs:         6000,
		Profile:                 profile,
		BurnSubtitleStreamIndex: &streamIndex,
	}
	if _, err := liveSessionArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe()); err == nil {
		t.Fatalf("copy profile with subtitle burn must be rejected")
	}
}
