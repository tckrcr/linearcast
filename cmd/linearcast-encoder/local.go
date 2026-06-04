package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
	"github.com/tckrcr/linearcast/internal/sysinfo"
)

// ── local mode ────────────────────────────────────────────────────────────────

type localConfig struct {
	dbPath     string
	outputRoot string
	workDir    string
}

func loadLocalConfig(getenv func(string) string) (localConfig, error) {
	cfg := localConfig{
		dbPath: strings.TrimSpace(getenv("LINEARCAST_DB")),
	}
	cfg.outputRoot = strings.TrimSpace(getenv("LINEARCAST_PACKAGE_ROOT"))
	if cfg.outputRoot == "" {
		if cacheRoot := strings.TrimSpace(getenv("CACHE_DIR")); cacheRoot != "" {
			cfg.outputRoot = cacheRoot + "/packages"
		}
	}
	if cfg.outputRoot == "" {
		return cfg, fmt.Errorf("LINEARCAST_PACKAGE_ROOT or CACHE_DIR is required in local mode")
	}
	cfg.workDir = strings.TrimSpace(getenv("LINEARCAST_WORK_DIR"))
	if cfg.workDir == "" {
		if cacheRoot := strings.TrimSpace(getenv("CACHE_DIR")); cacheRoot != "" {
			cfg.workDir = cacheRoot + "/encoder-work"
		}
	}
	return cfg, nil
}

func runLocal(ctx context.Context, subcmd string, getenv func(string) string, out io.Writer) error {
	if subcmd != "run" {
		return fmt.Errorf("local mode (LINEARCAST_DB is set) only supports the 'run' subcommand, got %q", subcmd)
	}

	lcfg, err := loadLocalConfig(getenv)
	if err != nil {
		return err
	}

	conn, err := db.OpenReadWrite(lcfg.dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer conn.Close()
	if err := db.ApplySchema(ctx, conn); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := db.VerifySchema(ctx, conn); err != nil {
		return fmt.Errorf("verify schema: %w", err)
	}

	if err := os.MkdirAll(lcfg.outputRoot, 0o755); err != nil {
		return fmt.Errorf("create output root %s: %w", lcfg.outputRoot, err)
	}
	if lcfg.workDir != "" {
		if err := os.MkdirAll(lcfg.workDir, 0o755); err != nil {
			return fmt.Errorf("create work dir %s: %w", lcfg.workDir, err)
		}
	}

	hostname, _ := os.Hostname()
	encoderName := "Local Worker"
	if hostname != "" {
		encoderName = "Local Worker (" + hostname + ")"
	}
	nowMs := time.Now().UTC().UnixMilli()
	encoderID, err := db.EnsureLocalEncoder(ctx, conn, encoderName, nowMs)
	if err != nil {
		return fmt.Errorf("register local encoder: %w", err)
	}
	enc, err := db.GetEncoderByID(ctx, conn, encoderID)
	if err != nil || enc == nil {
		return fmt.Errorf("read local encoder row: %v", err)
	}
	fmt.Fprintf(out, "local encoder id=%s concurrency=%d\n",
		encoderID, enc.Concurrency)

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runLocalPingLoop(sigCtx, conn, encoderID, lcfg.workDir, lcfg.outputRoot, out)

	w := &packager.Worker{
		DB:         conn,
		OutputRoot: lcfg.outputRoot,
		WorkDir:    lcfg.workDir,
	}
	fmt.Fprintf(out, "local encoder starting output=%s work=%s\n", lcfg.outputRoot, lcfg.workDir)
	w.Run(sigCtx)
	fmt.Fprintf(out, "local encoder shutting down\n")
	return nil
}

func runLocalPingLoop(ctx context.Context, conn *sql.DB, encoderID, workDir, outputRoot string, out io.Writer) {
	diskPath := workDir
	if diskPath == "" {
		diskPath = outputRoot
	}
	ping := func() {
		caps := buildLocalCapabilities(ctx, diskPath)
		b, _ := json.Marshal(caps)
		nowMs := time.Now().UTC().UnixMilli()
		if err := db.UpdateEncoderCapabilities(ctx, conn, encoderID, string(b), nowMs); err != nil {
			fmt.Fprintf(out, "local encoder ping warning: %v\n", err)
		}
	}
	ping()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ping()
		}
	}
}

func buildLocalCapabilities(ctx context.Context, diskPath string) map[string]any {
	hostname, _ := os.Hostname()
	reported := map[string]any{
		"hostname": hostname,
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
	}
	if diskPath != "" {
		if gb := sysinfo.DiskFreeGB(diskPath); gb > 0 {
			reported["diskFreeGB"] = gb
		}
	}
	if gpus := sysinfo.DetectNVIDIAGPUs(ctx); len(gpus) > 0 {
		reported["nvidiaGpus"] = gpus
	}
	return map[string]any{
		"type":         "local",
		"lastReportMs": time.Now().UTC().UnixMilli(),
		"reported":     reported,
	}
}
