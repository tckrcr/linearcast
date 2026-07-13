package packager

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// runFFmpegMusic packages an audio-only media file (FLAC, MP3, DSD, etc.) into
// fMP4 HLS by synthesising a dark background video track. Audio is encoded
// using the profile's audio settings; if the profile says copy, AAC 256k is
// used instead because most audio codecs (FLAC, DSD, PCM) are not valid in
// fMP4/HLS.
func runFFmpegMusic(ctx context.Context, input, packageRoot string, targetSegmentMs int64, profile packageprofile.Profile) error {
	absInput, err := filepath.Abs(input)
	if err != nil {
		return fmt.Errorf("resolve input path: %w", err)
	}
	absPackageRoot, err := filepath.Abs(packageRoot)
	if err != nil {
		return fmt.Errorf("resolve package root: %w", err)
	}

	args := ffmpegMusicArgs(absInput, absPackageRoot, targetSegmentMs, profile)
	cmd, err := ffmpegexec.CommandContext(ctx, "ffmpeg", args...)
	if err != nil {
		return err
	}
	cmd.Dir = absPackageRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ffmpegMusicArgs(input, packageRoot string, targetSegmentMs int64, profile packageprofile.Profile) []string {
	segmentPattern := filepath.Join(packageRoot, layout.SegmentPattern)
	targetSeconds := formatSeconds(targetSegmentMs)

	// NTSC frame rate for the synthetic video — gives the browser decoder enough
	// frames per segment to start playback without stalling on the first buffer.
	const synthNum, synthDen = 30000, 1001
	gopFrames := int(float64(synthNum)/synthDen*float64(targetSegmentMs)/1000 + 0.5)
	if gopFrames < 1 {
		gopFrames = synthNum / synthDen * 6
	}
	gop := strconv.Itoa(gopFrames)

	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		// Synthetic dark background video source at NTSC frame rate.
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x121212:s=1280x720:r=%d/%d", synthNum, synthDen),
		// Audio source.
		"-i", input,
		"-map", "0:v:0", "-map", "1:a:0",
		"-map_metadata", "-1", "-dn",
		// Video: libx264 with stillimage tuning — minimal bitrate for a static frame.
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "stillimage",
		"-crf", "35",
		"-pix_fmt", "yuv420p",
		"-bf", "0",
		"-g", gop,
		"-keyint_min", gop,
		"-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%s)", targetSeconds),
	}

	// Audio: use profile settings; fall back to AAC when profile says copy.
	if profile.Audio.Mode == packageprofile.AudioModeTranscode && profile.Audio.Codec != "" {
		args = append(args, "-c:a", profile.Audio.Codec)
		if profile.Audio.Bitrate != "" {
			args = append(args, "-b:a", profile.Audio.Bitrate)
		}
		if profile.Audio.SampleHz > 0 {
			args = append(args, "-ar", strconv.Itoa(profile.Audio.SampleHz))
		}
		if profile.Audio.Channels > 0 {
			args = append(args, "-ac", strconv.Itoa(profile.Audio.Channels))
		}
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "256k", "-ac", "2", "-ar", "48000")
	}

	args = append(args,
		"-shortest",
		"-f", "hls",
		"-hls_time", targetSeconds,
		"-hls_list_size", "0",
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", layout.InitName,
		"-hls_segment_filename", segmentPattern,
		layout.PlaylistPath(packageRoot),
	)
	return args
}
