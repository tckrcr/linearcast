package ffmpegexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesSpecificPathEnv(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, exeNameForTest("ffmpeg"))
	writeTool(t, ffmpeg)
	t.Setenv(envFFmpegPath, ffmpeg)
	t.Setenv(envFFmpegDir, "")
	t.Setenv(envEncoderFFmpegDir, "")

	got, err := Resolve("ffmpeg")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != ffmpeg {
		t.Fatalf("Resolve=%q, want %q", got, ffmpeg)
	}
}

func TestResolveUsesConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	ffprobe := filepath.Join(dir, exeNameForTest("ffprobe"))
	writeTool(t, ffprobe)
	t.Setenv(envFFprobePath, "")
	t.Setenv(envFFmpegDir, dir)
	t.Setenv(envEncoderFFmpegDir, "")

	got, err := Resolve("ffprobe")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != ffprobe {
		t.Fatalf("Resolve=%q, want %q", got, ffprobe)
	}
}

func TestResolveRejectsUnknownTool(t *testing.T) {
	_, err := Resolve("nvidia-smi")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err=%v, want unsupported tool", err)
	}
}

func writeTool(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
}

func exeNameForTest(tool string) string {
	name, err := executableName(tool)
	if err != nil {
		panic(err)
	}
	return name
}
