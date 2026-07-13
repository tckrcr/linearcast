package packager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/scheduler"
	"github.com/tckrcr/linearcast/internal/subtitlepolicy"
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

func makeAVProbeWithSubtitle(streamIndex int, language string, forced bool) sourceProbe {
	probe := makeAVProbe()
	sub := makeStream("subtitle", "hdmv_pgs_subtitle")
	sub.Index = streamIndex
	sub.Tags.Language = language
	if forced {
		sub.Disposition.Forced = 1
	}
	probe.Streams = append(probe.Streams, sub)
	return probe
}

// noBurn is the no-op burn decision for ffmpegArgs calls that don't exercise
// subtitle burn-in.
var noBurn subtitlepolicy.Decision

func makeAVProbeWithTextSubtitles(forced bool) sourceProbe {
	probe := makeAVProbe()
	ara := makeStream("subtitle", "subrip")
	ara.Index = 2
	ara.Tags.Language = "ara"
	eng := makeStream("subtitle", "subrip")
	eng.Index = 3
	eng.Tags.Language = "eng"
	if forced {
		eng.Disposition.Forced = 1
	}
	probe.Streams = append(probe.Streams, ara, eng)
	return probe
}

func requireFFmpegTools(t *testing.T) {
	t.Helper()
	if _, err := ffmpegexec.Resolve("ffmpeg"); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	if _, err := ffmpegexec.Resolve("ffprobe"); err != nil {
		t.Skipf("ffprobe unavailable: %v", err)
	}
}

func generateTinySource(t *testing.T, path string) {
	t.Helper()
	cmd, err := ffmpegexec.CommandContext(context.Background(), "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=128x72:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac",
		path,
	)
	if err != nil {
		t.Fatalf("resolve ffmpeg: %v", err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func TestResolveSubtitleDecisionForcedBurn(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	got := resolveSubtitleDecision(profile, makeAVProbeWithSubtitle(4, "eng", true))
	if got.Action != subtitlepolicy.ActionBurn || got.StreamIndex != 4 || got.Source != db.TrackSourceEmbeddedBitmap {
		t.Fatalf("forced bitmap decision = %+v, want burn stream 4 bitmap", got)
	}
	got = resolveSubtitleDecision(profile, makeAVProbeWithTextSubtitles(true))
	if got.Action != subtitlepolicy.ActionBurn || got.StreamIndex != 3 || got.Source != db.TrackSourceEmbedded {
		t.Fatalf("forced text decision = %+v, want burn stream 3 text", got)
	}
	if got := resolveSubtitleDecision(profile, makeAVProbeWithSubtitle(4, "eng", false)); got.Action == subtitlepolicy.ActionBurn {
		t.Fatalf("non-forced decision = %+v, want no burn", got)
	}
	copyProfile, ok := packageprofile.Lookup(packageprofile.HEVCCopySourceName)
	if !ok {
		t.Fatalf("missing copy profile")
	}
	if got := resolveSubtitleDecision(copyProfile, makeAVProbeWithSubtitle(4, "eng", true)); got.Action == subtitlepolicy.ActionBurn {
		t.Fatalf("copy profile decision = %+v, want no burn", got)
	}
}

func TestFFmpegArgsBurnForcedSubtitleForTranscodeProfile(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	probe := makeAVProbeWithSubtitle(4, "eng", true)
	decision := resolveSubtitleDecision(profile, probe)
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", probe, profile, nil, decision, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-filter_complex [0:0][0:4]overlay=eof_action=pass") {
		t.Fatalf("args missing subtitle burn filter: %s", joined)
	}
	if strings.Contains(joined, "-map 0:0") {
		t.Fatalf("args mapped raw video instead of burn filter output: %s", joined)
	}
}

func TestFFmpegArgsBurnForcedTextSubtitle(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	probe := makeAVProbeWithTextSubtitles(true)
	decision := resolveSubtitleDecision(profile, probe)
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", probe, profile, nil, decision, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	// si=1: the forced eng track is the second subtitle stream in the source.
	want := "-filter_complex [0:0]scale=-2:'min(ih,1080)',subtitles=filename=/in.mkv:si=1,format=yuv420p[v]"
	if !strings.Contains(joined, want) {
		t.Fatalf("args missing text burn filter %q: %s", want, joined)
	}
	if strings.Contains(joined, "overlay") {
		t.Fatalf("text burn used bitmap overlay: %s", joined)
	}
	if strings.Contains(joined, "-map 0:0") {
		t.Fatalf("args mapped raw video instead of burn filter output: %s", joined)
	}
	if strings.Contains(joined, "-pix_fmt") {
		t.Fatalf("burn filter output should pin format in-filter, not via -pix_fmt: %s", joined)
	}
}

func TestFFmpegArgsBurnForcedTextSubtitleTonemapsHDRSource(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	probe := makeAVProbeWithTextSubtitles(true)
	probe.Streams[0].ColorTransfer = "smpte2084"
	decision := resolveSubtitleDecision(profile, probe)
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", probe, profile, nil, decision, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	tonemapIdx := strings.Index(joined, "zscale=t=linear")
	subtitlesIdx := strings.Index(joined, "subtitles=filename=")
	if tonemapIdx < 0 {
		t.Fatalf("HDR text burn missing SDR tone-map: %s", joined)
	}
	if subtitlesIdx < tonemapIdx {
		t.Fatalf("text must render after the tone-map, got: %s", joined)
	}
}

func TestSubtitlesFilterPath(t *testing.T) {
	got := subtitlesFilterPath(`/tmp/cache/Tom's file [4K], part 1: intro.mkv`)
	want := `/tmp/cache/Tom\\\'s file \[4K\]\, part 1\\: intro.mkv`
	if got != want {
		t.Fatalf("subtitlesFilterPath = %q, want %q", got, want)
	}
}

func TestExtractSubtitleTracksUsesPackageSidecarPath(t *testing.T) {
	conn, err := db.OpenReadWrite(newWorkerTestDB(t))
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`INSERT INTO media
		(id, path, directory, duration_ms, container, video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 1000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media_packages
		(id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
		VALUES ('pkg', 'm1', 'prof', 'processing', 0, 0)`); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	root := t.TempDir()
	legacyDir := filepath.Join(root, "cache", "subtitles", "m1")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacyPath := filepath.Join(legacyDir, "s2.vtt")
	if err := os.WriteFile(legacyPath, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nlegacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if err := db.UpsertPackageTrack(context.Background(), conn, db.PackageTrack{
		PackageID:   "pkg",
		Kind:        "subtitle",
		StreamIndex: 2,
		Language:    "eng",
		Codec:       "webvtt",
		Source:      db.TrackSourceEmbedded,
		Path:        &legacyPath,
	}); err != nil {
		t.Fatalf("upsert legacy track: %v", err)
	}

	packageRoot := filepath.Join(root, "cache", "packages", "m1", "prof")
	subtitleDir := layout.PackageSubtitleDir(packageRoot)
	wantPath := filepath.Join(subtitleDir, layout.SubtitleFileName(2))
	if err := os.MkdirAll(subtitleDir, 0o755); err != nil {
		t.Fatalf("mkdir canonical: %v", err)
	}
	if err := os.WriteFile(wantPath, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\ncanonical\n"), 0o644); err != nil {
		t.Fatalf("write canonical: %v", err)
	}

	sub := makeStream("subtitle", "subrip")
	sub.Index = 2
	sub.Tags.Language = "eng"
	probe := sourceProbe{Streams: []probeStream{sub}}

	extractSubtitleTracks(context.Background(), conn, "/tmp/m1.mkv", packageRoot, "pkg", probe, []string{"eng"})

	gotBytes, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read canonical sidecar: %v", err)
	}
	if !strings.Contains(string(gotBytes), "canonical") {
		t.Fatalf("canonical sidecar was unexpectedly overwritten:\n%s", gotBytes)
	}

	tracks, err := db.PackageTracksByPackageID(context.Background(), conn, "pkg")
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Path == nil || *tracks[0].Path != wantPath {
		t.Fatalf("track path = %+v, want %s", tracks, wantPath)
	}
}

func TestPackagedDurationShortfall(t *testing.T) {
	cases := []struct {
		name      string
		packaged  int64
		source    int64
		wantTrunc bool
	}{
		{"sub-frame slack ok", 1638626, 1638637, false},  // 11ms short (clean re-encode)
		{"truncated by minutes", 1182215, 1638637, true}, // killed encode: ~7.5min short
		{"exact", 1638637, 1638637, false},
		{"packaged longer than source", 1638700, 1638637, false}, // never short
		{"unknown source disables check", 1182215, 0, false},
		{"under 0.5pct tolerance", 1638637 - 8000, 1638637, false},
		{"over 0.5pct tolerance", 1638637 - 9000, 1638637, true},
		{"short file under abs tolerance", 60000 - 1500, 60000, false},
		{"short file over abs tolerance", 60000 - 3000, 60000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, truncated := PackagedDurationShortfall(tc.packaged, tc.source); truncated != tc.wantTrunc {
				t.Fatalf("PackagedDurationShortfall(%d, %d) truncated=%v, want %v",
					tc.packaged, tc.source, truncated, tc.wantTrunc)
			}
		})
	}
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

	got, err := ParseHLSManifest(manifest)
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
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", makeAVProbe(), profile, nil, noBurn, nil)
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
		"-g 48",
		"-force_key_frames expr:gte(t,n_forced*2)",
		"-c:a aac",
		"-b:a 256k",
		"-ac 2",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}

func TestFFmpegArgsForPackagedTranscodeProfileUsesDurableCadence(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatalf("missing default profile")
	}
	args, err := ffmpegArgs("/in.mkv", "/out", PackagedSegmentMs, "veryfast", makeAVProbe(), profile, nil, noBurn, nil)
	if err != nil {
		t.Fatalf("ffmpeg args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-g 144",
		"-keyint_min 144",
		"-force_key_frames expr:gte(t,n_forced*6)",
		"-hls_time 6",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("packaged args missing %q: %s", want, joined)
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
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", probe, profile, nil, noBurn, nil)
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
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", makeAVProbe(), profile, nil, noBurn, nil)
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
	args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", makeAVProbe(), profile, nil, noBurn, nil)
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
		args, err := ffmpegArgs("/in.mkv", "/out", scheduler.TargetSegmentMs, "veryfast", makeAVProbe(), profile, nil, noBurn, nil)
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
			"-force_key_frames expr:gte(t,n_forced*2)",
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
	pkg, err := db.MediaPackageByID(context.Background(), conn, "m-missing-source-h264-1080p-8mbps")
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

func TestPackageOneUsesProfileRootWhenPackageIDHasLegacySubtitleIdentity(t *testing.T) {
	requireFFmpegTools(t)
	dbPath := newWorkerTestDB(t)
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	mediaPath := filepath.Join(t.TempDir(), "source.mp4")
	generateTinySource(t, mediaPath)
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', ?, ?, 2000, 'mp4', 'h264', 72, 'aac', 1, 0)`, mediaPath, filepath.Dir(mediaPath)); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	outputRoot := t.TempDir()
	legacyPackageID := layout.ID("m1", DefaultProfile) + "-burn-forced-disposition-s2-eng"
	res, err := PackageOne(context.Background(), conn, Options{
		MediaPath:       mediaPath,
		OutputRoot:      outputRoot,
		Profile:         DefaultProfile,
		PackageID:       legacyPackageID,
		TargetSegmentMs: 1000,
		NowMs:           500,
	})
	if err != nil {
		t.Fatalf("PackageOne: %v", err)
	}

	wantRoot := layout.PackageRoot(outputRoot, "m1", DefaultProfile)
	legacyRoot := filepath.Join(outputRoot, "m1", legacyPackageID)
	if res.PackageID != legacyPackageID {
		t.Fatalf("result package id=%q, want %q", res.PackageID, legacyPackageID)
	}
	if res.PackageRoot != wantRoot || res.InitSegmentPath != layout.InitPath(wantRoot) {
		t.Fatalf("result paths root=%q init=%q, want root=%q init=%q", res.PackageRoot, res.InitSegmentPath, wantRoot, layout.InitPath(wantRoot))
	}
	if _, err := os.Stat(wantRoot); err != nil {
		t.Fatalf("canonical profile root missing: %v", err)
	}
	if _, err := os.Stat(legacyRoot); !os.IsNotExist(err) {
		t.Fatalf("legacy package-id root exists: %s stat err=%v", legacyRoot, err)
	}

	pkg, err := db.MediaPackageByID(context.Background(), conn, legacyPackageID)
	if err != nil || pkg == nil {
		t.Fatalf("lookup package: pkg=%v err=%v", pkg, err)
	}
	if pkg.PackageRoot == nil || *pkg.PackageRoot != wantRoot {
		t.Fatalf("package root=%+v, want %s", pkg.PackageRoot, wantRoot)
	}
	if pkg.InitSegmentPath == nil || *pkg.InitSegmentPath != layout.InitPath(wantRoot) {
		t.Fatalf("init path=%+v, want %s", pkg.InitSegmentPath, layout.InitPath(wantRoot))
	}
	segs, err := db.PackagedSegments(context.Background(), conn, legacyPackageID)
	if err != nil {
		t.Fatalf("segments: %v", err)
	}
	if len(segs) == 0 {
		t.Fatal("no packaged segments recorded")
	}
	for _, seg := range segs {
		if seg.Path == nil {
			t.Fatalf("segment path nil: %+v", seg)
		}
		if !strings.HasPrefix(filepath.Clean(*seg.Path), wantRoot+string(os.PathSeparator)) {
			t.Fatalf("segment path %q not under canonical root %q", *seg.Path, wantRoot)
		}
		if strings.HasPrefix(filepath.Clean(*seg.Path), legacyRoot+string(os.PathSeparator)) {
			t.Fatalf("segment path %q under legacy package-id root %q", *seg.Path, legacyRoot)
		}
	}
}

func TestValidateSourceFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := validateSourceFile(dir); err == nil {
		t.Fatalf("directory source should be rejected")
	}
}
