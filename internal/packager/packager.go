// Package packager creates normalized fMP4 HLS packages for one linearcast
// media row and writes package/segment metadata to SQLite.
package packager

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/lcingest"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/subtitlepolicy"
)

const (
	DefaultProfile = packageprofile.DefaultName
	// PackagedSegmentMs is the durable package segment/GOP target. Live
	// on-demand encodes pass scheduler.TargetSegmentMs explicitly for faster
	// startup; offline packages favor encoder efficiency and smaller playlists.
	PackagedSegmentMs int64 = 6000
)

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
	// ProgressFunc, when non-nil, is called with a 0–99 percentage as ffmpeg
	// advances through the source. The caller drives this from a heartbeat
	// goroutine so progress reaches the DB/SSE stream without blocking the encode.
	ProgressFunc func(int)
}

type FinalizeOptions struct {
	MediaPath  string
	MediaID    string
	Profile    string
	OutputRoot string
	PackageID  string
	NowMs      int64
	// SourceDurationMs is the ingested source media duration. When > 0,
	// FinalizePackage rejects output that falls materially short of it (a
	// truncated or killed encode). Zero disables the check.
	SourceDurationMs int64
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
	Index          int    `json:"index"`
	CodecType      string `json:"codec_type"`
	CodecName      string `json:"codec_name"`
	CodecTagString string `json:"codec_tag_string"`
	Profile        string `json:"profile"`
	Width          int64  `json:"width"`
	Height         int64  `json:"height"`
	// ColorTransfer / ColorPrimaries are ffprobe's color_transfer /
	// color_primaries fields, used to detect HDR (PQ/HLG) sources so the
	// transcode path can preserve HDR instead of flattening it to SDR.
	ColorTransfer  string          `json:"color_transfer"`
	ColorPrimaries string          `json:"color_primaries"`
	SideDataList   []probeSideData `json:"side_data_list"`
	AvgFrameRate   string          `json:"avg_frame_rate"`
	TimeBase       string          `json:"time_base"`
	Disposition    struct {
		Default         int `json:"default"`
		Forced          int `json:"forced"`
		AttachedPic     int `json:"attached_pic"`
		HearingImpaired int `json:"hearing_impaired"`
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

// HLSSegment is one segment entry parsed from an HLS media playlist: the
// segment URI and its exact #EXTINF duration.
type HLSSegment struct {
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
		opts.TargetSegmentMs = PackagedSegmentMs
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
		packageID = layout.ID(media.ID, opts.Profile)
	}
	packageRoot := layout.PackageRoot(opts.OutputRoot, media.ID, opts.Profile)
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
	ip := layout.InitPath(packageRoot)
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
		return runFFmpeg(jobCtx, mediaPath, encodeTarget, opts.TargetSegmentMs, opts.Preset, probe, *profile, opts.ProgressFunc)
	}()
	if encodeErr != nil {
		if encodeTarget != packageRoot {
			os.RemoveAll(encodeTarget)
		}
		// Unsupported-source errors (e.g. Dolby Vision Profile 5) are terminal:
		// retrying re-probes the same file and fails identically, so don't burn
		// attempts. The existing codec-failed gating then excludes it from
		// scheduling.
		failKind := opts.FailKind
		if errors.Is(encodeErr, ErrUnsupportedDolbyVision) {
			failKind = "terminal"
		}
		recordFailureIfStillProcessing(ctx, conn, pkg, opts.NowMs, encodeErr, failKind, opts.MaxAttempts)
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
		MediaPath:        mediaPath,
		MediaID:          media.ID,
		Profile:          opts.Profile,
		OutputRoot:       opts.OutputRoot,
		PackageID:        packageID,
		NowMs:            opts.NowMs,
		SourceDurationMs: media.DurationMs,
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
		targetSegmentMs = PackagedSegmentMs
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
	return runFFmpeg(ctx, mediaPath, packageRoot, targetSegmentMs, preset, probe, profile, nil)
}

const (
	// durationShortfallToleranceMs is the largest absolute gap between source
	// and packaged duration tolerated before an encode is treated as truncated.
	durationShortfallToleranceMs = 2000
	// durationShortfallToleranceDenom expresses proportional slack (1/200 =
	// 0.5% of source) tolerated on longer media, whichever bound is larger.
	durationShortfallToleranceDenom = 200
)

// PackagedDurationShortfall reports how far a packaged duration falls short of
// the source duration and whether that shortfall is large enough to treat the
// encode as truncated. Encodes legitimately drop a sub-frame tail to land on a
// GOP boundary, so a small absolute/proportional slack is tolerated; a larger
// gap means output was cut off (e.g. the encoder was killed mid-run) and must
// not be finalized as ready. A non-positive sourceMs disables the check.
//
// FinalizePackage uses this as a finalize guard; CheckReadyPackageIntegrity and
// the maint audit-duration command reuse it so detection can never drift from
// the guard.
func PackagedDurationShortfall(packagedMs, sourceMs int64) (shortfallMs int64, truncated bool) {
	if sourceMs <= 0 {
		return 0, false
	}
	shortfallMs = sourceMs - packagedMs
	if shortfallMs <= 0 {
		return shortfallMs, false
	}
	return shortfallMs, shortfallMs > durationShortfallTolerance(sourceMs)
}

// durationShortfallTolerance is the largest shortfall (source minus packaged)
// tolerated for sourceMs before an encode is treated as truncated: the larger of
// a fixed absolute floor and a proportional bound. Shared by the finalize guard
// and the audit so both report against the identical threshold.
func durationShortfallTolerance(sourceMs int64) int64 {
	tolerance := int64(durationShortfallToleranceMs)
	if frac := sourceMs / durationShortfallToleranceDenom; frac > tolerance {
		tolerance = frac
	}
	return tolerance
}

// FinalizePackage validates encoded HLS output — including a guard that the
// packaged duration covers the source duration, so a truncated or killed encode
// is rejected rather than silently finalized as ready — writes
// packaged_segments, extracts best-effort package-owned subtitle sidecars from
// the source media, and returns the metadata needed to mark the media package
// ready.
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
		opts.PackageID = layout.ID(opts.MediaID, opts.Profile)
	}
	if opts.NowMs == 0 {
		opts.NowMs = time.Now().UTC().UnixMilli()
	}
	packageRoot := layout.PackageRoot(opts.OutputRoot, opts.MediaID, opts.Profile)
	playlist := layout.PlaylistPath(packageRoot)
	segments, err := ParseHLSManifest(playlist)
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
	var packageBytes int64
	for i, seg := range segments {
		segPath := filepath.Join(packageRoot, filepath.FromSlash(seg.URI))
		if info, statErr := os.Stat(segPath); statErr == nil {
			packageBytes += info.Size()
		}
		rows = append(rows, db.PackagedSegment{
			PackageID:     opts.PackageID,
			SegmentNumber: int64(i),
			MediaStartMs:  curMs,
			DurationMs:    seg.DurationMs,
			Path:          &segPath,
		})
		curMs += seg.DurationMs
	}
	// The init segment carries the moov/codec config and is part of the package's
	// footprint. Best-effort: a stat failure just under-counts rather than failing
	// the finalize (size is informational, never a finalize gate).
	if info, statErr := os.Stat(layout.InitPath(packageRoot)); statErr == nil {
		packageBytes += info.Size()
	}
	if shortfall, truncated := PackagedDurationShortfall(curMs, opts.SourceDurationMs); truncated {
		return Result{}, db.FinalizedPackage{}, fmt.Errorf(
			"packaged duration %dms is %dms short of source %dms; encode likely truncated",
			curMs, shortfall, opts.SourceDurationMs)
	}
	if err := db.ReplacePackagedSegments(ctx, conn, opts.PackageID, rows); err != nil {
		return Result{}, db.FinalizedPackage{}, fmt.Errorf("write packaged segments: %w", err)
	}

	if opts.MediaPath != "" {
		ExtractSubtitles(ctx, conn, opts.MediaPath, packageRoot, opts.PackageID)
		if n, err := packageSubtitleBytes(layout.PackageSubtitleDir(packageRoot)); err == nil {
			packageBytes += n
		}
	}

	finalized := db.FinalizedPackage{
		PackageRoot:        nullString(packageRoot),
		InitSegmentPath:    nullString(layout.InitPath(packageRoot)),
		SegmentBasePath:    nullString(packageRoot),
		Container:          nullString("fmp4"),
		VideoCodec:         nullString(meta.VideoCodec),
		VideoProfile:       nullString(meta.VideoProfile),
		VideoWidth:         nullInt64(meta.VideoWidth),
		VideoHeight:        nullInt64(meta.VideoHeight),
		AudioCodec:         nullString(meta.AudioCodec),
		Timescale:          nullInt64(meta.Timescale),
		PackagedDurationMs: &curMs,
		PackageBytes:       nullInt64(packageBytes),
	}
	return Result{
		PackageID:        opts.PackageID,
		MediaID:          opts.MediaID,
		RenditionProfile: opts.Profile,
		PackageRoot:      packageRoot,
		InitSegmentPath:  layout.InitPath(packageRoot),
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
	pkg.PackageBytes = finalized.PackageBytes
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
	// The failing encode was often aborted by cancelling ctx (lease lost,
	// shutdown). The status transition must still land, or the row stays
	// 'processing' with no lease — invisible to the sweeper until the next
	// worker restart drains it.
	ctx = context.WithoutCancel(ctx)
	if _, err := db.MarkPackageFailedWithKind(ctx, conn, pkg.ID, kind, reason, maxAttempts, nowMs); err != nil {
		log.Printf("WARN record package failure pkg=%s kind=%s: %v (row may be stuck in processing)", pkg.ID, kind, err)
	}
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
	if isDolbyVisionProfile5Stream(s) {
		return fmt.Errorf("%w: source codec tag %q", ErrUnsupportedDolbyVision, s.CodecTagString)
	}
	if profile.Video.CodecRequired != "" && strings.ToLower(s.CodecName) != profile.Video.CodecRequired {
		return fmt.Errorf("source video codec %q is not valid for profile %s; requires %s", s.CodecName, profile.Name, profile.Video.CodecRequired)
	}
	return nil
}

func resolveSubtitleDecision(profile packageprofile.Profile, probe sourceProbe) subtitlepolicy.Decision {
	return subtitlepolicy.Resolve(subtitlepolicy.Request{
		Mode:     subtitlepolicy.Mode(profile.Subtitles.Mode),
		Language: profile.Subtitles.Language,
	}, profile, subtitleTracksFromProbe(probe))
}

func subtitleTracksFromProbe(probe sourceProbe) []db.PackageTrack {
	tracks := make([]db.PackageTrack, 0)
	for _, s := range probe.Streams {
		if s.CodecType != "subtitle" {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(s.Tags.Language))
		if lang == "" {
			lang = "und"
		}
		source := db.TrackSourceEmbedded
		if isBitmapSubtitle(s.CodecName) {
			source = db.TrackSourceEmbeddedBitmap
		}
		tracks = append(tracks, db.PackageTrack{
			Kind:            "subtitle",
			StreamIndex:     s.Index,
			Language:        lang,
			Title:           s.Tags.Title,
			Codec:           strings.ToLower(s.CodecName),
			Source:          source,
			DefaultFlag:     s.Disposition.Default == 1,
			Forced:          s.Disposition.Forced == 1,
			HearingImpaired: s.Disposition.HearingImpaired == 1,
		})
	}
	return tracks
}

func runFFmpeg(ctx context.Context, input, packageRoot string, targetSegmentMs int64, preset string, probe sourceProbe, profile packageprofile.Profile, progressFn func(int)) error {
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
	// MKV exposes HDR10 mastering/CLL only as frame side data, not in
	// -show_streams, so backfill it from the first frame before building args so
	// an HDR-preserving encode can carry it. Best-effort; skipped for SDR.
	enrichHDRSideData(ctx, &probe, absInput)
	subtitleDecision := resolveSubtitleDecision(profile, probe)
	if profile.Subtitles.Mode != "" {
		log.Printf("INFO package subtitle decision profile=%s action=%s reason=%s stream_index=%d language=%s",
			profile.Name, subtitleDecision.Action, subtitleDecision.Reason, subtitleDecision.StreamIndex, subtitleDecision.Language)
	}
	args, err := ffmpegArgs(absInput, absPackageRoot, targetSegmentMs, preset, probe, profile, nil, subtitleDecision, nil)
	if err != nil {
		return err
	}
	if progressFn != nil {
		// -progress pipe:1 writes key=value progress lines to stdout each time
		// ffmpeg completes a frame batch; stderr continues to carry errors.
		args = append([]string{"-progress", "pipe:1"}, args...)
	}
	cmd, err := ffmpegexec.CommandContext(ctx, "ffmpeg", args...)
	if err != nil {
		return err
	}
	cmd.Dir = absPackageRoot

	if progressFn == nil {
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}
	totalUs := probeDurationUs(probe)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "out_time_us=") {
			continue
		}
		us, err := strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64)
		if err != nil || totalUs <= 0 || us < 0 {
			continue
		}
		pct := int(us * 100 / totalUs)
		if pct > 99 {
			pct = 99
		}
		progressFn(pct)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// probeDurationUs returns the source duration in microseconds from the probe
// format block. Returns 0 when the duration is absent or unparseable.
func probeDurationUs(probe sourceProbe) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int64(f * 1_000_000)
}

// ffmpegArgs builds the package-encode command line. inputArgs, when non-nil,
// is spliced in directly before -i (input options such as -ss seeking or
// -readrate pacing for live encodings); package encodes pass nil. Subtitle
// stream indexes are optional HLS side outputs for live encodings.
func ffmpegArgs(input, packageRoot string, targetSegmentMs int64, preset string, probe sourceProbe, profile packageprofile.Profile, inputArgs []string, burn subtitlepolicy.Decision, subtitleStreamIndexes []int) ([]string, error) {
	segmentPattern := filepath.Join(packageRoot, layout.SegmentPattern)
	targetSeconds := formatSeconds(targetSegmentMs)
	selected := selectSourceStreams(probe)
	if selected.Video == nil {
		slog.Warn("ffmpeg args: source has no usable video stream", "profile", profile.Name, "input", input)
		return nil, fmt.Errorf("source has no video stream for profile %s", profile.Name)
	}
	if selected.Audio == nil {
		slog.Warn("ffmpeg args: source has no usable audio stream", "profile", profile.Name, "input", input)
		return nil, fmt.Errorf("source has no audio stream for profile %s", profile.Name)
	}
	gopFrames := gopFramesForStream(*selected.Video, targetSegmentMs)
	gop := strconv.Itoa(gopFrames)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
	}
	if profile.Video.Mode == packageprofile.VideoModeCopy {
		args = append(args, "-fflags", "+genpts", "-copytb", "1")
	}
	args = append(args, inputArgs...)
	args = append(args, "-i", input)
	burning := burn.Action == subtitlepolicy.ActionBurn
	if burning {
		args = append(args, "-filter_complex", burnFilter(input, selected.Video, burn, probe, profile), "-map", "[v]")
	} else {
		args = append(args, "-map", fmt.Sprintf("0:%d", selected.Video.Index))
	}
	args = append(args,
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
		// HDR-preserving transcode: an HDR (PQ/HLG) source encoded by an HEVC
		// profile keeps its 10-bit BT.2020 signal instead of being flattened to
		// SDR. Subtitle burn-in composites in yuv420p (SDR), so it is excluded.
		preserveHDR := !burning && sourceIsHDR(selected.Video) && isHEVCEncoder(profile.Video.Codec)
		// An SDR transcode (e.g. libx264) of an HDR source must tone-map, not just
		// flatten the pixel format: copying PQ/HLG luma into yuv420p without a
		// linear-light conversion yields a dark, desaturated picture. preserveHDR
		// (HEVC HDR profiles) keeps HDR instead, so the two are mutually exclusive.
		tonemapSDR := !burning && sourceIsHDR(selected.Video) && !preserveHDR
		if !burning {
			pixFmt := profile.Video.PixelFormat
			if pixFmt == "" {
				pixFmt = "yuv420p"
			}
			args = append(args, "-pix_fmt", pixFmt)
		}
		if !burning {
			var filters []string
			if tonemapSDR {
				filters = append(filters, tonemapSDRFilter())
			}
			if shouldScaleVideo(selected.Video, profile.Video.ScaleHeight) {
				// Scale down sources taller than ScaleHeight; leave unknown-height
				// sources conservative. -2 keeps width as a multiple of 2 after
				// the height is pinned.
				filters = append(filters, fmt.Sprintf("scale=-2:'min(ih,%d)'", profile.Video.ScaleHeight))
			}
			if len(filters) > 0 {
				args = append(args, "-vf", strings.Join(filters, ","))
			}
		}
		if preserveHDR {
			args = append(args, hdrColorArgs(selected.Video.ColorTransfer)...)
			if strings.Contains(strings.ToLower(profile.Video.Codec), "x265") {
				args = append(args, "-x265-params", hevcHDRx265Params(gopFrames, selected.Video))
			}
		}
		// Apple clients only accept HEVC fMP4 tagged hvc1 (not hev1).
		if isHEVCEncoder(profile.Video.Codec) {
			args = append(args, "-tag:v", "hvc1")
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
		if selected.Video != nil && selected.Video.CodecName == "hevc" {
			args = append(args, "-tag:v", "hvc1")
		}
		args = append(args, "-muxdelay", "0", "-muxpreload", "0")
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
		"-hls_fmp4_init_filename", layout.InitName,
		"-hls_segment_filename", segmentPattern,
		layout.PlaylistPath(packageRoot),
	)

	for _, streamIndex := range subtitleStreamIndexes {
		subdir := filepath.Join(packageRoot, "subs", subtitleSlug(streamIndex))
		args = append(args,
			"-map", fmt.Sprintf("0:%d", streamIndex),
			"-c:s", "webvtt",
			"-f", "segment",
			"-segment_time", targetSeconds,
			"-segment_format", "webvtt",
			"-segment_list", filepath.Join(subdir, "playlist.m3u8"),
			"-segment_list_type", "m3u8",
			filepath.Join(subdir, "seg_%06d.vtt"),
		)
	}

	return args, nil
}

func subtitleSlug(streamIndex int) string { return "s" + strconv.Itoa(streamIndex) }

func shouldScaleVideo(video *probeStream, scaleHeight int64) bool {
	if scaleHeight <= 0 {
		return false
	}
	return video == nil || video.Height <= 0 || video.Height > scaleHeight
}

// burnFilter picks the composite chain for a resolved burn decision: text
// tracks render via the subtitles filter, anything else overlays as video
// (the pre-Source default, so decisions without a Source stay bitmap).
func burnFilter(input string, video *probeStream, burn subtitlepolicy.Decision, probe sourceProbe, profile packageprofile.Profile) string {
	if burn.Source == db.TrackSourceEmbedded {
		return textSubtitleBurnFilter(input, video, subtitleRelativeIndex(probe, burn.StreamIndex), profile)
	}
	return subtitleBurnFilter(video.Index, burn.StreamIndex, profile)
}

func subtitleBurnFilter(videoStreamIndex, subtitleStreamIndex int, profile packageprofile.Profile) string {
	scale := "scale=-2:ih"
	if profile.Video.ScaleHeight > 0 {
		scale = fmt.Sprintf("scale=-2:'min(ih,%d)'", profile.Video.ScaleHeight)
	}
	return fmt.Sprintf("[0:%d][0:%d]overlay=eof_action=pass,%s,format=yuv420p[v]", videoStreamIndex, subtitleStreamIndex, scale)
}

// textSubtitleBurnFilter renders a text subtitle stream onto the video via the
// subtitles filter (libass), reading the track straight from the source file
// so the encode carries no sidecar dependency. Unlike the bitmap overlay chain
// it tone-maps HDR sources to SDR first — text burn targets HDR Web-DLs, and
// compositing over untouched PQ/HLG frames ships crushed, desaturated video.
// Text renders after the downscale so glyphs rasterize at output resolution.
func textSubtitleBurnFilter(input string, video *probeStream, subtitleRelIndex int, profile packageprofile.Profile) string {
	var filters []string
	if sourceIsHDR(video) {
		filters = append(filters, tonemapSDRFilter())
	}
	if shouldScaleVideo(video, profile.Video.ScaleHeight) {
		filters = append(filters, fmt.Sprintf("scale=-2:'min(ih,%d)'", profile.Video.ScaleHeight))
	}
	filters = append(filters,
		fmt.Sprintf("subtitles=filename=%s:si=%d", subtitlesFilterPath(input), subtitleRelIndex),
		"format=yuv420p",
	)
	return fmt.Sprintf("[0:%d]%s[v]", video.Index, strings.Join(filters, ","))
}

// subtitleRelativeIndex converts an absolute stream index into the stream's
// position among the source's subtitle streams, which is what the subtitles
// filter's si option expects.
func subtitleRelativeIndex(probe sourceProbe, streamIndex int) int {
	rel := 0
	for _, s := range probe.Streams {
		if s.CodecType != "subtitle" {
			continue
		}
		if s.Index == streamIndex {
			return rel
		}
		rel++
	}
	return -1
}

// subtitlesFilterPath escapes a path for the subtitles filter's filename
// option. ffmpeg unescapes filter option values twice — the filtergraph
// parser handles \ ' [ ] , ; and then the option parser handles \ ' : — so
// the path is escaped for the option level first and the graph level second.
func subtitlesFilterPath(path string) string {
	return escapeFilterChars(escapeFilterChars(path, `\':`), `\'[],;`)
}

func escapeFilterChars(s, special string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
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

// ParseHLSManifest reads an HLS media playlist from disk and returns its
// segments with exact EXTINF durations. ffmpeg appends EXTINF+URI pairs as
// segments complete, so this is safe to call on a still-growing playlist of a
// live encode: it simply returns the segments written so far.
func ParseHLSManifest(path string) ([]HLSSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []HLSSegment
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
		out = append(out, HLSSegment{URI: line, DurationMs: pendingDuration})
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

// ExtractSubtitles converts text-based subtitle streams to package-owned WebVTT
// sidecars and records them in package_tracks. Only streams whose language is in
// prefs are extracted. Forced text tracks are always extracted. Bitmap formats
// are recorded as package inventory rows with no path. Each text stream has a
// 2-minute timeout so a hung ffmpeg can't block the worker.
func ExtractSubtitles(ctx context.Context, conn *sql.DB, mediaPath, packageRoot, packageID string) {
	probe, err := probeSource(ctx, mediaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "packager: probe for subtitle extraction package=%s: %v\n", packageID, err)
		return
	}
	prefs, _ := db.GetSubtitleLanguagePreference(ctx, conn)
	extractSubtitleTracks(ctx, conn, mediaPath, packageRoot, packageID, probe, prefs)
}

func extractSubtitleTracks(ctx context.Context, conn *sql.DB, mediaPath, packageRoot, packageID string, probe sourceProbe, prefs []string) {
	prefSet := make(map[string]bool, len(prefs))
	for _, p := range prefs {
		prefSet[strings.ToLower(p)] = true
	}

	subtitleDir := layout.PackageSubtitleDir(packageRoot)
	if err := os.MkdirAll(subtitleDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "packager: mkdir subtitle output dir %s: %v\n", subtitleDir, err)
		return
	}
	if err := db.DeletePackageTracks(ctx, conn, packageID); err != nil {
		fmt.Fprintf(os.Stderr, "packager: clear package tracks package=%s: %v\n", packageID, err)
		return
	}

	// Per-run log file so the operator can see what happened without digging
	// through container stderr. Truncated on each new extraction attempt.
	logFile, logErr := os.OpenFile(filepath.Join(subtitleDir, "extract.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(time.Now().UTC().Format("2006-01-02T15:04:05Z")+" "+format+"\n", args...)
		fmt.Fprint(os.Stderr, "packager: "+line)
		if logErr == nil {
			fmt.Fprint(logFile, line)
		}
	}
	if logErr == nil {
		defer logFile.Close()
	}
	logf("start package=%s path=%s", packageID, mediaPath)
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
			track := db.PackageTrack{
				PackageID:   packageID,
				Kind:        "subtitle",
				StreamIndex: s.Index,
				Language:    lang,
				Title:       s.Tags.Title,
				Codec:       strings.ToLower(s.CodecName),
				Source:      db.TrackSourceEmbeddedBitmap,
				DefaultFlag: s.Disposition.Default == 1,
				Forced:      s.Disposition.Forced == 1,
				Path:        nil,
			}
			if err := db.UpsertPackageTrack(ctx, conn, track); err != nil {
				logf("stream %d (%s) bitmap inventory error: %v", s.Index, lang, err)
			} else {
				logf("stream %d (%s) bitmap — inventoried (not extractable)", s.Index, lang)
			}
			subtitleRelIdx++
			continue
		}

		// Forced tracks carry foreign-dialogue translations that are meaningful
		// regardless of the user's language preference — always extract them.
		isForced := s.Disposition.Forced == 1
		if len(prefSet) > 0 && !prefSet[strings.ToLower(lang)] && !isForced {
			logf("stream %d (%s) skipped (not in language preference)", s.Index, lang)
			subtitleRelIdx++
			continue
		}

		vttPath := filepath.Join(subtitleDir, layout.SubtitleFileName(s.Index))

		// The canonical sidecar path is package-local. If the file is already
		// there, record that path even if an older DB row pointed elsewhere.
		if _, err := os.Stat(vttPath); err == nil {
			if err := upsertExtractedTextSubtitleTrack(ctx, conn, packageID, s, lang, vttPath); err != nil {
				logf("stream %d (%s) db error for existing subtitle: %v", s.Index, lang, err)
			} else {
				logf("stream %d (%s) skipped (already extracted: %s)", s.Index, lang, vttPath)
			}
			subtitleRelIdx++
			continue
		}

		logf("stream %d (%s) extracting -> %s", s.Index, lang, vttPath)
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		cmd, err := ffmpegexec.CommandContext(sctx, "ffmpeg",
			"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
			"-i", mediaPath,
			"-map", fmt.Sprintf("0:s:%d", subtitleRelIdx),
			"-c:s", "webvtt",
			vttPath,
		)
		if err != nil {
			logf("stream %d (%s) ffmpeg setup error: %v", s.Index, lang, err)
			cancel()
			subtitleRelIdx++
			continue
		}
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			logf("stream %d (%s) ffmpeg error: %v: %s", s.Index, lang, err, strings.TrimSpace(string(out)))
			subtitleRelIdx++
			continue
		}
		if err := upsertExtractedTextSubtitleTrack(ctx, conn, packageID, s, lang, vttPath); err != nil {
			logf("stream %d (%s) db error: %v", s.Index, lang, err)
		} else {
			logf("stream %d (%s) done -> %s", s.Index, lang, vttPath)
		}
		subtitleRelIdx++
	}
	logf("done")
}

func upsertExtractedTextSubtitleTrack(ctx context.Context, conn *sql.DB, packageID string, s probeStream, lang, vttPath string) error {
	return db.UpsertPackageTrack(ctx, conn, db.PackageTrack{
		PackageID:       packageID,
		Kind:            "subtitle",
		StreamIndex:     s.Index,
		Language:        lang,
		Title:           s.Tags.Title,
		Codec:           "webvtt",
		Source:          db.TrackSourceEmbedded,
		DefaultFlag:     s.Disposition.Default == 1,
		Forced:          s.Disposition.Forced == 1,
		HearingImpaired: s.Disposition.HearingImpaired == 1,
		Path:            &vttPath,
	})
}

func isBitmapSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "dvd_subtitle", "dvb_subtitle", "hdmv_pgs_subtitle", "pgssub", "xsub":
		return true
	}
	return false
}

func packageSubtitleBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
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
