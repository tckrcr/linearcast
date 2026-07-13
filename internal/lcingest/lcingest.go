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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/codec"
	"github.com/tckrcr/linearcast/internal/db"
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

type EpisodeOrderingBackfillResult struct {
	Scanned int
	Updated int
}

type Result struct {
	Total          int
	Passed         int
	Failed         int
	FailureReasons map[string]int
}

type Logger interface {
	Printf(format string, args ...any)
}

func BackfillEpisodeOrdering(ctx context.Context, conn *sql.DB, log Logger) (EpisodeOrderingBackfillResult, error) {
	if log == nil {
		log = nopLogger{}
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, path, season_number, episode_number FROM media`)
	if err != nil {
		return EpisodeOrderingBackfillResult{}, fmt.Errorf("select media: %w", err)
	}
	defer rows.Close()

	type pending struct {
		id            string
		seasonNumber  int64
		episodeNumber int64
	}
	var todo []pending
	var res EpisodeOrderingBackfillResult
	for rows.Next() {
		var id, path string
		var seasonNumber, episodeNumber sql.NullInt64
		if err := rows.Scan(&id, &path, &seasonNumber, &episodeNumber); err != nil {
			return res, fmt.Errorf("scan media: %w", err)
		}
		res.Scanned++
		if seasonNumber.Valid && episodeNumber.Valid {
			continue
		}
		season, episode, ok := ParseEpisodeCode(path)
		if !ok {
			continue
		}
		nextSeason := int64(season)
		nextEpisode := int64(episode)
		if seasonNumber.Valid {
			nextSeason = seasonNumber.Int64
		}
		if episodeNumber.Valid {
			nextEpisode = episodeNumber.Int64
		}
		todo = append(todo, pending{id: id, seasonNumber: nextSeason, episodeNumber: nextEpisode})
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	for _, item := range todo {
		if _, err := conn.ExecContext(ctx, `UPDATE media
			SET season_number = COALESCE(season_number, ?),
			    episode_number = COALESCE(episode_number, ?)
			WHERE id = ?`, item.seasonNumber, item.episodeNumber, item.id); err != nil {
			return res, fmt.Errorf("update %s: %w", item.id, err)
		}
		res.Updated++
		log.Printf("episode ordering backfill %s -> S%02dE%02d", item.id, item.seasonNumber, item.episodeNumber)
	}
	return res, nil
}

// ProgressReporter is an optional extension to Logger. If a Logger also
// implements OnProgress, Ingest/IngestMusic call it after each file so callers
// can report scan progress (e.g. 47/211 files processed).
type ProgressReporter interface {
	OnProgress()
}

// ScanConcurrency is the worker count for per-file probes during scans.
// Probing is I/O-bound (header reads plus a few seeks, often over network
// mounts), so wall time scales with how much per-file latency the pool can
// overlap; DB writes still serialize on the process's single write
// connection. Capped at 16: beyond that the NFS/SMB server's request queue,
// not the client, is the ceiling.
func ScanConcurrency() int {
	n := runtime.NumCPU()
	switch {
	case n < 2:
		return 2
	case n > 16:
		return 16
	default:
		return n
	}
}

// ScanPool runs work over tasks with ScanConcurrency workers and merges the
// per-worker Results. work does its own Result accounting on the *Result it
// is handed. Cancelling ctx stops dispatch and skips undispatched tasks, so
// the merged Result covers attempted files only. If logger implements
// ProgressReporter, OnProgress fires after each attempted task.
func ScanPool[T any](ctx context.Context, tasks []T, logger Logger, work func(task T, res *Result)) Result {
	pr, _ := logger.(ProgressReporter)
	workers := ScanConcurrency()
	if len(tasks) < workers {
		workers = len(tasks)
	}
	taskCh := make(chan T)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		merged Result
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local Result
			for t := range taskCh {
				if ctx.Err() != nil {
					continue
				}
				work(t, &local)
				if pr != nil {
					pr.OnProgress()
				}
			}
			mu.Lock()
			merged.Add(local)
			mu.Unlock()
		}()
	}
dispatch:
	for _, t := range tasks {
		select {
		case taskCh <- t:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(taskCh)
	wg.Wait()
	return merged
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// Ingest walks mediaDir, probes each .mkv/.mp4 with ffprobe, and upserts a
// row per file. Files are probed concurrently via ScanPool. Codec policy is
// recorded in codec_check_passed/reason but does not block writes. logger may
// be nil.
func Ingest(ctx context.Context, conn *sql.DB, mediaDir string, logger Logger) (Result, error) {
	if logger == nil {
		logger = nopLogger{}
	}
	var paths []string
	err := walkMedia(mediaDir, func(p string) error {
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk: %w", err)
	}
	if len(paths) == 0 {
		return Result{}, fmt.Errorf("no media files under %s", mediaDir)
	}
	res := ScanPool(ctx, paths, logger, func(p string, r *Result) {
		r.Total++
		ingestOne(ctx, conn, p, 0, 0, logger, r)
	})
	return res, ctx.Err()
}

// IngestFile probes and upserts a single media file. Codec policy is
// recorded but does not block writes. logger may be nil. Returns a
// single-file Result for symmetry with Ingest.
func IngestFile(ctx context.Context, conn *sql.DB, path string, logger Logger) (Result, error) {
	return IngestFileWithHints(ctx, conn, path, 0, 0, logger)
}

func IngestFileWithHints(ctx context.Context, conn *sql.DB, path string, seasonHint, episodeHint int, logger Logger) (Result, error) {
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
	ingestOne(ctx, conn, abs, seasonHint, episodeHint, logger, &res)
	return res, nil
}

func ingestOne(ctx context.Context, conn *sql.DB, p string, seasonHint, episodeHint int, logger Logger, res *Result) {
	probe, dur, err := ffprobeFile(ctx, p)
	if err != nil {
		logger.Printf("probe failed path=%q err=%v", p, err)
		res.recordFailure(fmt.Sprintf("probe error: %v", err))
		return
	}
	dec := codec.Admit(probe)
	seasonNumber, episodeNumber := resolveEpisodeOrdering(p, seasonHint, episodeHint)
	row := mediaRowFor(p, probe, dur, dec.OK, dec.Reason, seasonNumber, episodeNumber)
	if err := upsertMedia(conn, row); err != nil {
		logger.Printf("upsert failed path=%q err=%v", p, err)
		res.recordFailure(fmt.Sprintf("db error: %v", err))
		return
	}
	if g := DeriveSchedulingGroup(p); g != "" {
		if err := setCollectionIfNull(conn, row.ID, g, "show"); err != nil {
			logger.Printf("set collection path=%q err=%v", p, err)
		}
	}
	if dec.OK {
		res.Passed++
		logger.Printf("ingested id=%s ok=true container=%s video=%s height=%d audio=%s duration_s=%.3f",
			row.ID, row.Container, row.VideoCodec, row.VideoHeight, row.AudioCodec, float64(row.DurationMs)/1000)
	} else {
		res.recordFailure(dec.Reason)
		logger.Printf("ingested id=%s ok=false reason=%q", row.ID, dec.Reason)
	}
}

func (r *Result) recordFailure(reason string) {
	r.Failed++
	if r.FailureReasons == nil {
		r.FailureReasons = make(map[string]int)
	}
	r.FailureReasons[reason]++
}

func (r *Result) Add(other Result) {
	r.Total += other.Total
	r.Passed += other.Passed
	r.Failed += other.Failed
	for reason, count := range other.FailureReasons {
		if r.FailureReasons == nil {
			r.FailureReasons = make(map[string]int)
		}
		r.FailureReasons[reason] += count
	}
}

func walkMedia(dir string, fn func(string) error) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isMediaFile(p) {
			return fn(p)
		}
		return nil
	})
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
		if isMediaFile(p) {
			count++
		}
		return nil
	})
	return count, err
}

func isMediaFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".webm":
		return true
	default:
		return false
	}
}

type ffprobeOutput struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		CodecType      string `json:"codec_type"`
		CodecName      string `json:"codec_name"`
		CodecTagString string `json:"codec_tag_string"`
		Profile        string `json:"profile"`
		Width          int64  `json:"width"`
		Height         int64  `json:"height"`
		BitRate        string `json:"bit_rate"`
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
	probe.CodecTagString = strings.ToLower(strings.TrimSpace(v.CodecTagString))
	probe.AudioCodec = canonicalAudio(a.CodecName, a.Profile)

	// Source video bitrate (bps). Prefer the selected video stream's bit_rate;
	// fall back to the whole-container bit_rate when the source doesn't report a
	// per-stream value (common in Matroska). 0 = unknown.
	probe.VideoBitrateBps = parseProbeBps(v.BitRate)
	if probe.VideoBitrateBps == 0 {
		probe.VideoBitrateBps = parseProbeBps(raw.Format.BitRate)
	}

	return probe, int64(durSecs * 1000), nil
}

// parseProbeBps parses an ffprobe bit_rate string (bits per second, e.g.
// "13582000"). Absent or non-numeric values ("N/A", "") parse as 0 = unknown.
func parseProbeBps(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
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
	SeasonNumber     *int64
	EpisodeNumber    *int64
	DurationMs       int64
	Container        string
	VideoCodec       string
	VideoWidth       int64
	VideoHeight      int64
	VideoBitrateBps  int64
	ColorTransfer    string
	ColorPrimaries   string
	CodecTagString   string
	AudioCodec       string
	CodecCheckPassed bool
	CodecCheckReason string
	IngestedAtMs     int64
	MediaKind        string // "" = video (NULL in DB); "music" = audio-only
}

func mediaRowFor(path string, p codec.Probe, durMs int64, passed bool, reason string, seasonNumber, episodeNumber *int64) mediaRow {
	return mediaRow{
		ID:               mediaIDFor(path),
		Path:             path,
		Directory:        filepath.Dir(path),
		Title:            DeriveTitle(path),
		SeasonNumber:     seasonNumber,
		EpisodeNumber:    episodeNumber,
		DurationMs:       durMs,
		Container:        p.Container,
		VideoCodec:       p.VideoCodec,
		VideoWidth:       p.VideoWidth,
		VideoHeight:      p.VideoHeight,
		VideoBitrateBps:  p.VideoBitrateBps,
		ColorTransfer:    p.ColorTransfer,
		ColorPrimaries:   p.ColorPrimaries,
		CodecTagString:   p.CodecTagString,
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

func resolveEpisodeOrdering(path string, seasonHint, episodeHint int) (*int64, *int64) {
	if seasonHint > 0 && episodeHint > 0 {
		season := int64(seasonHint)
		episode := int64(episodeHint)
		return &season, &episode
	}
	season, episode, ok := ParseEpisodeCode(path)
	if !ok {
		return nil, nil
	}
	season64 := int64(season)
	episode64 := int64(episode)
	return &season64, &episode64
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
	var seasonNumber any
	if m.SeasonNumber != nil {
		seasonNumber = *m.SeasonNumber
	}
	var episodeNumber any
	if m.EpisodeNumber != nil {
		episodeNumber = *m.EpisodeNumber
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
	var tag any
	if m.CodecTagString != "" {
		tag = m.CodecTagString
	}
	_, err := conn.Exec(`
        INSERT INTO media (id, path, directory, title, scheduling_group, season_number, episode_number, duration_ms, container,
                           video_codec, video_width, video_height, video_bitrate_bps, color_transfer, color_primaries,
                           codec_tag_string, audio_codec, codec_check_passed, codec_check_reason,
                           ingested_at_ms, media_kind)
        VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(path) DO UPDATE SET
            directory = excluded.directory,
            title = excluded.title,
            season_number = excluded.season_number,
            episode_number = excluded.episode_number,
            duration_ms = excluded.duration_ms,
            container = excluded.container,
            video_codec = excluded.video_codec,
            video_width = excluded.video_width,
            video_height = excluded.video_height,
            video_bitrate_bps = excluded.video_bitrate_bps,
            color_transfer = excluded.color_transfer,
            color_primaries = excluded.color_primaries,
            codec_tag_string = excluded.codec_tag_string,
            audio_codec = excluded.audio_codec,
            codec_check_passed = excluded.codec_check_passed,
            codec_check_reason = excluded.codec_check_reason,
            ingested_at_ms = excluded.ingested_at_ms,
            media_kind = excluded.media_kind
        ON CONFLICT(id) DO UPDATE SET
            path = excluded.path,
            directory = excluded.directory,
            title = excluded.title,
            season_number = excluded.season_number,
            episode_number = excluded.episode_number,
            duration_ms = excluded.duration_ms,
            container = excluded.container,
            video_codec = excluded.video_codec,
            video_width = excluded.video_width,
            video_height = excluded.video_height,
            video_bitrate_bps = excluded.video_bitrate_bps,
            color_transfer = excluded.color_transfer,
            color_primaries = excluded.color_primaries,
            codec_tag_string = excluded.codec_tag_string,
            audio_codec = excluded.audio_codec,
            codec_check_passed = excluded.codec_check_passed,
            codec_check_reason = excluded.codec_check_reason,
            ingested_at_ms = excluded.ingested_at_ms,
            media_kind = excluded.media_kind`,
		m.ID, m.Path, m.Directory, title, seasonNumber, episodeNumber, m.DurationMs, m.Container,
		m.VideoCodec, width, m.VideoHeight, m.VideoBitrateBps, transfer, primaries,
		tag, m.AudioCodec, passed, reason, m.IngestedAtMs, kind)
	return err
}

// setCollectionIfNull writes collection_id only when it is currently NULL.
// Manual overrides survive re-ingest.
func setCollectionIfNull(conn *sql.DB, mediaID, name, kind string) error {
	id, err := db.UpsertCollection(context.Background(), conn, name, kind, "filename")
	if err != nil {
		return err
	}
	_, err = conn.Exec(
		`UPDATE media SET collection_id = ? WHERE id = ? AND collection_id IS NULL`,
		id, mediaID,
	)
	return err
}
