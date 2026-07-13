package lcingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/ffmpegexec"
)

// fullAlbumMinDurationMs: files longer than this with no per-track metadata
// are assumed to be full-album rips (whole CD as one file + .cue sheet).
const fullAlbumMinDurationMs = 35 * 60 * 1000 // 35 minutes

var musicExts = map[string]bool{
	".flac": true, ".mp3": true, ".wav": true,
	".m4a": true, ".aiff": true, ".aif": true,
	".dsf": true, ".dff": true, ".ape": true,
	".ogg": true, ".opus": true,
}

var (
	// yearPrefixRe strips a leading "YYYY " or "YYYY - " from a directory name.
	yearPrefixRe = regexp.MustCompile(`^\d{4}\s*[-–]?\s*`)
	// yearParenRe strips a trailing " (YYYY)" from a string.
	yearParenRe = regexp.MustCompile(`\s*\(\d{4}\)\s*$`)
	// yearBracketRe strips bracket year tags like " [2009]".
	yearBracketRe = regexp.MustCompile(`\s*\[\d{4}\]\s*`)
	// pureYearRe matches a bare 4-digit year token.
	pureYearRe = regexp.MustCompile(`^\d{4}$`)
	// leadingTrackRe strips a leading track number (e.g. "01 - " or "1. ").
	leadingTrackRe = regexp.MustCompile(`^\d{1,3}[\s.\-_]+`)
)

// IngestMusic walks musicDir, probes each audio file with ffprobe, and upserts
// a row per file into the media table. Titles and scheduling groups are derived
// from embedded tags with a path-structure fallback.
//
// All music rows are written with codec_check_passed=true and
// media_kind="music". The media_kind column — not the codec gate — is what
// routes them: the channel-add and filler-attach gates require the media kind
// to match the channel's kind, and the packager only packages music under a
// music profile.
func IngestMusic(ctx context.Context, conn *sql.DB, musicDir string, logger Logger) (Result, error) {
	if logger == nil {
		logger = nopLogger{}
	}
	var paths []string
	err := walkMusicFiles(musicDir, func(p string) error {
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk: %w", err)
	}
	if len(paths) == 0 {
		return Result{}, fmt.Errorf("no music files under %s", musicDir)
	}
	res := ScanPool(ctx, paths, logger, func(p string, r *Result) {
		r.Total++
		ingestMusicOne(ctx, conn, p, logger, r)
	})
	return res, ctx.Err()
}

func walkMusicFiles(dir string, fn func(string) error) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if musicExts[strings.ToLower(filepath.Ext(p))] {
			return fn(p)
		}
		return nil
	})
}

func CountMusicFiles(dir string) (int, error) {
	var count int
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if musicExts[strings.ToLower(filepath.Ext(p))] {
			count++
		}
		return nil
	})
	return count, err
}

type musicTags struct {
	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	Track       string
	Date        string
}

// MusicProbeResult is defined in probe.go

type musicFFProbeOutput struct {
	Format struct {
		Duration   string            `json:"duration"`
		FormatName string            `json:"format_name"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
	} `json:"streams"`
}

func ffprobeMusicFile(ctx context.Context, path string) (MusicProbeResult, error) {
	cmd, err := ffmpegexec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	if err != nil {
		return MusicProbeResult{}, fmt.Errorf("ffprobe: %w", err)
	}
	out, err := cmd.Output()
	if err != nil {
		return MusicProbeResult{}, fmt.Errorf("ffprobe: %w", err)
	}
	var raw musicFFProbeOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return MusicProbeResult{}, fmt.Errorf("parse ffprobe: %w", err)
	}
	durSecs, err := strconv.ParseFloat(strings.TrimSpace(raw.Format.Duration), 64)
	if err != nil || durSecs <= 0 {
		return MusicProbeResult{}, fmt.Errorf("invalid duration %q", raw.Format.Duration)
	}

	var audioCodec string
	for _, s := range raw.Streams {
		if s.CodecType == "audio" {
			audioCodec = strings.ToLower(s.CodecName)
			break
		}
	}
	if audioCodec == "" {
		return MusicProbeResult{}, fmt.Errorf("no audio stream")
	}

	stem := strings.TrimSuffix(path, filepath.Ext(path))
	_, cueErr := os.Stat(stem + ".cue")

	return MusicProbeResult{
		FormatName:    containerFromFormat(raw.Format.FormatName, path),
		DurationMs:    int64(durSecs * 1000),
		AudioCodec:    audioCodec,
		Tags:          parseMusicTags(raw.Format.Tags),
		HasCueSidecar: cueErr == nil,
	}, nil
}

// parseMusicTags reads the common tag keys used by both Vorbis/FLAC
// (uppercase: ARTIST, ALBUM, TITLE, TRACKNUMBER, DATE) and ID3 (lowercase).
func parseMusicTags(raw map[string]string) musicTags {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(raw[k]); v != "" {
				return v
			}
			if v := strings.TrimSpace(raw[strings.ToUpper(k)]); v != "" {
				return v
			}
			if v := strings.TrimSpace(raw[strings.ToLower(k)]); v != "" {
				return v
			}
		}
		return ""
	}
	return musicTags{
		Title:       get("title"),
		Artist:      get("artist"),
		AlbumArtist: get("album_artist", "albumartist", "TPE2"),
		Album:       get("album"),
		Track:       get("track", "tracknumber"),
		Date:        get("date", "year"),
	}
}

func isSingleFileAlbum(mp MusicProbeResult) bool {
	return mp.HasCueSidecar || mp.DurationMs >= fullAlbumMinDurationMs
}

// DeriveMusicTitle returns a display title for an audio file.
//
// For full-album single-file rips: "[Full Album] <Album Name>"
// For normal tracks: "<NN>. <Track Title>" from tags, or cleaned filename stem.
func DeriveMusicTitle(path string, mp MusicProbeResult) string {
	if isSingleFileAlbum(mp) {
		album := mp.Tags.Album
		if album == "" {
			album = albumNameFromPath(path)
		}
		if album != "" {
			return "[Full Album] " + album
		}
		return "[Full Album] " + cleanedStem(path)
	}

	title := mp.Tags.Title
	if title == "" {
		// Derive from filename: strip leading track number, normalise separators.
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		stem = titleSepRe.ReplaceAllString(stem, " ")
		stem = multiSpaceRe.ReplaceAllString(stem, " ")
		title = strings.TrimSpace(leadingTrackRe.ReplaceAllString(stem, ""))
		if title == "" {
			title = stem
		}
	}

	if n := trackNum(mp.Tags.Track); n > 0 {
		return fmt.Sprintf("%02d. %s", n, title)
	}
	return title
}

// DeriveMusicSchedulingGroup returns a scheduling group for an audio file.
// Format: "<Artist> — <Album>" (with " [Full Album]" suffix for whole-album files).
//
// Tags take priority; falls back to the album directory name parsed from the path.
// The [2009 Stereo Remaster] / [2009 Mono Remaster] suffixes in directory names
// are preserved, so stereo and mono remasters of the same album land in separate
// scheduling groups.
func DeriveMusicSchedulingGroup(path string, mp MusicProbeResult) string {
	artist := firstNonEmpty(mp.Tags.AlbumArtist, mp.Tags.Artist)
	album := mp.Tags.Album
	full := isSingleFileAlbum(mp)

	if artist != "" && album != "" {
		if full {
			return fmt.Sprintf("%s — %s [Full Album]", artist, album)
		}
		return fmt.Sprintf("%s — %s", artist, album)
	}

	return musicGroupFromPath(path, full)
}

// musicGroupFromPath derives a scheduling group from the directory structure
// when tags are absent or incomplete.
func musicGroupFromPath(path string, fullAlbum bool) string {
	dir := filepath.Base(filepath.Dir(path))
	artist, album := splitArtistAlbum(dir, path)
	suffix := ""
	if fullAlbum {
		suffix = " [Full Album]"
	}
	if artist != "" && album != "" {
		return fmt.Sprintf("%s — %s%s", artist, album, suffix)
	}
	if album != "" {
		return album + suffix
	}
	return dir + suffix
}

// splitArtistAlbum attempts to extract artist and album from a directory name,
// falling back to the grandparent directory as the artist when the directory
// starts with a year prefix.
func splitArtistAlbum(dir, filePath string) (artist, album string) {
	// Case: "Artist - Album (Year)" or "Artist - Album [Tag]"
	if idx := strings.Index(dir, " - "); idx > 0 {
		left := strings.TrimSpace(dir[:idx])
		right := strings.TrimSpace(dir[idx+3:])

		if pureYearRe.MatchString(left) {
			// "1977 - Example Artist - Example Album" — the left is just a year.
			// Try splitting the right portion again.
			if idx2 := strings.Index(right, " - "); idx2 > 0 {
				left = strings.TrimSpace(right[:idx2])
				right = strings.TrimSpace(right[idx2+3:])
			} else {
				// "2016 - Example Album" — no second split available.
				left = ""
			}
		}

		album = cleanAlbumName(right)
		if left != "" {
			return left, album
		}
		// No artist from dir — try grandparent.
		gp := filepath.Base(filepath.Dir(filepath.Dir(filePath)))
		if isUsableArtistDir(gp) {
			return gp, album
		}
		return "", album
	}

	// Case: "2008 Example Album" — leading year, album follows.
	stripped := strings.TrimSpace(yearPrefixRe.ReplaceAllString(dir, ""))
	if stripped != dir && stripped != "" {
		album = cleanAlbumName(stripped)
		gp := filepath.Base(filepath.Dir(filepath.Dir(filePath)))
		if isUsableArtistDir(gp) {
			return gp, album
		}
		return "", album
	}

	// Plain directory name — use it as album, grandparent as artist.
	album = cleanAlbumName(dir)
	gp := filepath.Base(filepath.Dir(filepath.Dir(filePath)))
	if isUsableArtistDir(gp) {
		return gp, album
	}
	return "", album
}

// isUsableArtistDir returns false for the music library root and similarly
// generic directory names that don't represent a real artist.
func isUsableArtistDir(name string) bool {
	switch strings.ToLower(name) {
	case "", "music", "plex", "media", "/", ".":
		return false
	}
	return true
}

// cleanAlbumName strips bare year annotations from an album directory name
// while preserving informative tags like "[2009 Stereo Remaster]".
// Stripped forms: trailing "(YYYY)" and bare "[YYYY]" brackets.
func cleanAlbumName(s string) string {
	s = yearParenRe.ReplaceAllString(s, "")
	s = yearBracketRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	return s
}

// albumNameFromPath returns a human-readable album name derived from the
// containing directory, suitable for the "[Full Album] ..." title prefix.
func albumNameFromPath(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	_, album := splitArtistAlbum(dir, path)
	if album != "" {
		return album
	}
	return cleanAlbumName(dir)
}

func cleanedStem(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	stem = titleSepRe.ReplaceAllString(stem, " ")
	return strings.TrimSpace(multiSpaceRe.ReplaceAllString(stem, " "))
}

func trackNum(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Handle "3/12" format.
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// musicMediaIDFor derives a stable, unique ID for a music file.
//
// Unlike the video mediaIDFor (filename stem only), music libraries contain
// many files with identical names across album directories — "01 - Opening Track.flac"
// can appear in both the stereo and SACD editions of one album, and generic
// names like "CD Track 01" appear across many albums.
// Including the parent directory name and the full filename (with extension)
// makes IDs unique across editions and across FLAC/MP3 duplicates of the same track.
func musicMediaIDFor(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	base := filepath.Base(path)
	id := slugRe.ReplaceAllString(strings.ToLower(dir+" "+base), "-")
	id = strings.Trim(id, "-")
	if id == "" {
		id = fmt.Sprintf("media-%d", time.Now().UnixNano())
	}
	if len(id) > 96 {
		id = id[:96]
	}
	return id
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func ingestMusicOne(ctx context.Context, conn *sql.DB, p string, logger Logger, res *Result) {
	mp, err := ffprobeMusicFile(ctx, p)
	if err != nil {
		logger.Printf("probe failed path=%q err=%v", p, err)
		res.recordFailure(fmt.Sprintf("probe error: %v", err))
		return
	}

	group := DeriveMusicSchedulingGroup(p, mp)
	row := mediaRow{
		ID:               musicMediaIDFor(p),
		Path:             p,
		Directory:        filepath.Dir(p),
		Title:            DeriveMusicTitle(p, mp),
		DurationMs:       mp.DurationMs,
		Container:        mp.FormatName,
		VideoCodec:       "",
		VideoHeight:      0,
		AudioCodec:       mp.AudioCodec,
		CodecCheckPassed: true,
		IngestedAtMs:     time.Now().UTC().UnixMilli(),
		MediaKind:        "music",
	}

	if err := upsertMedia(conn, row); err != nil {
		logger.Printf("upsert failed path=%q err=%v", p, err)
		res.recordFailure(fmt.Sprintf("db error: %v", err))
		return
	}

	if group != "" {
		if err := setCollectionIfNull(conn, row.ID, group, "album"); err != nil {
			logger.Printf("set collection path=%q err=%v", p, err)
		}
	}

	logger.Printf("ingested id=%s title=%q group=%q container=%s audio=%s duration_s=%.0f full_album=%v",
		row.ID, row.Title, group, mp.FormatName, mp.AudioCodec,
		float64(mp.DurationMs)/1000, isSingleFileAlbum(mp))
	res.Passed++
}
