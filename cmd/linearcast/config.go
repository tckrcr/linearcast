package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	clockCheckStrict   = "strict"
	clockCheckDisabled = "disabled"
)

type startupConfig struct {
	dbPath         string
	addr           string
	encodingDir    string
	clockCheckMode string
	// cacheDir is the package cache root (CACHE_DIR). Optional: when set, the
	// playback process samples <cacheDir>/packages into the package-cache-bytes
	// gauge. Blank disables that sampler.
	cacheDir string
	// onDemandMaxConcurrent caps concurrent on-demand encoder processes. This
	// legitimately varies per deployment because it tracks the host's encode
	// capacity. Blank/invalid falls back to defaultOnDemandMaxConcurrent.
	onDemandMaxConcurrent int
	// onDemandPlaybackLagMs is how far behind wall clock the on-demand encoder
	// seeks before it starts producing live HLS segments.
	onDemandPlaybackLagMs int64
	// onDemandWarmupMs is the head-start buffer between the encoder seek point
	// and the served media position.
	onDemandWarmupMs int64
}

// defaultOnDemandMaxConcurrent matches ondemand.NewManager's built-in default;
// duplicated here only so a blank env var resolves to a positive value at the
// call site. Keep the two in sync.
const defaultOnDemandMaxConcurrent = 4

func loadStartupConfig(getenv func(string) string) (startupConfig, error) {
	cfg := startupConfig{
		dbPath:         getenv("LINEARCAST_DB"),
		addr:           getenv("LINEARCAST_ADDR"),
		encodingDir:    getenv("LINEARCAST_ENCODING_DIR"),
		clockCheckMode: strings.ToLower(strings.TrimSpace(getenv("LINEARCAST_CLOCK_CHECK"))),
		cacheDir:       strings.TrimSpace(getenv("CACHE_DIR")),
	}
	cfg.onDemandMaxConcurrent = defaultOnDemandMaxConcurrent
	if raw := strings.TrimSpace(getenv("LINEARCAST_ON_DEMAND_MAX_CONCURRENT")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return startupConfig{}, fmt.Errorf("LINEARCAST_ON_DEMAND_MAX_CONCURRENT must be a positive integer (got %q)", raw)
		}
		cfg.onDemandMaxConcurrent = n
	}
	var err error
	cfg.onDemandPlaybackLagMs, err = parseOptionalPositiveInt64(
		getenv("LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS"),
		defaultOnDemandPlaybackLagMs,
		"LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS",
	)
	if err != nil {
		return startupConfig{}, err
	}
	cfg.onDemandWarmupMs, err = parseOptionalPositiveInt64(
		getenv("LINEARCAST_ON_DEMAND_WARMUP_MS"),
		defaultOnDemandWarmupMs,
		"LINEARCAST_ON_DEMAND_WARMUP_MS",
	)
	if err != nil {
		return startupConfig{}, err
	}
	if cfg.dbPath == "" {
		return startupConfig{}, fmt.Errorf("LINEARCAST_DB is required")
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if err := validateListenAddr("LINEARCAST_ADDR", cfg.addr); err != nil {
		return startupConfig{}, err
	}
	if cfg.encodingDir == "" {
		cfg.encodingDir = filepath.Join(os.TempDir(), "linearcast-encodings")
	}
	if cfg.clockCheckMode == "" {
		cfg.clockCheckMode = clockCheckStrict
	}
	switch cfg.clockCheckMode {
	case clockCheckStrict, clockCheckDisabled:
	default:
		return startupConfig{}, fmt.Errorf("LINEARCAST_CLOCK_CHECK must be %q or %q", clockCheckStrict, clockCheckDisabled)
	}
	return cfg, nil
}

func parseOptionalPositiveInt64(raw string, def int64, name string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer in milliseconds (got %q)", name, raw)
	}
	return n, nil
}

// parseConfigBool parses the boolean spellings accepted across Linearcast env
// vars. strconv.ParseBool is too narrow (no yes/on/off), so this is the shared
// truthy/falsy set.
func parseConfigBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", raw)
	}
}

// validateListenAddr rejects an address that net.Listen would later fail on, so
// a malformed listen address surfaces as a clear config error at startup
// instead of an opaque ListenAndServe failure. The port must be numeric to
// match the nginx upstream the container entrypoint derives from it.
func validateListenAddr(name, addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s must be a host:port listen address (got %q): %w", name, addr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%s must end in a numeric port 1-65535 (got %q)", name, addr)
	}
	return nil
}
