package lcingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/codec"
	"github.com/tckrcr/linearcast/internal/db"
)

func TestIngestReturnsWalkErrorForMissingDirectory(t *testing.T) {
	conn := openTestDB(t)
	defer conn.Close()

	_, err := Ingest(context.Background(), conn, filepath.Join(t.TempDir(), "missing"), nil)
	if err == nil || !strings.Contains(err.Error(), "walk:") {
		t.Fatalf("Ingest err=%v, want walk error", err)
	}
}

func TestIngestReturnsWalkErrorForUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read chmod 000 directories")
	}

	conn := openTestDB(t)
	defer conn.Close()

	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o755)

	_, err := Ingest(context.Background(), conn, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "walk:") {
		t.Fatalf("Ingest err=%v, want walk error", err)
	}
}

func TestIngestReturnsNoMediaErrorForEmptyDirectory(t *testing.T) {
	conn := openTestDB(t)
	defer conn.Close()

	dir := t.TempDir()
	_, err := Ingest(context.Background(), conn, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "no media files under") {
		t.Fatalf("Ingest err=%v, want no media files error", err)
	}
}

func TestIngestRecordsPassAndCodecFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffprobe helper is POSIX-only")
	}

	conn := openTestDB(t)
	defer conn.Close()

	dir := t.TempDir()
	passPath := mustWriteMediaFile(t, dir, "pass.mkv")
	failPath := mustWriteMediaFile(t, dir, "fail.mkv")
	t.Setenv("LINEARCAST_FFPROBE_PATH", writeFakeFFprobe(t, map[string]string{
		// SDR 4K HEVC is now admitted (Phase 2); AV1 has no copy/transcode rung
		// and is still rejected.
		"pass.mkv": fakeFFprobeJSON("hevc", 2160, "aac", "120.0"),
		"fail.mkv": fakeFFprobeJSON("av1", 2160, "aac", "120.0"),
	}))

	res, err := Ingest(context.Background(), conn, dir, nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Total != 2 || res.Passed != 1 || res.Failed != 1 {
		t.Fatalf("Result=%+v, want total=2 passed=1 failed=1", res)
	}
	if len(res.FailureReasons) != 1 || !hasFailureReasonContaining(res, "video_codec=av1") {
		t.Fatalf("FailureReasons=%v, want av1 rejection", res.FailureReasons)
	}

	passRow, err := db.MediaByID(context.Background(), conn, mediaIDFor(passPath))
	if err != nil {
		t.Fatalf("MediaByID(pass): %v", err)
	}
	if passRow == nil || !passRow.CodecCheckPassed {
		t.Fatalf("pass row=%+v, want codec_check_passed=true", passRow)
	}

	failRow, err := db.MediaByID(context.Background(), conn, mediaIDFor(failPath))
	if err != nil {
		t.Fatalf("MediaByID(fail): %v", err)
	}
	if failRow == nil || failRow.CodecCheckPassed || !strings.Contains(failRow.CodecCheckReason, "video_codec=av1") {
		t.Fatalf("fail row=%+v, want codec failure recorded", failRow)
	}
}

func TestIngestFileWithHintsStoresEpisodeOrdering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffprobe helper is POSIX-only")
	}

	conn := openTestDB(t)
	defer conn.Close()

	dir := t.TempDir()
	path := mustWriteMediaFile(t, dir, "ambiguous-episode-name.mkv")
	t.Setenv("LINEARCAST_FFPROBE_PATH", writeFakeFFprobe(t, map[string]string{
		"ambiguous-episode-name.mkv": fakeFFprobeJSON("h264", 1080, "aac", "120.0"),
	}))

	if _, err := IngestFileWithHints(context.Background(), conn, path, 3, 9, nil); err != nil {
		t.Fatalf("IngestFileWithHints: %v", err)
	}

	row, err := db.MediaByID(context.Background(), conn, mediaIDFor(path))
	if err != nil {
		t.Fatalf("MediaByID: %v", err)
	}
	if row == nil {
		t.Fatal("row missing")
	}
	if row.SeasonNumber == nil || *row.SeasonNumber != 3 {
		t.Fatalf("SeasonNumber=%v, want 3", row.SeasonNumber)
	}
	if row.EpisodeNumber == nil || *row.EpisodeNumber != 9 {
		t.Fatalf("EpisodeNumber=%v, want 9", row.EpisodeNumber)
	}
}

func TestBackfillEpisodeOrderingFillsMissingFields(t *testing.T) {
	conn := openTestDB(t)
	defer conn.Close()

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, title, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES
		('ep1', '/srv/media/tv/Show/Show.S02E03.1080p.mkv', '/srv/media/tv/Show', 'Show', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0),
		('movie1', '/srv/media/movies/Movie.2010.1080p.mkv', '/srv/media/movies', 'Movie', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	result, err := BackfillEpisodeOrdering(context.Background(), conn, nil)
	if err != nil {
		t.Fatalf("BackfillEpisodeOrdering: %v", err)
	}
	if result.Scanned != 2 || result.Updated != 1 {
		t.Fatalf("result=%+v, want scanned=2 updated=1", result)
	}

	row, err := db.MediaByID(context.Background(), conn, "ep1")
	if err != nil {
		t.Fatalf("MediaByID(ep1): %v", err)
	}
	if row.SeasonNumber == nil || *row.SeasonNumber != 2 || row.EpisodeNumber == nil || *row.EpisodeNumber != 3 {
		t.Fatalf("ep1 ordering=%v/%v, want 2/3", row.SeasonNumber, row.EpisodeNumber)
	}
	movie, err := db.MediaByID(context.Background(), conn, "movie1")
	if err != nil {
		t.Fatalf("MediaByID(movie1): %v", err)
	}
	if movie.SeasonNumber != nil || movie.EpisodeNumber != nil {
		t.Fatalf("movie ordering=%v/%v, want nil/nil", movie.SeasonNumber, movie.EpisodeNumber)
	}
}

func TestIngestRecordsColorMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffprobe helper is POSIX-only")
	}

	conn := openTestDB(t)
	defer conn.Close()

	dir := t.TempDir()
	hdrPath := mustWriteMediaFile(t, dir, "hdr.mkv")
	sdrPath := mustWriteMediaFile(t, dir, "sdr.mkv")
	t.Setenv("LINEARCAST_FFPROBE_PATH", writeFakeFFprobe(t, map[string]string{
		"hdr.mkv": `{
  "format": {"duration": "120.0", "format_name": "matroska,webm"},
  "streams": [
    {
      "codec_type": "video",
      "codec_name": "hevc",
      "width": 3840,
      "height": 2160,
      "color_transfer": "smpte2084",
      "color_primaries": "bt2020",
      "disposition": {"default": 1, "attached_pic": 0}
    },
    {
      "codec_type": "audio",
      "codec_name": "aac",
      "disposition": {"default": 1, "attached_pic": 0}
    }
  ]
}`,
		"sdr.mkv": fakeFFprobeJSON("h264", 1080, "aac", "120.0"),
	}))

	if _, err := Ingest(context.Background(), conn, dir, nil); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	hdrRow, err := db.MediaByID(context.Background(), conn, mediaIDFor(hdrPath))
	if err != nil {
		t.Fatalf("MediaByID(hdr): %v", err)
	}
	if hdrRow == nil || hdrRow.VideoWidth != 3840 || hdrRow.ColorTransfer != "smpte2084" || hdrRow.ColorPrimaries != "bt2020" {
		t.Fatalf("hdr row=%+v, want width=3840 transfer=smpte2084 primaries=bt2020", hdrRow)
	}
	if !codec.IsHDRTransfer(hdrRow.ColorTransfer) {
		t.Fatalf("IsHDRTransfer(%q)=false, want true", hdrRow.ColorTransfer)
	}

	// Sources without color metadata (and pre-v29 rows) must read back empty.
	sdrRow, err := db.MediaByID(context.Background(), conn, mediaIDFor(sdrPath))
	if err != nil {
		t.Fatalf("MediaByID(sdr): %v", err)
	}
	if sdrRow == nil || sdrRow.ColorTransfer != "" || sdrRow.ColorPrimaries != "" {
		t.Fatalf("sdr row=%+v, want empty color metadata", sdrRow)
	}
	if codec.IsHDRTransfer(sdrRow.ColorTransfer) {
		t.Fatalf("IsHDRTransfer(%q)=true, want false", sdrRow.ColorTransfer)
	}
}

func TestIngestReportsProbeFailureWhenFFprobeMissing(t *testing.T) {
	conn := openTestDB(t)
	defer conn.Close()

	dir := t.TempDir()
	mustWriteMediaFile(t, dir, "missing-ffprobe.mkv")
	t.Setenv("LINEARCAST_FFPROBE_PATH", filepath.Join(t.TempDir(), "does-not-exist"))

	res, err := Ingest(context.Background(), conn, dir, nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Total != 1 || res.Passed != 0 || res.Failed != 1 {
		t.Fatalf("Result=%+v, want total=1 passed=0 failed=1", res)
	}
	if len(res.FailureReasons) != 1 || !hasFailureReasonContaining(res, "probe error") {
		t.Fatalf("FailureReasons=%v, want probe error", res.FailureReasons)
	}
}

func hasFailureReasonContaining(res Result, needle string) bool {
	for reason := range res.FailureReasons {
		if strings.Contains(reason, needle) {
			return true
		}
	}
	return false
}

func TestIngestRecordsVideoBitrate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffprobe helper is POSIX-only")
	}

	conn := openTestDB(t)
	defer conn.Close()

	dir := t.TempDir()
	streamPath := mustWriteMediaFile(t, dir, "stream.mkv")
	fallbackPath := mustWriteMediaFile(t, dir, "fallback.mkv")
	nonePath := mustWriteMediaFile(t, dir, "none.mkv")
	t.Setenv("LINEARCAST_FFPROBE_PATH", writeFakeFFprobe(t, map[string]string{
		// Per-stream bit_rate present: it wins over the whole-container value.
		"stream.mkv": `{
  "format": {"duration": "120.0", "format_name": "matroska,webm", "bit_rate": "20000000"},
  "streams": [
    {"codec_type": "video", "codec_name": "hevc", "height": 2160, "bit_rate": "13582000", "disposition": {"default": 1, "attached_pic": 0}},
    {"codec_type": "audio", "codec_name": "aac", "disposition": {"default": 1, "attached_pic": 0}}
  ]
}`,
		// No per-stream bit_rate (common in Matroska): fall back to format bit_rate.
		"fallback.mkv": `{
  "format": {"duration": "120.0", "format_name": "matroska,webm", "bit_rate": "8256000"},
  "streams": [
    {"codec_type": "video", "codec_name": "h264", "height": 1080, "disposition": {"default": 1, "attached_pic": 0}},
    {"codec_type": "audio", "codec_name": "aac", "disposition": {"default": 1, "attached_pic": 0}}
  ]
}`,
		// Neither present: bitrate is unknown (0).
		"none.mkv": fakeFFprobeJSON("h264", 1080, "aac", "120.0"),
	}))

	if _, err := Ingest(context.Background(), conn, dir, nil); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	for _, tc := range []struct {
		path string
		want int64
	}{
		{streamPath, 13582000},
		{fallbackPath, 8256000},
		{nonePath, 0},
	} {
		row, err := db.MediaByID(context.Background(), conn, mediaIDFor(tc.path))
		if err != nil {
			t.Fatalf("MediaByID(%s): %v", tc.path, err)
		}
		if row == nil || row.VideoBitrateBps != tc.want {
			t.Fatalf("%s VideoBitrateBps=%d, want %d (row=%+v)", filepath.Base(tc.path), row.VideoBitrateBps, tc.want, row)
		}
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "linearcast.db")
	conn, err := db.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	if err := db.ApplySchema(context.Background(), conn); err != nil {
		conn.Close()
		t.Fatalf("ApplySchema: %v", err)
	}
	return conn
}

func mustWriteMediaFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
	return path
}

func writeFakeFFprobe(t *testing.T, responses map[string]string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ffprobe")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\nfor last; do :; done\ncase \"$(basename \"$last\")\" in\n")
	for name, body := range responses {
		fmt.Fprintf(&script, "%s)\ncat <<'EOF'\n%s\nEOF\n;;\n", name, body)
	}
	script.WriteString("*) echo \"unexpected probe target: $last\" >&2; exit 1 ;;\nesac\n")
	if err := os.WriteFile(path, []byte(script.String()), 0o755); err != nil {
		t.Fatalf("WriteFile(ffprobe): %v", err)
	}
	return path
}

func fakeFFprobeJSON(videoCodec string, videoHeight int, audioCodec, duration string) string {
	return fmt.Sprintf(`{
  "format": {"duration": %q, "format_name": "matroska,webm"},
  "streams": [
    {
      "codec_type": "video",
      "codec_name": %q,
      "height": %d,
      "disposition": {"default": 1, "attached_pic": 0}
    },
    {
      "codec_type": "audio",
      "codec_name": %q,
      "disposition": {"default": 1, "attached_pic": 0}
    }
  ]
}`, duration, videoCodec, videoHeight, audioCodec)
}
