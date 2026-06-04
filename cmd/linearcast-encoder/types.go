package main

import (
	"time"

	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/sysinfo"
)

const (
	envAdminURL    = "LINEARCAST_ADMIN_URL"
	envAPIKey      = "LINEARCAST_ENCODER_API_KEY"
	envWorkDir     = "LINEARCAST_ENCODER_WORK_DIR"
	envConcurrency = "LINEARCAST_ENCODER_CONCURRENCY"
)

const (
	defaultIdlePollInterval = 30 * time.Second
	startupPingInterval     = 5 * time.Second
	errorPollInterval       = 60 * time.Second
	maxErrorPollInterval    = 5 * time.Minute
	controlRequestTimeout   = 10 * time.Second
	pingInterval            = 30 * time.Second
	heartbeatInterval       = 20 * time.Second
	logSampleInterval       = 5 * time.Minute
)

type config struct {
	AdminURL    string
	APIKey      string
	WorkDir     string
	Concurrency int // 0 means use the value from the server's ping response
}

type pingResponse struct {
	OK          bool   `json:"ok"`
	EncoderID   string `json:"encoderId"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Concurrency int    `json:"concurrency"`
}

type encoderReport struct {
	Hostname    string              `json:"hostname,omitempty"`
	OS          string              `json:"os,omitempty"`
	Arch        string              `json:"arch,omitempty"`
	FFmpegPath  string              `json:"ffmpegPath,omitempty"`
	FFprobePath string              `json:"ffprobePath,omitempty"`
	Encoders    []string            `json:"encoders,omitempty"`
	NVIDIAGPUs  []sysinfo.NVIDIAGPU `json:"nvidiaGpus,omitempty"`
	WorkDir     string              `json:"workDir,omitempty"`
	DiskFreeGB  float64             `json:"diskFreeGB,omitempty"`
	Extra       map[string]string   `json:"extra,omitempty"`
}

type claimResponse struct {
	PackageID        string                 `json:"packageId"`
	MediaID          string                 `json:"mediaId"`
	MediaPath        string                 `json:"mediaPath"`
	RenditionProfile string                 `json:"renditionProfile"`
	Profile          packageprofile.Profile `json:"profile"`
}

type completeResponse struct {
	OK           bool  `json:"ok"`
	SegmentCount int   `json:"segmentCount"`
	DurationMs   int64 `json:"durationMs"`
}
