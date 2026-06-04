// Package packager creates normalized fMP4 HLS packages for one linearcast
// media row and writes package/segment metadata to SQLite.
package packager

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/lcingest"
	"github.com/tckrcr/linearcast/internal/packageid"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

const DefaultProfile = packageprofile.DefaultName

type Options struct {
	MediaPath  string
	Profile    string
	OutputRoot string
	// WorkDir, when non-empty, is a scratch directory on the same filesystem as
	// OutputRoot. Encoding happens under WorkDir/<packageID>/ and the finished
	// directory is renamed atomically into OutputRoot on success. Failed encodes
	// leave no debris in OutputRoot. If empty, encoding writes directly to
	// OutputRoot (legacy behaviour).
	WorkDir         string
	TargetSegmentMs int64
	Preset          string
	PackageID       string
	IngestMissing   bool
	NowMs           int64
	FailKind        string
	MaxAttempts     int
}

type FinalizeOptions struct {
	MediaPath  string
	MediaID    string
	Profile    string
	OutputRoot string
	PackageID  string
	NowMs      int64
}

type Result struct {
	PackageID        string
	MediaID          string
	RenditionProfile string
	PackageRoot      string
	InitSegmentPath  string
	SegmentCount     int
	DurationMs       int64
}

type probeStream struct {
	Index        int    `json:"index"`
	CodecType    string `json:"codec_type"`
	CodecName    string `json:"codec_name"`
	Profile      string `json:"profile"`
	Width        int64  `json:"width"`
	Height       int64  `json:"height"`
	AvgFrameRate string `json:"avg_frame_rate"`
	TimeBase     string `json:"time_base"`
	Disposition  struct {
		Default     int `json:"default"`
		Forced      int `json:"forced"`
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
	Tags struct {
		Language    string `json:"language"`
		Title       string `json:"title"`
		HandlerName string `json:"handler_name"`
	} `json:"tags"`
}

type sourceProbe struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []probeStream `json:"streams"`
}

type hlsSegment struct {
	URI        string
	DurationMs int64
}

// PackageOne packages one media file and records the resulting package
// metadata. It does not affect scheduling or playback.
func PackageOne(ctx context.Context, conn *sql.DB, opts Options) (Result, error) {
	if opts.MediaPath == "" {
		return Result{}, errors.New("media path is required")
	}
	if opts.OutputRoot == "" {
		return Result{}, errors.New("output root is required")
	}
	if opts.Profile == "" {
		opts.Profile = DefaultProfile
	}
	if opts.TargetSegmentMs <= 0 {
		opts.TargetSegmentMs = scheduler.TargetSegmentMs
	}
	if opts.Preset == "" {
		opts.Preset = "veryfast"
	}
	if opts.NowMs == 0 {
		opts.NowMs = time.Now().UTC().UnixMilli()
	}
	profile, err := db.GetPackageProfile(ctx, conn, opts.Profile)
	if err != nil {
		return Result{}, err
	}
	if profile == nil {
		return Result{}, fmt.Errorf("unknown or disabled package profile %q", strings.TrimSpace(opts.Profile))
	}
	profile.MediaKind = packageprofile.NormalizeMediaKind(profile.MediaKind)

	mediaPath, err := filepath.Abs(opts.MediaPath)
	if err != nil {
		return Result{}, err
	}
	media, err := db.MediaByPath(ctx, conn, mediaPath)
	if err != nil {
		return Result{}, fmt.Errorf("lookup media: %w", err)
	}
	if media == nil && opts.IngestMissing {
		if _, err := lcingest.IngestFile(ctx, conn, mediaPath, nil); err != nil {
			return Result{}, fmt.Errorf("ingest missing media: %w", err)
		}
		media, err = db.MediaByPath(ctx, conn, mediaPath)
		if err != nil {
			return Result{}, fmt.Errorf("lookup ingested media: %w", err)
		}
	}
	if media == nil {
		return Result{}, fmt.Errorf("media path %s is not in media table; run linearcast-ingest first or pass -ingest-missing", mediaPath)
	}

	packageID := opts.PackageID
	if packageID == "" {
		packageID = packageid.For(media.ID, opts.Profile)
	}
	packageRoot := filepath.Join(opts.OutputRoot, media.ID, opts.Profile)
	// encodeTarget is where ffmpeg writes. With WorkDir set it is a scratch dir
	// that gets renamed to packageRoot on success; otherwise it is packageRoot.
	encodeTarget := packageRoot
	if opts.WorkDir != "" {
		encodeTarget = filepath.Join(opts.WorkDir, packageID)
	}
	pkg := db.MediaPackage{
		ID:               packageID,
		MediaID:          media.ID,
		RenditionProfile: opts.Profile,
		Status:           db.PackageStatusProcessing,
		SegmentBasePath:  packageRoot,
		Container:        "fmp4",
		CreatedAtMs:      opts.NowMs,
		UpdatedAtMs:      opts.NowMs,
	}
	pr := packageRoot
	ip := filepath.Join(packageRoot, "init.mp4")
	pkg.PackageRoot = &pr
	pkg.InitSegmentPath = &ip
	if err := db.MarkPackageProcessing(ctx, conn, pkg); err != nil {
		return Result{}, fmt.Errorf("mark processing: %w", err)
	}
	mediaKind := packageprofile.MediaKindVideo
	if db.NormalizeMediaKind(media.MediaKind) == db.MediaKindMusic {
		mediaKind = packageprofile.MediaKindMusic
	}
	if mediaKind != profile.MediaKind {
		err := fmt.Errorf("media kind %s is not valid for profile %s (%s)", mediaKind, profile.Name, profile.MediaKind)
		recordFailure(ctx, conn, pkg, opts.NowMs, err, "terminal", opts.MaxAttempts)
		return Result{}, err
	}
	jobCtx, stopJob := context.WithCancel(ctx)
	defer stopJob()
	stopMonitor := monitorPackageCancellation(ctx, conn, packageID, stopJob)
	defer stopMonitor()

	if err := validateSourceFile(mediaPath); err != nil {
		err := fmt.Errorf("source file unavailable: %w", err)
		recordFailure(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
		return Result{}, err
	}

	// Clear any leftover scratch from a previous attempt at this package.
	if encodeTarget != packageRoot {
		if err := os.RemoveAll(encodeTarget); err != nil {
			recordFailure(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
			return Result{}, fmt.Errorf("clear work dir: %w", err)
		}
	} else {
		// No work dir: wipe the final location before encoding into it.
		if err := os.RemoveAll(packageRoot); err != nil {
			recordFailure(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
			return Result{}, fmt.Errorf("clear package root: %w", err)
		}
	}
	if err := os.MkdirAll(encodeTarget, 0o755); err != nil {
		recordFailure(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
		return Result{}, fmt.Errorf("create encode target: %w", err)
	}

	encodeErr := func() error {
		if db.NormalizeMediaKind(media.MediaKind) == db.MediaKindMusic {
			return runFFmpegMusic(jobCtx, mediaPath, encodeTarget, opts.TargetSegmentMs, *profile)
		}
		probe, err := probeSource(jobCtx, mediaPath)
		if err != nil {
			return err
		}
		if err := validateSourceForProfile(probe, *profile); err != nil {
			return err
		}
		return runFFmpeg(jobCtx, mediaPath, encodeTarget, opts.TargetSegmentMs, opts.Preset, probe, *profile)
	}()
	if encodeErr != nil {
		if encodeTarget != packageRoot {
			os.RemoveAll(encodeTarget)
		}
		recordFailureIfStillProcessing(ctx, conn, pkg, opts.NowMs, encodeErr, opts.FailKind, opts.MaxAttempts)
		return Result{}, encodeErr
	}

	// With a work dir, atomically promote the scratch dir to the final location.
	if encodeTarget != packageRoot {
		if err := os.MkdirAll(filepath.Dir(packageRoot), 0o755); err != nil {
			os.RemoveAll(encodeTarget)
			recordFailureIfStillProcessing(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
			return Result{}, fmt.Errorf("create package parent: %w", err)
		}
		if err := os.RemoveAll(packageRoot); err != nil {
			os.RemoveAll(encodeTarget)
			recordFailureIfStillProcessing(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
			return Result{}, fmt.Errorf("clear package root before rename: %w", err)
		}
		if err := os.Rename(encodeTarget, packageRoot); err != nil {
			os.RemoveAll(encodeTarget)
			recordFailureIfStillProcessing(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
			return Result{}, fmt.Errorf("promote to package root: %w", err)
		}
	}

	res, finalized, err := FinalizePackage(jobCtx, conn, FinalizeOptions{
		MediaPath:  mediaPath,
		MediaID:    media.ID,
		Profile:    opts.Profile,
		OutputRoot: opts.OutputRoot,
		PackageID:  packageID,
		NowMs:      opts.NowMs,
	})
	if err != nil {
		recordFailureIfStillProcessing(ctx, conn, pkg, opts.NowMs, err, opts.FailKind, opts.MaxAttempts)
		return Result{}, err
	}

	if changed, err := packageNoLongerProcessing(conn, packageID); err != nil {
		return Result{}, fmt.Errorf("check package state: %w", err)
	} else if changed {
		return Result{}, context.Canceled
	}

	// Mark ready only after segment metadata exists. Playback reads the ready
	// package row first, then packaged_segments; reversing this order can expose
	// manifests with missing segment rows.
	pkg.Status = db.PackageStatusReady
	applyFinalizedPackage(&pkg, finalized)
	pkg.Error = nil
	pkg.UpdatedAtMs = time.Now().UTC().UnixMilli()
	if err := db.MarkPackageReady(ctx, conn, pkg); err != nil {
		return Result{}, fmt.Errorf("mark ready: %w", err)
	}

	return res, nil
}

// EncodePackageOutput validates a source against profile and writes fMP4 HLS
// files into packageRoot. It does not touch the DB.
func EncodePackageOutput(ctx context.Context, mediaPath, packageRoot string, targetSegmentMs int64, preset string, profile packageprofile.Profile) error {
	if targetSegmentMs <= 0 {
		targetSegmentMs = scheduler.TargetSegmentMs
	}
	if preset == "" {
		preset = "veryfast"
	}
	if err := validateSourceFile(mediaPath); err != nil {
		return fmt.Errorf("source file unavailable: %w", err)
	}
	probe, err := probeSource(ctx, mediaPath)
	if err != nil {
		return err
	}
	if err := validateSourceForProfile(probe, profile); err != nil {
		return err
	}
	if err := os.RemoveAll(packageRoot); err != nil {
		return fmt.Errorf("clear package root: %w", err)
	}
	if err := os.MkdirAll(packageRoot, 0o755); err != nil {
		return fmt.Errorf("create package root: %w", err)
	}
	return runFFmpeg(ctx, mediaPath, packageRoot, targetSegmentMs, preset, probe, profile)
}

// FinalizePackage validates encoded HLS output, writes packaged_segments,
// extracts best-effort subtitle sidecars from the source media, and returns
// the metadata needed to mark the media package ready.
func FinalizePackage(ctx context.Context, conn *sql.DB, opts FinalizeOptions) (Result, db.FinalizedPackage, error) {
	if opts.Profile == "" {
		opts.Profile = DefaultProfile
	}
	if opts.OutputRoot == "" {
		return Result{}, db.FinalizedPackage{}, errors.New("output root is required")
	}
	if opts.MediaID == "" {
		return Result{}, db.FinalizedPackage{}, errors.New("media id is required")
	}
	if opts.PackageID == "" {
		opts.PackageID = packageid.For(opts.MediaID, opts.Profile)
	}
	if opts.NowMs == 0 {
		opts.NowMs = time.Now().UTC().UnixMilli()
	}
	packageRoot := filepath.Join(opts.OutputRoot, opts.MediaID, opts.Profile)
	playlist := filepath.Join(packageRoot, "stream.m3u8")
	segments, err := parseHLSManifest(playlist)
	if err != nil {
		return Result{}, db.FinalizedPackage{}, err
	}
	if len(segments) == 0 {
		return Result{}, db.FinalizedPackage{}, errors.New("packaged manifest contains no segments")
	}
	meta, err := probePackage(ctx, playlist)
	if err != nil {
		return Result{}, db.FinalizedPackage{}, err
	}

	var rows []db.PackagedSegment
	var curMs int64
	for i, seg := range segments {
		segPath := filepath.Join(packageRoot, filepath.FromSlash(seg.URI))
		rows = append(rows, db.PackagedSegment{
			PackageID:     opts.PackageID,
			SegmentNumber: int64(i),
			MediaStartMs:  curMs,
			DurationMs:    seg.DurationMs,
			Path:          &segPath,
		})
		curMs += seg.DurationMs
	}
	if err := db.ReplacePackagedSegments(ctx, conn, opts.PackageID, rows); err != nil {
		return Result{}, db.FinalizedPackage{}, fmt.Errorf("write packaged segments: %w", err)
	}

	if opts.MediaPath != "" {
		if probe, err := probeSource(ctx, opts.MediaPath); err == nil {
			prefs, _ := db.GetSubtitleLanguagePreference(ctx, conn)
			extractSubtitleTracks(ctx, conn, opts.MediaPath, opts.OutputRoot, opts.MediaID, probe, prefs)
		}
	}

	finalized := db.FinalizedPackage{
		PackageRoot:        nullString(packageRoot),
		InitSegmentPath:    nullString(filepath.Join(packageRoot, "init.mp4")),
		SegmentBasePath:    nullString(packageRoot),
		Container:          nullString("fmp4"),
		VideoCodec:         nullString(meta.VideoCodec),
		VideoProfile:       nullString(meta.VideoProfile),
		VideoWidth:         nullInt64(meta.VideoWidth),
		VideoHeight:        nullInt64(meta.VideoHeight),
		AudioCodec:         nullString(meta.AudioCodec),
		Timescale:          nullInt64(meta.Timescale),
		PackagedDurationMs: &curMs,
	}
	return Result{
		PackageID:        opts.PackageID,
		MediaID:          opts.MediaID,
		RenditionProfile: opts.Profile,
		PackageRoot:      packageRoot,
		InitSegmentPath:  filepath.Join(packageRoot, "init.mp4"),
		SegmentCount:     len(rows),
		DurationMs:       curMs,
	}, finalized, nil
}

func applyFinalizedPackage(pkg *db.MediaPackage, finalized db.FinalizedPackage) {
	pkg.PackageRoot = finalized.PackageRoot
	pkg.InitSegmentPath = finalized.InitSegmentPath
	if finalized.SegmentBasePath != nil {
		pkg.SegmentBasePath = *finalized.SegmentBasePath
	}
	if finalized.Container != nil {
		pkg.Container = *finalized.Container
	}
	if finalized.VideoCodec != nil {
		pkg.VideoCodec = *finalized.VideoCodec
	}
	if finalized.VideoProfile != nil {
		pkg.VideoProfile = *finalized.VideoProfile
	}
	pkg.VideoWidth = finalized.VideoWidth
	pkg.VideoHeight = finalized.VideoHeight
	if finalized.AudioCodec != nil {
		pkg.AudioCodec = *finalized.AudioCodec
	}
	if finalized.AudioProfile != nil {
		pkg.AudioProfile = *finalized.AudioProfile
	}
	pkg.Timescale = finalized.Timescale
	pkg.PackagedDurationMs = finalized.PackagedDurationMs
}

// BackfillSubtitleTracks probes mediaPath for subtitle streams and extracts
// them to VTT sidecars without re-encoding video. Safe to call on any media
// that already has a ready package. prefs is the ordered preference list
// (ISO 639-2 codes); pass nil to read from the DB.
func BackfillSubtitleTracks(ctx context.Context, conn *sql.DB, mediaID, mediaPath, outputRoot string, prefs []string) error {
	if prefs == nil {
		var err error
		prefs, err = db.GetSubtitleLanguagePreference(ctx, conn)
		if err != nil {
			return fmt.Errorf("read subtitle preferences: %w", err)
		}
	}
	probe, err := probeSource(ctx, mediaPath)
	if err != nil {
		return fmt.Errorf("ffprobe %s: %w", mediaPath, err)
	}
	extractSubtitleTracks(ctx, conn, mediaPath, outputRoot, mediaID, probe, prefs)
	return nil
}

func recordFailure(ctx context.Context, conn *sql.DB, pkg db.MediaPackage, nowMs int64, cause error, kind string, maxAttempts int) {
	// Preserve the package identity/path fields while moving the state machine
	// into the appropriate failure status so an operator can retry later.
	reason := ""
	if cause != nil {
		reason = cause.Error()
	}
	if kind == "" {
		kind = "terminal"
	}
	_, _ = db.MarkPackageFailedWithKind(ctx, conn, pkg.ID, kind, reason, maxAttempts, nowMs)
}

func recordFailureIfStillProcessing(ctx context.Context, conn *sql.DB, pkg db.MediaPackage, nowMs int64, cause error, kind string, maxAttempts int) {
	changed, err := packageNoLongerProcessing(conn, pkg.ID)
	if err == nil && changed {
		return
	}
	recordFailure(ctx, conn, pkg, nowMs, cause, kind, maxAttempts)
}

func packageNoLongerProcessing(conn *sql.DB, packageID string) (bool, error) {
	current, err := db.MediaPackageByID(context.Background(), conn, packageID)
	if err != nil {
		return false, err
	}
	return current == nil || current.Status != db.PackageStatusProcessing, nil
}

func monitorPackageCancellation(ctx context.Context, conn *sql.DB, packageID string, cancel context.CancelFunc) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				changed, err := packageNoLongerProcessing(conn, packageID)
				if err == nil && changed {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(done)
	}
}

func validateSourceFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}

func validateSourceForProfile(probe sourceProbe, profile packageprofile.Profile) error {
	selected := selectSourceStreams(probe)
	if selected.Video == nil {
		return fmt.Errorf("source has no video stream for profile %s", profile.Name)
	}
	if selected.Audio == nil {
		return fmt.Errorf("source has no audio stream for profile %s", profile.Name)
	}
	s := *selected.Video
	if profile.Video.CodecRequired != "" && strings.ToLower(s.CodecName) != profile.Video.CodecRequired {
		return fmt.Errorf("source video codec %q is not valid for profile %s; requires %s", s.CodecName, profile.Name, profile.Video.CodecRequired)
	}
	return nil
}

func runFFmpeg(ctx context.Context, input, packageRoot string, targetSegmentMs int64, preset string, probe sourceProbe, profile packageprofile.Profile) error {
	// cmd.Dir = packageRoot below, so resolve both paths against the caller's
	// cwd before ffmpeg's cwd changes underneath it. Otherwise relative
	// packageRoot args (init.mp4 / seg*.m4s / stream.m3u8) get re-resolved
	// against the new cwd and ffmpeg writes into a directory that doesn't exist.
	absInput, err := filepath.Abs(input)
	if err != nil {
		return fmt.Errorf("resolve input path: %w", err)
	}
	absPackageRoot, err := filepath.Abs(packageRoot)
	if err != nil {
		return fmt.Errorf("resolve package root: %w", err)
	}
	args, err := ffmpegArgs(absInput, absPackageRoot, targetSegmentMs, preset, probe, profile)
	if err != nil {
		return err
	}
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

func ffmpegArgs(input, packageRoot string, targetSegmentMs int64, preset string, probe sourceProbe, profile packageprofile.Profile) ([]string, error) {
	segmentPattern := filepath.Join(packageRoot, "seg%06d.m4s")
	targetSeconds := formatSeconds(targetSegmentMs)
	selected := selectSourceStreams(probe)
	if selected.Video == nil {
		return nil, fmt.Errorf("source has no video stream for profile %s", profile.Name)
	}
	if selected.Audio == nil {
		return nil, fmt.Errorf("source has no audio stream for profile %s", profile.Name)
	}
	gopFrames := gopFramesForStream(*selected.Video, targetSegmentMs)
	gop := strconv.Itoa(gopFrames)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
	}
	if profile.Video.Mode == packageprofile.VideoModeCopy {
		args = append(args, "-fflags", "+genpts")
	}
	args = append(args,
		"-i", input,
		"-map", fmt.Sprintf("0:%d", selected.Video.Index),
		"-map", fmt.Sprintf("0:%d", selected.Audio.Index),
		"-map_metadata", "-1",
		"-dn",
	)
	switch profile.Video.Mode {
	case packageprofile.VideoModeTranscode:
		args = append(args, "-c:v", profile.Video.Codec)
		// VideoToolbox codecs don't accept libx264-style -preset or -crf; they
		// use -q:v on a 0–100 scale instead. Skip preset/crf for them and let
		// the worker fall back to software encoding if the hwaccel session fails.
		isVideoToolbox := strings.HasSuffix(profile.Video.Codec, "_videotoolbox")
		if !isVideoToolbox {
			effectivePreset := profile.Video.Preset
			if effectivePreset == "" {
				effectivePreset = preset
			}
			if effectivePreset != "" {
				args = append(args, "-preset", effectivePreset)
			}
			if profile.Video.CRF > 0 {
				args = append(args, "-crf", strconv.Itoa(profile.Video.CRF))
			}
		} else {
			if profile.Video.VideoQuality > 0 {
				args = append(args, "-q:v", strconv.Itoa(profile.Video.VideoQuality))
			}
		}
		if profile.Video.VideoBitrate != "" {
			args = append(args, "-b:v", profile.Video.VideoBitrate)
		}
		if profile.Video.VideoMaxBitrate != "" {
			// bufsize = 2× maxrate is standard CBR-ish VBV for HLS delivery.
			args = append(args, "-maxrate", profile.Video.VideoMaxBitrate, "-bufsize", doubleRate(profile.Video.VideoMaxBitrate))
		}
		if profile.Video.Profile != "" {
			args = append(args, "-profile:v", profile.Video.Profile)
		}
		if profile.Video.Level != "" {
			args = append(args, "-level:v", profile.Video.Level)
		}
		args = append(args, "-pix_fmt", "yuv420p")
		if profile.Video.ScaleHeight > 0 {
			// Scale down sources taller than ScaleHeight; leave shorter sources unchanged.
			// -2 keeps width as a multiple of 2 after the height is pinned.
			args = append(args, "-vf", fmt.Sprintf("scale=-2:'min(ih,%d)'", profile.Video.ScaleHeight))
		}
		// HLS requires segment boundaries to land on keyframes — always emit.
		args = append(args,
			"-g", gop,
			"-keyint_min", gop,
			"-sc_threshold", "0",
			"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%s)", targetSeconds),
		)
	case packageprofile.VideoModeCopy:
		args = append(args, "-c:v", "copy")
	default:
		return nil, fmt.Errorf("unsupported video mode %q for profile %s", profile.Video.Mode, profile.Name)
	}

	switch profile.Audio.Mode {
	case packageprofile.AudioModeTranscode:
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
	case packageprofile.AudioModeCopy:
		args = append(args, "-c:a", "copy")
	default:
		return nil, fmt.Errorf("unsupported audio mode %q for profile %s", profile.Audio.Mode, profile.Name)
	}

	args = append(args,
		"-f", "hls",
		"-hls_time", targetSeconds,
		"-hls_list_size", "0",
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", segmentPattern,
		filepath.Join(packageRoot, "stream.m3u8"),
	)
	return args, nil
}

type packageMeta struct {
	VideoCodec   string
	VideoProfile string
	VideoWidth   int64
	VideoHeight  int64
	AudioCodec   string
	Timescale    int64
}

func probePackage(ctx context.Context, playlist string) (packageMeta, error) {
	raw, err := probeJSON(ctx, playlist)
	if err != nil {
		return packageMeta{}, err
	}
	var meta packageMeta
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if meta.VideoCodec == "" || s.Disposition.Default == 1 {
				meta.VideoCodec = strings.ToLower(s.CodecName)
				meta.VideoProfile = s.Profile
				meta.VideoWidth = s.Width
				meta.VideoHeight = s.Height
				meta.Timescale = timescaleFromTimeBase(s.TimeBase)
			}
		case "audio":
			if meta.AudioCodec == "" || s.Disposition.Default == 1 {
				meta.AudioCodec = strings.ToLower(s.CodecName)
			}
		}
	}
	if meta.VideoCodec == "" {
		return packageMeta{}, errors.New("packaged output has no video stream")
	}
	if meta.AudioCodec == "" {
		return packageMeta{}, errors.New("packaged output has no audio stream")
	}
	return meta, nil
}

func probeSource(ctx context.Context, path string) (sourceProbe, error) {
	raw, err := probeJSON(ctx, path)
	if err != nil {
		return sourceProbe{}, err
	}
	return raw, nil
}

func probeJSON(ctx context.Context, path string) (sourceProbe, error) {
	cmd, err := ffmpegexec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	if err != nil {
		return sourceProbe{}, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return sourceProbe{}, fmt.Errorf("ffprobe %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	var raw sourceProbe
	if err := json.Unmarshal(out, &raw); err != nil {
		return sourceProbe{}, fmt.Errorf("parse ffprobe %s: %w", path, err)
	}
	return raw, nil
}

type selectedSourceStreams struct {
	Video *probeStream
	Audio *probeStream
}

func selectSourceStreams(probe sourceProbe) selectedSourceStreams {
	var selected selectedSourceStreams
	for i := range probe.Streams {
		s := &probe.Streams[i]
		switch s.CodecType {
		case "video":
			if s.Disposition.AttachedPic == 1 {
				continue
			}
			if selected.Video == nil || s.Disposition.Default == 1 {
				selected.Video = s
			}
		case "audio":
			if selected.Audio == nil || audioStreamScore(*s) > audioStreamScore(*selected.Audio) {
				selected.Audio = s
			}
		}
	}
	return selected
}

func audioStreamScore(s probeStream) int {
	score := 0
	if s.Disposition.Default == 1 {
		score += 100
	}
	switch normalizeLanguageTag(s.Tags.Language) {
	case "eng", "en":
		score += 50
	case "", "und":
		score += 20
	}
	if isCommentaryOrDescriptiveAudio(s) {
		score -= 200
	}
	return score
}

func normalizeLanguageTag(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		lang = lang[:i]
	}
	return lang
}

func isCommentaryOrDescriptiveAudio(s probeStream) bool {
	text := strings.ToLower(s.Tags.Title + " " + s.Tags.HandlerName)
	for _, marker := range []string{
		"commentary",
		"director's comments",
		"director comments",
		"audio description",
		"descriptive audio",
		"descriptive",
		"visually impaired",
		"hearing impaired",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func gopFramesFor(probe sourceProbe, targetSegmentMs int64) int {
	selected := selectSourceStreams(probe)
	if selected.Video != nil {
		return gopFramesForStream(*selected.Video, targetSegmentMs)
	}
	return 144
}

func gopFramesForStream(stream probeStream, targetSegmentMs int64) int {
	fps := 24000.0 / 1001.0
	if parsed, ok := parseRatio(stream.AvgFrameRate); ok && parsed > 0 {
		fps = parsed
	}
	frames := int(fps*float64(targetSegmentMs)/1000.0 + 0.5)
	if frames < 1 {
		return 144
	}
	return frames
}

func parseHLSManifest(path string) ([]hlsSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []hlsSegment
	var pendingDuration int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF:") {
			v := strings.TrimPrefix(line, "#EXTINF:")
			v = strings.TrimSuffix(v, ",")
			ms, err := parseSecondsToMs(v)
			if err != nil {
				return nil, fmt.Errorf("parse EXTINF %q: %w", line, err)
			}
			pendingDuration = ms
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if pendingDuration <= 0 {
			return nil, fmt.Errorf("segment URI %q without preceding EXTINF", line)
		}
		out = append(out, hlsSegment{URI: line, DurationMs: pendingDuration})
		pendingDuration = 0
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseSecondsToMs(v string) (int64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, err
	}
	return int64(f*1000 + 0.5), nil
}

// doubleRate returns twice the numeric value of a bitrate string like "6000k"
// or "2000k", used to set -bufsize = 2× -maxrate for VBV HLS delivery.
func doubleRate(rate string) string {
	rate = strings.TrimSpace(rate)
	if rate == "" {
		return ""
	}
	suffix := ""
	num := rate
	if len(rate) > 0 {
		last := rate[len(rate)-1]
		if last == 'k' || last == 'K' || last == 'm' || last == 'M' || last == 'g' || last == 'G' {
			suffix = string(last)
			num = rate[:len(rate)-1]
		}
	}
	v, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return rate // pass through unchanged if we can't parse it
	}
	return strconv.FormatInt(v*2, 10) + suffix
}

func formatSeconds(ms int64) string {
	whole := ms / 1000
	frac := ms % 1000
	if frac == 0 {
		return strconv.FormatInt(whole, 10)
	}
	return fmt.Sprintf("%d.%03d", whole, frac)
}

func parseRatio(v string) (float64, bool) {
	parts := strings.Split(v, "/")
	if len(parts) != 2 {
		return 0, false
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0, false
	}
	return num / den, true
}

func timescaleFromTimeBase(v string) int64 {
	parts := strings.Split(v, "/")
	if len(parts) != 2 || parts[0] != "1" {
		return 0
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// extractSubtitleTracks converts text-based subtitle streams to WebVTT sidecars
// and records them in media_tracks. Only streams whose language is in prefs are
// extracted. Bitmap formats are skipped. Already-extracted streams (DB row +
// file on disk) are skipped — making the function idempotent on re-runs.
// Each stream has a 2-minute timeout so a hung ffmpeg can't block the worker.
func extractSubtitleTracks(ctx context.Context, conn *sql.DB, mediaPath, outputRoot, mediaID string, probe sourceProbe, prefs []string) {
	prefSet := make(map[string]bool, len(prefs))
	for _, p := range prefs {
		prefSet[strings.ToLower(p)] = true
	}

	// Build index of already-extracted streams: stream_index -> vttPath.
	existing := make(map[int]string)
	if tracks, err := db.MediaTracksByMediaID(ctx, conn, mediaID); err == nil {
		for _, t := range tracks {
			if t.Kind == "subtitle" && t.Source == db.TrackSourceEmbedded && t.Path != nil {
				existing[t.StreamIndex] = *t.Path
			}
		}
	}

	mediaDir := filepath.Join(outputRoot, mediaID)
	var subtitleRelIdx int
	for _, s := range probe.Streams {
		if s.CodecType != "subtitle" {
			continue
		}
		lang := s.Tags.Language
		if lang == "" {
			lang = "und"
		}
		if isBitmapSubtitle(s.CodecName) {
			// Bitmap subs (PGS/VOBSUB) cannot be converted to a VTT sidecar.
			// Record an inventory row (path stays NULL) so the track is visible —
			// admin UI, and the future forced-burn-in / external-fetch slices —
			// instead of silently dropping it. Recorded regardless of language
			// preference: the point of the inventory is to surface everything we
			// cannot currently serve. Idempotent via the embedded upsert.
			track := db.MediaTrack{
				MediaID:     mediaID,
				Kind:        "subtitle",
				StreamIndex: s.Index,
				Language:    lang,
				Codec:       strings.ToLower(s.CodecName),
				Source:      db.TrackSourceEmbeddedBitmap,
				DefaultFlag: s.Disposition.Default == 1,
				Forced:      s.Disposition.Forced == 1,
				Path:        nil,
			}
			if err := db.UpsertMediaTrack(ctx, conn, track); err != nil {
				fmt.Fprintf(os.Stderr, "packager: record bitmap subtitle stream %d (%s) media=%s: %v\n",
					s.Index, lang, mediaID, err)
			} else {
				fmt.Fprintf(os.Stderr, "packager: bitmap subtitle stream %d (%s, %s) not extractable to text; recorded as inventory (forced=%v) media=%s\n",
					s.Index, lang, strings.ToLower(s.CodecName), s.Disposition.Forced == 1, mediaID)
			}
			subtitleRelIdx++
			continue
		}

		// Skip languages not in the preference list (empty prefs = extract all).
		if len(prefSet) > 0 && !prefSet[strings.ToLower(lang)] {
			subtitleRelIdx++
			continue
		}

		// Skip if already extracted and file still exists.
		if existingPath, done := existing[s.Index]; done {
			if _, err := os.Stat(existingPath); err == nil {
				subtitleRelIdx++
				continue
			}
		}

		vttPath := filepath.Join(mediaDir, fmt.Sprintf("sub_%d_%s.vtt", s.Index, lang))
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		cmd, err := ffmpegexec.CommandContext(sctx, "ffmpeg",
			"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
			"-i", mediaPath,
			"-map", fmt.Sprintf("0:s:%d", subtitleRelIdx),
			"-c:s", "webvtt",
			vttPath,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "packager: subtitle stream %d (%s) media=%s: %v\n",
				s.Index, lang, mediaID, err)
			cancel()
			subtitleRelIdx++
			continue
		}
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "packager: subtitle stream %d (%s) media=%s: %v: %s\n",
				s.Index, lang, mediaID, err, strings.TrimSpace(string(out)))
			subtitleRelIdx++
			continue
		}
		track := db.MediaTrack{
			MediaID:     mediaID,
			Kind:        "subtitle",
			StreamIndex: s.Index,
			Language:    lang,
			Codec:       "webvtt",
			Source:      db.TrackSourceEmbedded,
			DefaultFlag: s.Disposition.Default == 1,
			Path:        &vttPath,
		}
		if err := db.UpsertMediaTrack(ctx, conn, track); err != nil {
			fmt.Fprintf(os.Stderr, "packager: upsert subtitle track stream=%d media=%s: %v\n",
				s.Index, mediaID, err)
		}
		subtitleRelIdx++
	}
}

func isBitmapSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "dvd_subtitle", "dvb_subtitle", "hdmv_pgs_subtitle", "pgssub", "xsub":
		return true
	}
	return false
}

func nullString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func nullInt64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
