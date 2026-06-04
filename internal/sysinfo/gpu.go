// Package sysinfo provides hardware and system capability detection shared by
// the remote encoder client and the admin server.
package sysinfo

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/ffmpegexec"
)

// NVIDIAGPU describes a detected NVIDIA GPU.
type NVIDIAGPU struct {
	Name          string `json:"name,omitempty"`
	DriverVersion string `json:"driverVersion,omitempty"`
}

// DetectNVIDIAGPUs runs nvidia-smi and returns any detected GPUs.
// Returns nil (not an error) when nvidia-smi is missing or fails.
func DetectNVIDIAGPUs(ctx context.Context) []NVIDIAGPU {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	var gpus []NVIDIAGPU
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		gpu := NVIDIAGPU{Name: strings.TrimSpace(parts[0])}
		if len(parts) > 1 {
			gpu.DriverVersion = strings.TrimSpace(parts[1])
		}
		gpus = append(gpus, gpu)
	}
	return gpus
}

var hwEncoderNames = []string{
	"h264_nvenc",
	"hevc_nvenc",
	"av1_nvenc",
	"h264_videotoolbox",
	"hevc_videotoolbox",
}

// DetectHardwareEncoders runs ffmpeg -encoders and returns the names of any
// hardware encoders that are present. Returns nil (not an error) when ffmpeg
// is missing or fails.
func DetectHardwareEncoders(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd, err := ffmpegexec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-encoders")
	if err != nil {
		return nil
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var encoders []string
	for _, line := range strings.Split(string(out), "\n") {
		for _, name := range hwEncoderNames {
			if strings.Contains(line, name) && !seen[name] {
				seen[name] = true
				encoders = append(encoders, name)
			}
		}
	}
	return encoders
}
