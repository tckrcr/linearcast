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
		"pass.mkv": fakeFFprobeJSON("h264", 1080, "aac", "120.0"),
		"fail.mkv": fakeFFprobeJSON("hevc", 2160, "aac", "120.0"),
	}))

	res, err := Ingest(context.Background(), conn, dir, nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Total != 2 || res.Passed != 1 || res.Failed != 1 {
		t.Fatalf("Result=%+v, want total=2 passed=1 failed=1", res)
	}
	if len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "video_codec=hevc") || !strings.Contains(res.Failures[0], "video_height=2160") {
		t.Fatalf("Failures=%v, want hevc/2160 rejection", res.Failures)
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
	if failRow == nil || failRow.CodecCheckPassed || !strings.Contains(failRow.CodecCheckReason, "video_codec=hevc") {
		t.Fatalf("fail row=%+v, want codec failure recorded", failRow)
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
	if len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "probe error") {
		t.Fatalf("Failures=%v, want probe error", res.Failures)
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
