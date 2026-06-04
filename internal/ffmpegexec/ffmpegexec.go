// Package ffmpegexec resolves ffmpeg/ffprobe binaries for Linearcast tools.
package ffmpegexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	envFFmpegPath  = "LINEARCAST_FFMPEG_PATH"
	envFFprobePath = "LINEARCAST_FFPROBE_PATH"
	envFFmpegDir   = "LINEARCAST_FFMPEG_DIR"

	// Kept for compatibility with the existing remote-encoder deploy script.
	envEncoderFFmpegDir = "LINEARCAST_ENCODER_FFMPEG_DIR"
)

// Resolve returns the executable path for ffmpeg or ffprobe.
func Resolve(tool string) (string, error) {
	name, err := executableName(tool)
	if err != nil {
		return "", err
	}
	if p := strings.TrimSpace(os.Getenv(toolPathEnv(tool))); p != "" {
		if err := executableFile(p); err != nil {
			return "", fmt.Errorf("%s=%s: %w", toolPathEnv(tool), p, err)
		}
		return p, nil
	}
	for _, env := range []string{envFFmpegDir, envEncoderFFmpegDir} {
		if dir := strings.TrimSpace(os.Getenv(env)); dir != "" {
			p := filepath.Join(dir, name)
			if err := executableFile(p); err != nil {
				return "", fmt.Errorf("%s=%s: %s: %w", env, dir, name, err)
			}
			return p, nil
		}
	}
	for _, p := range adjacentCandidates(name) {
		if err := executableFile(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath(tool); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found; set %s/%s or place it in tools, bin, or ffmpeg/bin next to the binary", tool, toolPathEnv(tool), envFFmpegDir)
}

// CommandContext resolves tool and returns an exec.Cmd for it.
func CommandContext(ctx context.Context, tool string, args ...string) (*exec.Cmd, error) {
	p, err := Resolve(tool)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, p, args...), nil
}

func executableName(tool string) (string, error) {
	if tool != "ffmpeg" && tool != "ffprobe" {
		return "", fmt.Errorf("unsupported ffmpeg tool %q", tool)
	}
	if runtime.GOOS == "windows" {
		return tool + ".exe", nil
	}
	return tool, nil
}

func toolPathEnv(tool string) string {
	if tool == "ffprobe" {
		return envFFprobePath
	}
	return envFFmpegPath
}

func adjacentCandidates(name string) []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	root := filepath.Dir(exe)
	return []string{
		filepath.Join(root, name),
		filepath.Join(root, "tools", name),
		filepath.Join(root, "bin", name),
		filepath.Join(root, "ffmpeg", "bin", name),
	}
}

func executableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	return nil
}
