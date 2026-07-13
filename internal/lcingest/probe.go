package lcingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tckrcr/linearcast/internal/codec"
)

// FFProbeFileForPath probes a single media file and returns its codec.Probe
// and duration in milliseconds. It is a lower-level alternative to IngestFile
// that doesn't write to the database; pair it with codec.Admit to apply the
// admission policy.
func FFProbeFileForPath(ctx context.Context, path string) (codec.Probe, int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return codec.Probe{}, 0, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return codec.Probe{}, 0, err
	}
	if st.IsDir() {
		return codec.Probe{}, 0, fmt.Errorf("%s is a directory", abs)
	}
	return ffprobeFile(ctx, abs)
}

// MediaIDFor generates a stable media ID for a file path.
func MediaIDFor(path string) string {
	return mediaIDFor(path)
}

// MusicProbeResult contains the results of probing a music file.
type MusicProbeResult struct {
	FormatName    string
	DurationMs    int64
	AudioCodec    string
	Tags          musicTags
	HasCueSidecar bool
}

// FFProbeMusicFileForPath probes a single music file and returns the result.
func FFProbeMusicFileForPath(ctx context.Context, path string) (MusicProbeResult, error) {
	mp, err := ffprobeMusicFile(ctx, path)
	if err != nil {
		return MusicProbeResult{}, err
	}
	return MusicProbeResult{
		FormatName:    mp.FormatName,
		DurationMs:    mp.DurationMs,
		AudioCodec:    mp.AudioCodec,
		Tags:          mp.Tags,
		HasCueSidecar: mp.HasCueSidecar,
	}, nil
}
