// Package lcingest probes media files under a directory and inserts/updates
// rows in the linearcast SQLite database.
//
// Used by cmd/linearcast-ingest and admin API ingest flows.
package lcingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/codec"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
)

// RetitleAll re-derives titles for every existing media row from its stored
// path and updates rows whose title has changed. Returns (scanned, updated)
// row counts. Safe to run repeatedly; a no-op when titles already match.
func RetitleAll(ctx context.Context, conn *sql.DB, log Logger) (scanned, updated int, err error) {
	if log == nil {
		log = nopLogger{}
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, path, title FROM media`)
	if err != nil {
		return 0, 0, fmt.Errorf("select media: %w", err)
	}
	defer rows.Close()

	type pending struct {
		id    string
		title string
	}
	var todo []pending
	for rows.Next() {
		var id, path string
		var title sql.NullString
		if err := rows.Scan(&id, &path, &title); err != nil {
			return scanned, updated, fmt.Errorf("scan media: %w", err)
		}
		scanned++
		want := DeriveTitle(path)
		if want == "" || want == title.String {
			continue
		}
		todo = append(todo, pending{id: id, title: want})
	}
	if err := rows.Err(); err != nil {
		return scanned, updated, err
	}

	for _, p := range todo {
		if _, err := conn.ExecContext(ctx, `UPDATE media SET title = ? WHERE id = ?`, p.title, p.id); err != nil {
			return scanned, updated, fmt.Errorf("update %s: %w", p.id, err)
		}
		updated++
		log.Printf("retitled %s -> %q", p.id, p.title)
	}
	return scanned, updated, nil
}

type Result struct {
	Total    int
	Passed   int
	Failed   int
	Failures []string
}

type Logger interface {
	Printf(format string, args ...any)
}

// ProgressReporter is an optional extension to Logger. If a Logger also
// implements OnProgress, Ingest/IngestMusic call it after each file so callers
// can report scan progress (e.g. 47/211 files processed).
type ProgressReporter interface {
	OnProgress()
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// Ingest walks mediaDir, probes each .mkv/.mp4 with ffprobe, and upserts a
// row per file. Codec policy is recorded in codec_check_passed/reason but does
// not block writes. logger may be nil.
func Ingest(ctx context.Context, conn *sql.DB, mediaDir string, logger Logger) (Result, error) {
	if logger == nil {
		logger = nopLogger{}
	}
	files, err := walkMedia(mediaDir)
	if err != nil {
		return Result{}, fmt.Errorf("walk: %w", err)
	}
	if len(files) == 0 {
		return Result{}, fmt.Errorf("no media files under %s", mediaDir)
	}

	var res Result
	res.Total = len(files)
	pr, _ := logger.(ProgressReporter)
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		ingestOne(ctx, conn, p, logger, &res)
		if pr != nil {
			pr.OnProgress()
		}
	}
	return res, nil
}

// IngestFile probes and upserts a single media file. Codec policy is
// recorded but does not block writes. logger may be nil. Returns a
// single-file Result for symmetry with Ingest.
func IngestFile(ctx context.Context, conn *sql.DB, path string, logger Logger) (Result, error) {
	if logger == nil {
		logger = nopLogger{}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Result{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return Result{}, err
	}
	if st.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory; use Ingest", abs)
	}
	res := Result{Total: 1}
	ingestOne(ctx, conn, abs, logger, &res)
	return res, nil
}

func ingestOne(ctx context.Context, conn *sql.DB, p string, logger Logger, res *Result) {
	probe, dur, err := ffprobeFile(ctx, p)
	if err != nil {
		logger.Printf("probe failed path=%q err=%v", p, err)
		res.Failed++
		res.Failures = append(res.Failures, fmt.Sprintf("%s: probe error: %v", filepath.Base(p), err))
		return
	}
	reason, ok := codec.Check(probe)
	row := mediaRowFor(p, probe, dur, ok, reason)
	if err := upsertMedia(conn, row); err != nil {
		logger.Printf("upsert failed path=%q err=%v", p, err)
		res.Failed++
		res.Failures = append(res.Failures, fmt.Sprintf("%s: db error: %v", filepath.Base(p), err))
		return
	}
	if g := DeriveSchedulingGroup(p); g != "" {
		if err := setSchedulingGroupIfNull(conn, row.ID, g); err != nil {
			logger.Printf("set scheduling_group path=%q err=%v", p, err)
		}
	}
	if ok {
		res.Passed++
		logger.Printf("ingested id=%s ok=true container=%s video=%s height=%d audio=%s duration_s=%.3f",
			row.ID, row.Container, row.VideoCodec, row.VideoHeight, row.AudioCodec, float64(row.DurationMs)/1000)
	} else {
		res.Failed++
		res.Failures = append(res.Failures, fmt.Sprintf("%s: %s", filepath.Base(p), reason))
		logger.Printf("ingested id=%s ok=false reason=%q", row.ID, reason)
	}
}

func walkMedia(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".mkv" || ext == ".mp4" || ext == ".webm" {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func CountMediaFiles(dir string) (int, error) {
	var count int
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".mkv" || ext == ".mp4" || ext == ".webm" {
			count++
		}
		return nil
	})
	return count, err
}

type ffprobeOutput struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
	Streams []struct {
		CodecType      string `json:"codec_type"`
		CodecName      string `json:"codec_name"`
		Profile        string `json:"profile"`
		Width          int64  `json:"width"`
		Height         int64  `json:"height"`
		ColorTransfer  string `json:"color_transfer"`
		ColorPrimaries string `json:"color_primaries"`
		Disposition    struct {
			Default     int `json:"default"`
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}

func ffprobeFile(ctx context.Context, path string) (codec.Probe, int64, error) {
	cmd, err := ffmpegexec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	if err != nil {
		return codec.Probe{}, 0, fmt.Errorf("ffprobe: %w", err)
	}
	out, err := cmd.Output()
	if err != nil {
		return codec.Probe{}, 0, fmt.Errorf("ffprobe: %w", err)
	}
	var raw ffprobeOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return codec.Probe{}, 0, fmt.Errorf("parse ffprobe: %w", err)
	}
	durSecs, err := strconv.ParseFloat(strings.TrimSpace(raw.Format.Duration), 64)
	if err != nil || durSecs <= 0 {
		return codec.Probe{}, 0, fmt.Errorf("invalid duration %q", raw.Format.Duration)
	}

	probe := codec.Probe{Container: containerFromFormat(raw.Format.FormatName, path)}

	bestVideoIdx, bestAudioIdx := -1, -1
	for i, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			// Skip embedded album art — it appears as a video stream in FLAC/MP3
			// files but is not a real video track.
			if s.Disposition.AttachedPic == 1 {
				continue
			}
			if bestVideoIdx == -1 || s.Disposition.Default == 1 {
				bestVideoIdx = i
			}
		case "audio":
			if bestAudioIdx == -1 || s.Disposition.Default == 1 {
				bestAudioIdx = i
			}
		}
	}
	if bestVideoIdx == -1 {
		return codec.Probe{}, 0, errors.New("no video stream")
	}
	if bestAudioIdx == -1 {
		return codec.Probe{}, 0, errors.New("no audio stream")
	}
	v := raw.Streams[bestVideoIdx]
	a := raw.Streams[bestAudioIdx]
	probe.VideoCodec = strings.ToLower(v.CodecName)
	probe.VideoWidth = v.Width
	probe.VideoHeight = v.Height
	probe.ColorTransfer = strings.ToLower(v.ColorTransfer)
	probe.ColorPrimaries = strings.ToLower(v.ColorPrimaries)
	probe.AudioCodec = canonicalAudio(a.CodecName, a.Profile)

	return probe, int64(durSecs * 1000), nil
}

func canonicalAudio(codecName, profile string) string {
	name := strings.ToLower(strings.TrimSpace(codecName))
	prof := strings.ToLower(strings.TrimSpace(profile))
	if name == "dts" {
		switch {
		case strings.Contains(prof, "dts-hd ma"), strings.Contains(prof, "dts-hd master"):
			return "dts-hd-ma"
		case strings.Contains(prof, "dts-hd hra"):
			return "dts-hd-hra"
		}
	}
	return name
}

func containerFromFormat(formatName, path string) string {
	for _, p := range strings.Split(formatName, ",") {
		switch strings.TrimSpace(p) {
		case "matroska", "webm":
			return "mkv"
		case "mp4", "mov", "m4a":
			return "mp4"
		}
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "mkv", "webm":
		return "mkv"
	case "mp4", "m4v", "mov":
		return "mp4"
	}
	return strings.ToLower(formatName)
}

type mediaRow struct {
	ID               string
	Path             string
	Directory        string
	Title            string
	DurationMs       int64
	Container        string
	VideoCodec       string
	VideoWidth       int64
	VideoHeight      int64
	ColorTransfer    string
	ColorPrimaries   string
	AudioCodec       string
	CodecCheckPassed bool
	CodecCheckReason string
	IngestedAtMs     int64
	MediaKind        string // "" = video (NULL in DB); "music" = audio-only
}

func mediaRowFor(path string, p codec.Probe, durMs int64, passed bool, reason string) mediaRow {
	return mediaRow{
		ID:               mediaIDFor(path),
		Path:             path,
		Directory:        filepath.Dir(path),
		Title:            DeriveTitle(path),
		DurationMs:       durMs,
		Container:        p.Container,
		VideoCodec:       p.VideoCodec,
		VideoWidth:       p.VideoWidth,
		VideoHeight:      p.VideoHeight,
		ColorTransfer:    p.ColorTransfer,
		ColorPrimaries:   p.ColorPrimaries,
		AudioCodec:       p.AudioCodec,
		CodecCheckPassed: passed,
		CodecCheckReason: reason,
		IngestedAtMs:     time.Now().UTC().UnixMilli(),
	}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func mediaIDFor(path string) string {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	id := slugRe.ReplaceAllString(strings.ToLower(stem), "-")
	id = strings.Trim(id, "-")
	if id == "" {
		id = fmt.Sprintf("media-%d", time.Now().UnixNano())
	}
	if len(id) > 96 {
		id = id[:96]
	}
	return id
}

func upsertMedia(conn *sql.DB, m mediaRow) error {
	var passed int64
	if m.CodecCheckPassed {
		passed = 1
	}
	var reason any
	if m.CodecCheckReason != "" {
		reason = m.CodecCheckReason
	}
	var title any
	if m.Title != "" {
		title = m.Title
	}
	var kind any
	if m.MediaKind != "" {
		kind = m.MediaKind
	}
	var width any
	if m.VideoWidth > 0 {
		width = m.VideoWidth
	}
	var transfer any
	if m.ColorTransfer != "" {
		transfer = m.ColorTransfer
	}
	var primaries any
	if m.ColorPrimaries != "" {
		primaries = m.ColorPrimaries
	}
	_, err := conn.Exec(`
        INSERT INTO media (id, path, directory, title, scheduling_group, duration_ms, container,
                           video_codec, video_width, video_height, color_transfer, color_primaries,
                           audio_codec, codec_check_passed, codec_check_reason,
                           ingested_at_ms, media_kind)
        VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(path) DO UPDATE SET
            directory = excluded.directory,
            title = excluded.title,
            duration_ms = excluded.duration_ms,
            container = excluded.container,
            video_codec = excluded.video_codec,
            video_width = excluded.video_width,
            video_height = excluded.video_height,
            color_transfer = excluded.color_transfer,
            color_primaries = excluded.color_primaries,
            audio_codec = excluded.audio_codec,
            codec_check_passed = excluded.codec_check_passed,
            codec_check_reason = excluded.codec_check_reason,
            ingested_at_ms = excluded.ingested_at_ms,
            media_kind = excluded.media_kind`,
		m.ID, m.Path, m.Directory, title, m.DurationMs, m.Container,
		m.VideoCodec, width, m.VideoHeight, transfer, primaries,
		m.AudioCodec, passed, reason, m.IngestedAtMs, kind)
	return err
}

// setSchedulingGroupIfNull writes scheduling_group only when it's currently
// NULL. Manual overrides (set via the `set-group` CLI or direct SQL) survive
// re-ingest.
func setSchedulingGroupIfNull(conn *sql.DB, mediaID, group string) error {
	_, err := conn.Exec(
		`UPDATE media SET scheduling_group = ? WHERE id = ? AND scheduling_group IS NULL`,
		group, mediaID,
	)
	return err
}
