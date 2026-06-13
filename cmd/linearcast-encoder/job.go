package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
)

// encodeJob executes one claimed encode job: download → ffmpeg → complete/fail.
// Claiming and pinging are the caller's responsibility.
func encodeJob(ctx context.Context, client *http.Client, cfg config, claim claimResponse, out io.Writer) error {
	stopHeartbeat := startHeartbeat(ctx, client, cfg, claim.PackageID, out)
	defer stopHeartbeat()

	jobDir := filepath.Join(cfg.WorkDir, claim.PackageID)
	sourceDir := filepath.Join(jobDir, "source")
	packageDir := filepath.Join(jobDir, "package")
	if err := os.RemoveAll(jobDir); err != nil {
		return fmt.Errorf("clear job dir: %w", err)
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		return fmt.Errorf("create source dir: %w", err)
	}
	sourcePath := filepath.Join(sourceDir, sourceFilename(claim.MediaPath, claim.MediaID))
	n, err := downloadMedia(ctx, client, cfg, claim.MediaID, sourcePath)
	if err != nil {
		_ = failClaimWithKind(ctx, client, cfg, claim.PackageID, classifyClaimFailure(err), err.Error())
		return err
	}
	fmt.Fprintf(out, "downloaded media=%s bytes=%d path=%s\n", claim.MediaID, n, sourcePath)

	err = packager.EncodePackageOutput(ctx, sourcePath, packageDir, db.ScheduleGridMs, "veryfast", claim.Profile)
	if err != nil {
		_ = failClaimWithKind(ctx, client, cfg, claim.PackageID, classifyClaimFailure(err), err.Error())
		return fmt.Errorf("encode package: %w", err)
	}
	fmt.Fprintf(out, "encoded package=%s path=%s\n", claim.PackageID, packageDir)

	resp, err := completeClaim(ctx, client, cfg, claim.PackageID, packageDir)
	if err != nil {
		_ = failClaimWithKind(ctx, client, cfg, claim.PackageID, classifyClaimFailure(err), err.Error())
		return err
	}
	fmt.Fprintf(out, "completed package=%s segments=%d duration_ms=%d\n",
		claim.PackageID, resp.SegmentCount, resp.DurationMs)
	if err := os.RemoveAll(jobDir); err != nil {
		fmt.Fprintf(out, "cleanup warning package=%s path=%s err=%v\n", claim.PackageID, jobDir, err)
	}
	return nil
}

func classifyClaimFailure(err error) string {
	if isTerminalClaimFailure(err) {
		return "terminal"
	}
	return "transient"
}

func isTerminalClaimFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	terminalPatterns := []string{
		"download media returned 404",
		"source file unavailable:",
		" is a directory",
		"source has no video stream",
		"source has no audio stream",
		"source video codec ",
		" is not valid for profile ",
		"unsupported video mode ",
		"unsupported audio mode ",
		"invalid data found when processing input",
	}
	for _, pattern := range terminalPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

// resolveConcurrency picks the effective worker count. A config override wins;
// otherwise the server's ping value is used; finally falls back to 1.
func resolveConcurrency(cfgOverride, serverValue int) int {
	if cfgOverride > 0 {
		return cfgOverride
	}
	if serverValue > 0 {
		return serverValue
	}
	return 1
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func sweepWorkDir(workDir string, out io.Writer) {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return
	}
	var removed int
	var freed int64
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(workDir, e.Name())
		size, _ := dirSize(path)
		if err := os.RemoveAll(path); err == nil {
			removed++
			freed += size
		}
	}
	if removed > 0 {
		fmt.Fprintf(out, "sweep work_dir=%s removed=%d freed=%d\n", workDir, removed, freed)
	}
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}
