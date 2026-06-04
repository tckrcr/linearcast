package packager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func makeStream(codecType, codecName string) probeStream {
	return probeStream{CodecType: codecType, CodecName: codecName}
}

func makeAVProbe() sourceProbe {
	video := makeStream("video", "h264")
	video.Index = 0
	video.AvgFrameRate = "24000/1001"
	audio := makeStream("audio", "aac")
	audio.Index = 1
	audio.Tags.Language = "eng"
	return sourceProbe{Streams: []probeStream{video, audio}}
}

func TestParseHLSManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "stream.m3u8")
	if err := os.WriteFile(manifest, []byte(`#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:7
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.006,
segments/seg000000.m4s
#EXTINF:5.964,
segments/seg000001.m4s
#EXT-X-ENDLIST
`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	got, err := parseHLSManifest(manifest)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 segments, got %+v", got)
	}
	if got[0].URI != "segments/seg000000.m4s" || got[0].DurationMs != 6006 {
		t.Fatalf("segment 0 mismatch: %+v", got[0])
	}
	if got[1].URI != "segments/seg000001.m4s" || got[1].DurationMs != 5964 {
		t.Fatalf("segment 1 mismatch: %+v", got[1])
	}
}

func TestGOPFramesForSourceRate(t *testing.T) {
	probe := sourceProbe{}
	probe.Streams = append(probe.Streams, makeStream("video", ""))
	probe.Streams[0].AvgFrameRate = "24000/1001"

	if got := gopFramesFor(probe, 6000); got != 144 {
		t.Fatalf("gopFramesFor 23.976 = %d, want 144", got)
	}
	probe.Streams[0].AvgFrameRate = "25/1"
	if got := gopFramesFor(probe, 6000); got != 150 {
		t.Fatalf("gopFramesFor 25 = %d, want 150", got)
	}
}

func TestParseSecondsToMs(t *testing.T) {
	got, err := parseSecondsToMs("6.006")
	if err != nil {
		t.Fatalf("parse seconds: %v", err)
	}
	if got != 6006 {
		t.Fatalf("got %d, want 6006", got)
	}
}

func TestFFmpegArgsForTranscodeProfile(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	args, err := ffmpegArgs("/in.mkv", "/out", 6000, "veryfast", makeAVProbe(), profile)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v libx264",
		"-preset veryfast",
		"-profile:v main",
		"-level:v 4.1",
		"-maxrate 8000k",
		"-bufsize 16000k",
		"-pix_fmt yuv420p",
		"-g 144",
		"-force_key_frames expr:gte(t,n_forced*6)",
		"-c:a aac",
		"-b:a 192k",
		"-ac 2",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}

func TestFFmpegMusicArgsDisableBFrames(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.MusicName)
	if !ok {
		t.Fatalf("missing music profile")
	}
	args := ffmpegMusicArgs("/in.flac", "/out", 6000, profile)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f lavfi -i color=c=0x121212:s=1280x720:r=30000/1001",
		"-c:v libx264",
		"-tune stillimage",
		"-bf 0",
		"-force_key_frames expr:gte(t,n_forced*6)",
		"-c:a aac",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("music args missing %q: %s", want, joined)
		}
	}
}

func TestFFmpegArgsSelectsMainAudioTrack(t *testing.T) {
	probe := makeAVProbe()
	probe.Streams[1].Index = 3
	probe.Streams[1].Tags.Title = "Director Commentary"
	probe.Streams[1].Disposition.Default = 1
	main := makeStream("audio", "ac3")
	main.Index = 4
	main.Tags.Language = "eng"
	probe.Streams = append(probe.Streams, main)

	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	args, err := ffmpegArgs("/in.mkv", "/out", 6000, "veryfast", probe, profile)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:0 -map 0:4") {
		t.Fatalf("args should map selected main audio stream: %s", joined)
	}
}

func TestFFmpegArgsForCopyProfileCopiesVideo(t *testing.T) {
	profile := packageprofile.Profile{
		Name: "custom-copy-1080p",
		Video: packageprofile.VideoSettings{
			Mode:          packageprofile.VideoModeCopy,
			CodecRequired: "h264",
		},
		Audio: packageprofile.AudioSettings{
			Mode:    packageprofile.AudioModeTranscode,
			Codec:   "aac",
			Bitrate: "192k",
		},
	}
	args, err := ffmpegArgs("/in.mkv", "/out", 6000, "veryfast", makeAVProbe(), profile)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v copy",
		"-fflags +genpts",
		"-c:a aac",
		"-b:a 192k",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	for _, notWant := range []string{"-force_key_frames", "-g 144", "-profile:v main"} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("source args should not contain %q: %s", notWant, joined)
		}
	}
}

func TestFFmpegArgsForVideoToolboxProfile(t *testing.T) {
	profile := packageprofile.Profile{
		Name: "custom-videotoolbox-1080p",
		Video: packageprofile.VideoSettings{
			Mode:         packageprofile.VideoModeTranscode,
			Codec:        "h264_videotoolbox",
			Profile:      "main",
			VideoQuality: 65,
		},
		Audio: packageprofile.AudioSettings{
			Mode:     packageprofile.AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "192k",
			Channels: 2,
			SampleHz: 48000,
		},
	}
	args, err := ffmpegArgs("/in.mkv", "/out", 6000, "veryfast", makeAVProbe(), profile)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v h264_videotoolbox",
		"-q:v 65",
		"-profile:v main",
		"-pix_fmt yuv420p",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	for _, notWant := range []string{"-preset", "-crf", "-allow_sw"} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("videotoolbox args should not contain %q: %s", notWant, joined)
		}
	}
}

func TestFFmpegArgsForScaledBitrateProfiles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		height      int64
		wantScale   string
		bitrate     string
		maxrate     string
		wantBitrate string
		wantMaxrate string
		wantBufsize string
	}{
		{"custom-main-720p", 720, "scale=-2:'min(ih,720)'", "4500k", "6000k", "-b:v 4500k", "-maxrate 6000k", "-bufsize 12000k"},
		{"custom-main-480p", 480, "scale=-2:'min(ih,480)'", "1500k", "2000k", "-b:v 1500k", "-maxrate 2000k", "-bufsize 4000k"},
	} {
		profile := packageprofile.Profile{
			Name: tc.name,
			Video: packageprofile.VideoSettings{
				Mode:            packageprofile.VideoModeTranscode,
				Codec:           "libx264",
				Profile:         "main",
				ScaleHeight:     tc.height,
				VideoBitrate:    tc.bitrate,
				VideoMaxBitrate: tc.maxrate,
			},
			Audio: packageprofile.AudioSettings{
				Mode:     packageprofile.AudioModeTranscode,
				Codec:    "aac",
				Bitrate:  "192k",
				Channels: 2,
				SampleHz: 48000,
			},
		}
		args, err := ffmpegArgs("/in.mkv", "/out", 6000, "veryfast", makeAVProbe(), profile)
		if err != nil {
			t.Fatalf("%s ffmpeg args: %v", tc.name, err)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{
			tc.wantScale,
			tc.wantBitrate,
			tc.wantMaxrate,
			tc.wantBufsize,
			"-c:v libx264",
			"-force_key_frames expr:gte(t,n_forced*6)",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s: args missing %q:\n  %s", tc.name, want, joined)
			}
		}
	}
}

func TestDoubleRate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"6000k", "12000k"},
		{"2000k", "4000k"},
		{"1500k", "3000k"},
		{"8M", "16M"},
	} {
		if got := doubleRate(tc.in); got != tc.want {
			t.Fatalf("doubleRate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateSourceForCopyProfile(t *testing.T) {
	profile := packageprofile.Profile{
		Name: "custom-copy-1080p",
		Video: packageprofile.VideoSettings{
			Mode:          packageprofile.VideoModeCopy,
			CodecRequired: "h264",
		},
	}
	probe := sourceProbe{}
	s := makeStream("video", "h264")
	s.Height = 1080
	probe.Streams = append(probe.Streams, s)
	audio := makeStream("audio", "aac")
	audio.Index = 1
	probe.Streams = append(probe.Streams, audio)

	if err := validateSourceForProfile(probe, profile); err != nil {
		t.Fatalf("valid source rejected: %v", err)
	}
	probe.Streams[0].CodecName = "hevc"
	if err := validateSourceForProfile(probe, profile); err == nil {
		t.Fatalf("hevc source should be rejected")
	}
}

func TestPackageOneMissingSourceFailsWithoutClearingExistingPackageRoot(t *testing.T) {
	dbPath := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	missingSource := filepath.Join(t.TempDir(), "missing.mkv")
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m-missing-source', ?, '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`, missingSource); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	outputRoot := t.TempDir()
	packageRoot := filepath.Join(outputRoot, "m-missing-source", DefaultProfile)
	if err := os.MkdirAll(packageRoot, 0o755); err != nil {
		t.Fatalf("mkdir package root: %v", err)
	}
	sentinel := filepath.Join(packageRoot, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("old package content"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	_, err = PackageOne(context.Background(), conn, Options{
		MediaPath:  missingSource,
		OutputRoot: outputRoot,
		Profile:    DefaultProfile,
		NowMs:      500,
	})
	if err == nil || !strings.Contains(err.Error(), "source file unavailable") {
		t.Fatalf("PackageOne err=%v, want source file unavailable", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel should not be removed before source validation: %v", err)
	}
	pkg, err := db.MediaPackageByID(context.Background(), conn, "m-missing-source-h264-main-1080p")
	if err != nil || pkg == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", pkg, err)
	}
	if pkg.Status != db.PackageStatusFailed || pkg.Error == nil || !strings.Contains(*pkg.Error, "source file unavailable") {
		t.Fatalf("package after missing source=%+v, want failed with source error", pkg)
	}
	if pkg.PackageRoot == nil || *pkg.PackageRoot != packageRoot {
		t.Fatalf("package root=%+v, want %s", pkg.PackageRoot, packageRoot)
	}
}

func TestValidateSourceFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := validateSourceFile(dir); err == nil {
		t.Fatalf("directory source should be rejected")
	}
}
