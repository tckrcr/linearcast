package packager

import (
	"errors"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

// makeHDRProbe returns a 4K HDR10 (PQ) HEVC source with HDR10 static metadata,
// tagged hvc1 (i.e. not Dolby Vision Profile 5).
func makeHDRProbe() sourceProbe {
	video := makeStream("video", "hevc")
	video.Index = 0
	video.AvgFrameRate = "24000/1001"
	video.Width = 3840
	video.Height = 2160
	video.ColorTransfer = "smpte2084"
	video.ColorPrimaries = "bt2020"
	video.CodecTagString = "hvc1"
	video.SideDataList = []probeSideData{
		{
			SideDataType: "Mastering display metadata",
			RedX:         "35400/50000", RedY: "14600/50000",
			GreenX: "8500/50000", GreenY: "39850/50000",
			BlueX: "6550/50000", BlueY: "2300/50000",
			WhitePointX: "15635/50000", WhitePointY: "16450/50000",
			MaxLuminance: "10000000/10000", MinLuminance: "50/10000",
		},
		{SideDataType: "Content light level metadata", MaxContent: 1000, MaxAverage: 400},
	}
	audio := makeStream("audio", "aac")
	audio.Index = 1
	audio.Tags.Language = "eng"
	return sourceProbe{Streams: []probeStream{video, audio}}
}

func testHEVCHDRProfile() packageprofile.Profile {
	return packageprofile.Profile{
		Name:      "test-hevc-hdr-2160p",
		MediaKind: packageprofile.MediaKindVideo,
		Video: packageprofile.VideoSettings{
			Mode:            packageprofile.VideoModeTranscode,
			Codec:           "libx265",
			Profile:         "main10",
			Preset:          "medium",
			CRF:             20,
			PixelFormat:     "yuv420p10le",
			ScaleHeight:     2160,
			VideoMaxBitrate: "20000k",
		},
		Audio: packageprofile.AudioSettings{
			Mode:     packageprofile.AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "256k",
			Channels: 2,
			SampleHz: 48000,
		},
	}
}

func TestFFmpegArgsPreservesHDRForHEVCProfile(t *testing.T) {
	profile := testHEVCHDRProfile()
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "medium", makeHDRProbe(), profile, nil, noBurn, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v libx265",
		"-pix_fmt yuv420p10le",
		"-profile:v main10",
		"-colorspace bt2020nc",
		"-color_primaries bt2020",
		"-color_trc smpte2084",
		"-x265-params",
		"hdr-opt=1",
		"keyint=48",
		"min-keyint=48",
		"no-scenecut=1",
		"master-display=G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,50)",
		"max-cll=1000,400",
		"-tag:v hvc1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("HDR args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "scale=-2:'min(ih,2160)'") {
		t.Fatalf("HDR args should not scale a source already within the height cap: %s", joined)
	}
	// HDR output must not be flattened to 8-bit SDR.
	if strings.Contains(joined, "-pix_fmt yuv420p ") || strings.HasSuffix(joined, "-pix_fmt yuv420p") {
		t.Fatalf("HDR args should not force 8-bit yuv420p: %s", joined)
	}
}

func TestFFmpegArgsHLGTransfer(t *testing.T) {
	profile := testHEVCHDRProfile()
	probe := makeHDRProbe()
	probe.Streams[0].ColorTransfer = "arib-std-b67"
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "medium", probe, profile, nil, noBurn, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-color_trc arib-std-b67") || !strings.Contains(joined, "transfer=arib-std-b67") {
		t.Fatalf("HLG transfer not propagated: %s", joined)
	}
}

func TestFFmpegArgsSDRSourceOnHEVCProfileSkipsHDRArgs(t *testing.T) {
	profile := testHEVCHDRProfile()
	probe := makeHDRProbe()
	probe.Streams[0].ColorTransfer = "bt709" // SDR source
	probe.Streams[0].ColorPrimaries = "bt709"
	probe.Streams[0].SideDataList = nil
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "medium", probe, profile, nil, noBurn, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	// No HDR color tags / x265 hdr params for an SDR source...
	for _, notWant := range []string{"hdr-opt=1", "-color_trc smpte2084", "-colorspace bt2020nc"} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("SDR source should not emit %q: %s", notWant, joined)
		}
	}
	// ...but the HEVC output is still tagged hvc1 and stays in the profile's pixel format.
	if !strings.Contains(joined, "-tag:v hvc1") || !strings.Contains(joined, "-pix_fmt yuv420p10le") {
		t.Fatalf("expected hvc1 tag and profile pixel format: %s", joined)
	}
}

func TestFFmpegArgsTonemapsHDRForSDRProfile(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default SDR profile")
	}
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", makeHDRProbe(), profile, nil, noBurn, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	// An HDR source on an SDR (libx264, 8-bit) profile must be tone-mapped to
	// BT.709, then scaled — not just flattened to yuv420p (which crushes it).
	wantVF := tonemapSDRFilter() + ",scale=-2:'min(ih,1080)'"
	if !strings.Contains(joined, "-vf "+wantVF) {
		t.Fatalf("expected tone-map+scale -vf %q in: %s", wantVF, joined)
	}
	if !strings.Contains(joined, "-c:v libx264") || !strings.Contains(joined, "-pix_fmt yuv420p") {
		t.Fatalf("expected SDR libx264 encode: %s", joined)
	}
	// SDR output must not carry HDR color tags or x265 HDR params.
	for _, notWant := range []string{"-color_trc smpte2084", "-colorspace bt2020nc", "hdr-opt=1"} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("tone-mapped SDR output should not emit %q: %s", notWant, joined)
		}
	}
}

func TestFFmpegArgsSDRSourceOnSDRProfileSkipsTonemap(t *testing.T) {
	profile, _ := packageprofile.Lookup(packageprofile.DefaultName)
	probe := makeHDRProbe()
	probe.Streams[0].ColorTransfer = "bt709" // SDR source
	probe.Streams[0].ColorPrimaries = "bt709"
	probe.Streams[0].SideDataList = nil
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", probe, profile, nil, noBurn, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "tonemap") || strings.Contains(joined, "zscale") {
		t.Fatalf("SDR source must not be tone-mapped: %s", joined)
	}
	if !strings.Contains(joined, "-vf scale=-2:'min(ih,1080)'") {
		t.Fatalf("expected plain scale -vf for SDR source: %s", joined)
	}
}

func TestFFmpegArgsScalesHDRSourceAboveProfileCap(t *testing.T) {
	profile := testHEVCHDRProfile()
	probe := makeHDRProbe()
	probe.Streams[0].Height = 2161

	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "medium", probe, profile, nil, noBurn, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "scale=-2:'min(ih,2160)'") {
		t.Fatalf("expected scale filter for source above height cap: %s", joined)
	}
}

func TestValidateSourceForProfileRejectsDolbyVision5(t *testing.T) {
	profile := testHEVCHDRProfile()
	probe := makeHDRProbe()
	probe.Streams[0].CodecTagString = "dvhe" // Dolby Vision Profile 5
	err := validateSourceForProfile(probe, profile)
	if !errors.Is(err, ErrUnsupportedDolbyVision) {
		t.Fatalf("validateSourceForProfile(DV5) err = %v, want ErrUnsupportedDolbyVision", err)
	}
}

func TestMasteringDisplayParam(t *testing.T) {
	md, ok := masteringDisplayParam(makeHDRProbe().Streams[0].SideDataList)
	if !ok {
		t.Fatalf("masteringDisplayParam ok=false")
	}
	want := "G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,50)"
	if md != want {
		t.Fatalf("masteringDisplayParam=%q, want %q", md, want)
	}
	// Missing/garbled metadata yields no param rather than an invalid one.
	if _, ok := masteringDisplayParam(nil); ok {
		t.Fatalf("masteringDisplayParam(nil) should be false")
	}
	bad := []probeSideData{{SideDataType: "Mastering display metadata", RedX: "garbage"}}
	if _, ok := masteringDisplayParam(bad); ok {
		t.Fatalf("masteringDisplayParam(garbled) should be false")
	}
}
