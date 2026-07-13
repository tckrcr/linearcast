package packager

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/subtitlepolicy"
)

// LiveEncodingSpec describes one ephemeral on-demand encode: a single schedule
// entry's media, seeked to the playhead, paced at realtime (-readrate 1.0),
// producing the same fMP4 HLS layout as package encodes.
type LiveEncodingSpec struct {
	MediaPath string
	OutDir    string
	// SeekMs is the media position to start encoding from (the playhead).
	SeekMs int64
	// LimitMs, when > 0, stops the encode after this much media so a channel encoding
	// never encodes past its schedule entry's end. 0 encodes to EOF.
	LimitMs         int64
	TargetSegmentMs int64
	// RealtimePacing enables -readrate 1.0 for live encodings. BurstSec > 0 adds
	// -readrate_initial_burst so viewer-triggered encodes can fill their first
	// buffer faster; with BurstSec 0 an encode paces at realtime with no burst.
	RealtimePacing bool
	BurstSec       int
	Profile        packageprofile.Profile
	// BurnSubtitleStreamIndex is an absolute source subtitle stream index. Nil
	// disables subtitle burn-in.
	BurnSubtitleStreamIndex *int
	// SubtitleStreamIndexes are absolute source stream indexes to remux as
	// WebVTT HLS renditions alongside the main AV encoding.
	SubtitleStreamIndexes []int
}

// LiveEncodingArgs builds the ffmpeg command line for a live encoding, including
// optional WebVTT subtitle segment outputs for selected source subtitle tracks.
//
// Both transcode and copy video profiles are supported. Transcode re-keyframes
// on the target grid, so seeks are frame-accurate and segments are ~uniform.
// Copy (-c:v copy) cannot re-keyframe: the input seek snaps to the source
// keyframe at or before the playhead (a joining viewer starts up to one GOP
// early) and segment boundaries land on the source's existing keyframes, so
// durations are irregular and >= the target. That is intentional for copy
// profiles — the on-demand manifest numbers segments by encoding ordinal rather
// than media-time/grid (see appendOnDemandManifestItems), so irregular copy
// durations stay contiguous and stable across manifest refreshes. Source-codec
// compatibility for copy is enforced by validateSourceForProfile via the
// profile's CodecRequired. Unsupported video modes are rejected in ffmpegArgs.
func LiveEncodingArgs(ctx context.Context, spec LiveEncodingSpec) ([]string, error) {
	absInput, err := filepath.Abs(spec.MediaPath)
	if err != nil {
		return nil, fmt.Errorf("resolve input path: %w", err)
	}
	absOutDir, err := filepath.Abs(spec.OutDir)
	if err != nil {
		return nil, fmt.Errorf("resolve encoding dir: %w", err)
	}
	probe, err := probeSource(ctx, absInput)
	if err != nil {
		return nil, err
	}
	args, err := liveEncodingArgsFromProbe(absInput, absOutDir, spec, probe)
	if err != nil {
		return nil, err
	}
	// Encoder startup visibility: the exact ffmpeg invocation for this live
	// encoding. When a channel encoding never produces segments this is the
	// ground truth for what was actually launched (profile, seek, paths,
	// subtitle maps).
	slog.Info("live encoding ffmpeg args built",
		"profile", spec.Profile.Name,
		"video_mode", spec.Profile.Video.Mode,
		"media", absInput,
		"out_dir", absOutDir,
		"seek_ms", spec.SeekMs,
		"limit_ms", spec.LimitMs,
		"ffmpeg", strings.Join(args, " "),
	)
	return args, nil
}

func liveEncodingArgsFromProbe(absInput, absOutDir string, spec LiveEncodingSpec, probe sourceProbe) ([]string, error) {
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
	var inputArgs []string
	// Copy mode cannot re-keyframe, so input -ss snaps video to the prior source
	// keyframe but accurate seek would trim the transcoded audio to the requested
	// playhead. ffmpeg then rebases the early video keyframe to zero and pushes
	// audio forward by (SeekMs - prev_keyframe), writing that delta as a leading
	// audio edit-list empty edit. MSE/hls.js ignore fMP4 edit lists, so audio
	// plays that delta early for the whole encoding and the first fragment loads
	// with a buffer hole (bufferSeekOverHole). -noaccurate_seek makes both tracks
	// start together at the prior keyframe (playback begins up to one GOP before
	// the playhead), eliminating the skew. Transcode profiles re-keyframe at
	// output t=0 and keep frame-accurate seeking, so this is copy-mode only. It
	// must precede -ss to apply to this input's seek.
	if spec.Profile.Video.Mode == packageprofile.VideoModeCopy {
		inputArgs = append(inputArgs, "-noaccurate_seek")
	}
	inputArgs = append(inputArgs, "-ss", formatSeconds(seekMs))
	if spec.LimitMs > 0 {
		inputArgs = append(inputArgs, "-t", formatSeconds(spec.LimitMs))
	}
	if spec.RealtimePacing {
		// Live encodings pace at realtime. The initial burst, when enabled, fills
		// the demux buffer faster so the first segment arrives sooner, improving
		// time-to-first-frame for viewer-triggered on-demand encodes.
		inputArgs = append(inputArgs, "-readrate", "1.0")
		if spec.BurstSec > 0 {
			inputArgs = append(inputArgs, "-readrate_initial_burst", strconv.Itoa(spec.BurstSec))
		}
	}
	// Same fallback preset as package encodes (Options.Preset default). A live
	// encoding must sustain >= 1x realtime, so never fall through to x264's
	// "medium" default.
	// Live burn indexes come from the on-demand manager, which only offers
	// bitmap tracks, so the decision is always a bitmap overlay here.
	var burn subtitlepolicy.Decision
	if spec.BurnSubtitleStreamIndex != nil {
		burn = subtitlepolicy.Decision{
			Action:      subtitlepolicy.ActionBurn,
			StreamIndex: *spec.BurnSubtitleStreamIndex,
			Source:      db.TrackSourceEmbeddedBitmap,
		}
	}
	return ffmpegArgs(absInput, absOutDir, spec.TargetSegmentMs, "veryfast", probe, spec.Profile, inputArgs, burn, spec.SubtitleStreamIndexes)
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
