package packager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// TestProbePackageDecodableEndToEnd builds a real fMP4 HLS package with ffmpeg
// and runs the actual probe over it. It locks in the ffprobe contract the probe
// depends on: a healthy package passes, and a stub tail segment fails. The
// stub-tail case is the regression for the bug that motivated counting packets
// rather than reading stream metadata — a stream-metadata probe reports the
// codec from init.mp4's moov box even for a 0-byte segment and so passes it.
// Skips where ffmpeg/ffprobe are unavailable.
func TestProbePackageDecodableEndToEnd(t *testing.T) {
	ffmpeg, err := ffmpegexec.Resolve("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	if _, err := ffmpegexec.Resolve("ffprobe"); err != nil {
		t.Skipf("ffprobe unavailable: %v", err)
	}

	dir := t.TempDir()
	cmd := exec.Command(ffmpeg, "-v", "error",
		"-f", "lavfi", "-i", "testsrc=duration=4:size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		"-hls_time", "2", "-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(dir, "seg%06d.m4s"),
		"-hls_playlist_type", "vod", filepath.Join(dir, "stream.m3u8"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg package: %v: %s", err, out)
	}

	initPath := filepath.Join(dir, "init.mp4")
	pkg := db.MediaPackage{
		ID: "p-e2e", MediaID: "m-e2e", RenditionProfile: "test",
		PackageRoot: &dir, InitSegmentPath: &initPath,
	}
	profile := packageprofile.Profile{
		MediaKind: packageprofile.MediaKindVideo,
		Video:     packageprofile.VideoSettings{Mode: packageprofile.VideoModeTranscode},
	}

	if rep := ProbePackageDecodable(context.Background(), pkg, profile); !rep.OK {
		t.Fatalf("healthy package: OK=false reason=%q", rep.Reason)
	}

	segs, err := filepath.Glob(filepath.Join(dir, "seg*.m4s"))
	if err != nil || len(segs) == 0 {
		t.Fatalf("glob segments: %v (n=%d)", err, len(segs))
	}
	if err := os.Truncate(segs[len(segs)-1], 0); err != nil {
		t.Fatalf("truncate tail: %v", err)
	}
	if rep := ProbePackageDecodable(context.Background(), pkg, profile); rep.OK {
		t.Fatalf("emptied tail segment: expected probe failure, got OK")
	}
}
