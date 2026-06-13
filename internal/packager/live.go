package packager

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// LiveSessionSpec describes one ephemeral on-demand encode: a single schedule
// entry's media, seeked to the playhead, paced at realtime after an initial
// burst, producing the same fMP4 HLS layout as package encodes.
type LiveSessionSpec struct {
	MediaPath string
	OutDir    string
	// SeekMs is the media position to start encoding from (the playhead).
	SeekMs int64
	// LimitMs, when > 0, stops the encode after this much media so a session
	// never encodes past its schedule entry's end. 0 encodes to EOF.
	LimitMs         int64
	TargetSegmentMs int64
	// BurstSec > 0 adds -readrate 1.0 -readrate_initial_burst: the first
	// BurstSec seconds encode as fast as the host allows (covering the manifest
	// look-ahead), the rest is paced at realtime, bounding CPU and disk. Pass 0
	// on ffmpeg builds without -readrate_initial_burst (< 6.0); the encode then
	// runs uncapped.
	BurstSec int
	Profile  packageprofile.Profile
	// BurnSubtitleStreamIndex is an absolute source stream index from
	// media_tracks.stream_index. Nil disables subtitle burn-in.
	BurnSubtitleStreamIndex *int
}

// LiveSessionArgs builds the ffmpeg command line for a live session.
//
// Both transcode and copy video profiles are supported. Transcode re-keyframes
// on the target grid, so seeks are frame-accurate and segments are ~uniform.
// Copy (-c:v copy) cannot re-keyframe: the input seek snaps to the source
// keyframe at or before the playhead (a joining viewer starts up to one GOP
// early) and segment boundaries land on the source's existing keyframes, so
// durations are irregular and >= the target. That is intentional for copy
// profiles — the on-demand manifest numbers segments by session ordinal rather
// than media-time/grid (see appendOnDemandManifestItems), so irregular copy
// durations stay contiguous and stable across manifest refreshes. Source-codec
// compatibility for copy is enforced by validateSourceForProfile via the
// profile's CodecRequired. Unsupported video modes are rejected in ffmpegArgs.
func LiveSessionArgs(ctx context.Context, spec LiveSessionSpec) ([]string, error) {
	absInput, err := filepath.Abs(spec.MediaPath)
	if err != nil {
		return nil, fmt.Errorf("resolve input path: %w", err)
	}
	absOutDir, err := filepath.Abs(spec.OutDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session dir: %w", err)
	}
	probe, err := probeSource(ctx, absInput)
	if err != nil {
		return nil, err
	}
	return liveSessionArgsFromProbe(absInput, absOutDir, spec, probe)
}

func liveSessionArgsFromProbe(absInput, absOutDir string, spec LiveSessionSpec, probe sourceProbe) ([]string, error) {
	if err := validateSourceForProfile(probe, spec.Profile); err != nil {
		return nil, err
	}
	if spec.BurnSubtitleStreamIndex != nil && spec.Profile.Video.Mode == packageprofile.VideoModeCopy {
		return nil, fmt.Errorf("subtitle burn-in requires a transcode video profile, got copy profile %s", spec.Profile.Name)
	}
	seekMs := spec.SeekMs
	if seekMs < 0 {
		seekMs = 0
	}
	// Input seeking with a decode (transcode mode) is frame-accurate: ffmpeg
	// seeks to the prior keyframe and discards frames up to the requested time.
	inputArgs := []string{"-ss", formatSeconds(seekMs)}
	if spec.LimitMs > 0 {
		inputArgs = append(inputArgs, "-t", formatSeconds(spec.LimitMs))
	}
	if spec.BurstSec > 0 {
		inputArgs = append(inputArgs,
			"-readrate", "1.0",
			"-readrate_initial_burst", strconv.Itoa(spec.BurstSec),
		)
	}
	// Same fallback preset as package encodes (Options.Preset default). A live
	// session must sustain >= 1x realtime, so never fall through to x264's
	// "medium" default.
	burnSubtitleStreamIndex := -1
	if spec.BurnSubtitleStreamIndex != nil {
		burnSubtitleStreamIndex = *spec.BurnSubtitleStreamIndex
	}
	return ffmpegArgs(absInput, absOutDir, spec.TargetSegmentMs, "veryfast", probe, spec.Profile, inputArgs, burnSubtitleStreamIndex)
}

// SupportsReadrateBurst reports whether the resolved ffmpeg binary understands
// -readrate_initial_burst (added in ffmpeg 6.0). Callers should pass BurstSec=0
// when it does not.
func SupportsReadrateBurst(ctx context.Context) bool {
	cmd, err := ffmpegexec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-h", "full")
	if err != nil {
		return false
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "readrate_initial_burst")
}
