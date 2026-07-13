package packager

import (
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestLiveEncodingArgsSeekAndPacingBeforeInput(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	spec := LiveEncodingSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		SeekMs:          1_815_500,
		LimitMs:         1_004_500,
		TargetSegmentMs: 6000,
		RealtimePacing:  true,
		BurstSec:        90,
		Profile:         profile,
	}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
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

func TestLiveEncodingArgsZeroBurstAndLimitSkipFlags(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	spec := LiveEncodingSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		TargetSegmentMs: 6000,
		Profile:         profile,
	}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
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

func TestLiveEncodingArgsRealtimePacingWithoutBurst(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	spec := LiveEncodingSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		TargetSegmentMs: 6000,
		RealtimePacing:  true,
		Profile:         profile,
	}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-readrate 1.0") {
		t.Fatalf("realtime pacing should include readrate: %s", joined)
	}
	if strings.Contains(joined, "-readrate_initial_burst") {
		t.Fatalf("zero burst should omit initial burst: %s", joined)
	}
}

func TestLiveEncodingArgsAcceptsCopyProfile(t *testing.T) {
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
	spec := LiveEncodingSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		SeekMs:          30_500,
		LimitMs:         60_250,
		TargetSegmentMs: 6000,
		RealtimePacing:  true,
		BurstSec:        90,
		Profile:         profile,
	}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("copy-mode profile must build live encoding args: %v", err)
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
	if !strings.Contains(joined, "-readrate 1.0") {
		t.Fatalf("copy mode must include -readrate 1.0: %s", joined)
	}
	if !strings.Contains(joined, "-readrate_initial_burst 90") {
		t.Fatalf("copy mode must include initial burst: %s", joined)
	}
}

func TestLiveEncodingArgsCopyReadrateBurst(t *testing.T) {
	profile := packageprofile.Profile{
		Name:  "h264-copy-source",
		Video: packageprofile.VideoSettings{Mode: packageprofile.VideoModeCopy, CodecRequired: "h264"},
		Audio: packageprofile.AudioSettings{Mode: packageprofile.AudioModeTranscode, Codec: "aac"},
	}
	spec := LiveEncodingSpec{
		MediaPath:       "/in.mkv",
		OutDir:          "/sess",
		TargetSegmentMs: 6000,
		RealtimePacing:  true,
		BurstSec:        90,
		Profile:         profile,
	}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-readrate 1.0") {
		t.Fatalf("copy burst must include readrate pacing: %s", joined)
	}
	if !strings.Contains(joined, "-readrate_initial_burst 90") {
		t.Fatalf("copy burst must include initial burst: %s", joined)
	}
}

func TestLiveEncodingArgsCopyAddsNoAccurateSeek(t *testing.T) {
	// Copy mode must use -noaccurate_seek so audio and video both start at the
	// prior source keyframe; without it the transcoded audio is trimmed to the
	// playhead while copied video snaps back, leaving a leading audio edit-list
	// skew that MSE/hls.js surface as a startup buffer hole.
	profile := packageprofile.Profile{
		Name:  "h264-copy-source",
		Video: packageprofile.VideoSettings{Mode: packageprofile.VideoModeCopy, CodecRequired: "h264"},
		Audio: packageprofile.AudioSettings{Mode: packageprofile.AudioModeTranscode, Codec: "aac"},
	}
	spec := LiveEncodingSpec{MediaPath: "/in.mkv", OutDir: "/sess", SeekMs: 30_500, TargetSegmentMs: 6000, Profile: profile}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
	}
	joined := strings.Join(args, " ")
	na := strings.Index(joined, "-noaccurate_seek")
	ss := strings.Index(joined, "-ss 30.500")
	in := strings.Index(joined, "-i /in.mkv")
	if na < 0 {
		t.Fatalf("copy mode must add -noaccurate_seek: %s", joined)
	}
	// -noaccurate_seek is an input option and must precede the seek and -i so it
	// applies to this input.
	if !(na < ss && ss < in) {
		t.Fatalf("-noaccurate_seek must precede -ss and -i: %s", joined)
	}
}

func TestLiveEncodingArgsTranscodeOmitsNoAccurateSeek(t *testing.T) {
	// Transcode profiles re-keyframe at output t=0 and must keep frame-accurate
	// seeking to the requested playhead, so they must not use -noaccurate_seek.
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	spec := LiveEncodingSpec{MediaPath: "/in.mkv", OutDir: "/sess", SeekMs: 30_500, TargetSegmentMs: 6000, Profile: profile}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
	}
	if strings.Contains(strings.Join(args, " "), "-noaccurate_seek") {
		t.Fatalf("transcode mode must not add -noaccurate_seek: %s", strings.Join(args, " "))
	}
}

func TestLiveEncodingArgsCopyRejectsCodecMismatch(t *testing.T) {
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
	spec := LiveEncodingSpec{MediaPath: "/in.mkv", OutDir: "/sess", TargetSegmentMs: 6000, Profile: profile}
	if _, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe()); err == nil {
		t.Fatalf("copy profile with mismatched source codec must be rejected")
	}
}

func TestLiveEncodingArgsBurnsSubtitleWithOverlayFilter(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	streamIndex := 3
	spec := LiveEncodingSpec{
		MediaPath:               "/in.mkv",
		OutDir:                  "/sess",
		TargetSegmentMs:         6000,
		Profile:                 profile,
		BurnSubtitleStreamIndex: &streamIndex,
	}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
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

func TestLiveEncodingArgsCopyRejectsSubtitleBurn(t *testing.T) {
	profile := packageprofile.Profile{
		Name: "h264-copy-source",
		Video: packageprofile.VideoSettings{
			Mode:          packageprofile.VideoModeCopy,
			CodecRequired: "h264",
		},
		Audio: packageprofile.AudioSettings{Mode: packageprofile.AudioModeCopy},
	}
	streamIndex := 3
	spec := LiveEncodingSpec{
		MediaPath:               "/in.mkv",
		OutDir:                  "/sess",
		TargetSegmentMs:         6000,
		Profile:                 profile,
		BurnSubtitleStreamIndex: &streamIndex,
	}
	if _, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe()); err == nil {
		t.Fatalf("copy profile with subtitle burn must be rejected")
	}
}

func TestLiveEncodingArgsAddsSubtitleOutputs(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	spec := LiveEncodingSpec{
		MediaPath:             "/in.mkv",
		OutDir:                "/sess",
		TargetSegmentMs:       6000,
		Profile:               profile,
		SubtitleStreamIndexes: []int{2, 3},
	}
	args, err := liveEncodingArgsFromProbe("/in.mkv", "/sess", spec, makeAVProbe())
	if err != nil {
		t.Fatalf("live encoding args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-map 0:2",
		"-map 0:3",
		"-c:s webvtt",
		"-segment_format webvtt",
		"-segment_list /sess/subs/s2/playlist.m3u8",
		"-segment_list /sess/subs/s3/playlist.m3u8",
		"/sess/subs/s2/seg_%06d.vtt",
		"/sess/subs/s3/seg_%06d.vtt",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}
