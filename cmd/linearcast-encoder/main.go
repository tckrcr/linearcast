// cmd/linearcast-encoder is the encoder client binary.
//
// When LINEARCAST_DB is set it runs in local mode: claims jobs directly from
// SQLite and drives packager.Worker with zero download/upload overhead.
// When LINEARCAST_DB is absent it runs in remote mode: HTTP claim, source
// download, encode, tar upload — exactly as before.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type backoffState struct {
	base    time.Duration
	max     time.Duration
	current time.Duration
}

func newBackoff(base, max time.Duration) backoffState {
	return backoffState{base: base, max: max}
}

func (b *backoffState) next() time.Duration {
	if b.current <= 0 {
		b.current = b.base
	} else {
		b.current *= 2
		if b.current > b.max {
			b.current = b.max
		}
	}
	return b.current
}

func (b *backoffState) reset() {
	b.current = 0
}

type sampledMessage struct {
	interval   time.Duration
	lastLog    time.Time
	suppressed int
}

func newSampledMessage(interval time.Duration) sampledMessage {
	return sampledMessage{interval: interval}
}

func (s *sampledMessage) logf(out io.Writer, format string, args ...any) {
	now := time.Now()
	if s.lastLog.IsZero() || now.Sub(s.lastLog) >= s.interval {
		msg := fmt.Sprintf(format, args...)
		if s.suppressed > 0 {
			fmt.Fprintf(out, "%s suppressed=%d\n", msg, s.suppressed)
		} else {
			fmt.Fprintln(out, msg)
		}
		s.lastLog = now
		s.suppressed = 0
		return
	}
	s.suppressed++
}

func (s *sampledMessage) reset() {
	s.lastLog = time.Time{}
	s.suppressed = 0
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "linearcast-encoder: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) != 1 {
		return usageError()
	}
	if getenv("LINEARCAST_DB") != "" {
		return runLocal(ctx, args[0], getenv, out)
	}
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	client := &http.Client{}
	switch args[0] {
	case "check":
		return runCheck(ctx, client, cfg, out)
	case "download-once":
		return runDownloadOnce(ctx, client, cfg, out)
	case "run-once":
		return runEncodeOnce(ctx, client, cfg, out)
	case "run":
		return runEncodeLoop(ctx, client, cfg, out)
	case "install":
		return runInstall(ctx, client, cfg, out)
	case "uninstall":
		return runUninstall(ctx, client, cfg, out)
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: linearcast-encoder <check|download-once|run-once|run|install|uninstall>")
}

// ── remote mode ───────────────────────────────────────────────────────────────

func runCheck(ctx context.Context, client *http.Client, cfg config, out io.Writer) error {
	if err := getOK(ctx, client, cfg.AdminURL+"/api/healthz", ""); err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	fmt.Fprintln(out, "healthz ok")

	ping, err := reportAndPing(ctx, client, cfg)
	if err != nil {
		return fmt.Errorf("encoder ping failed: %w", err)
	}
	if !ping.OK || ping.EncoderID == "" {
		return fmt.Errorf("encoder ping returned incomplete response")
	}
	fmt.Fprintf(out, "encoder ping ok id=%s name=%q status=%s\n", ping.EncoderID, ping.Name, ping.Status)
	return nil
}

func runDownloadOnce(ctx context.Context, client *http.Client, cfg config, out io.Writer) error {
	claim, err := claimOnce(ctx, client, cfg)
	if err != nil {
		return err
	}
	if claim.MediaID == "" {
		fmt.Fprintln(out, "no claimable media")
		return nil
	}
	fmt.Fprintf(out, "claimed package=%s media=%s profile=%s\n", claim.PackageID, claim.MediaID, claim.RenditionProfile)

	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	filename := sourceFilename(claim.MediaPath, claim.MediaID)
	target := filepath.Join(cfg.WorkDir, filename)
	n, err := downloadMedia(ctx, client, cfg, claim.MediaID, target)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "downloaded media=%s bytes=%d path=%s\n", claim.MediaID, n, target)
	if err := failClaim(ctx, client, cfg, claim.PackageID, "download-only check complete"); err != nil {
		return err
	}
	fmt.Fprintf(out, "released package=%s status=pending\n", claim.PackageID)
	return nil
}

func runEncodeOnce(ctx context.Context, client *http.Client, cfg config, out io.Writer) error {
	if _, err := reportAndPing(ctx, client, cfg); err != nil {
		fmt.Fprintf(out, "encoder report warning: %v\n", err)
	}
	claim, err := claimOnce(ctx, client, cfg)
	if err != nil {
		return err
	}
	if claim.MediaID == "" {
		fmt.Fprintln(out, "no claimable media")
		return nil
	}
	if claim.Profile.Name == "" {
		return fmt.Errorf("claim response missing profile config")
	}
	fmt.Fprintf(out, "claimed package=%s media=%s profile=%s\n", claim.PackageID, claim.MediaID, claim.RenditionProfile)
	return encodeJob(ctx, client, cfg, claim, out)
}

func runEncodeLoop(ctx context.Context, client *http.Client, cfg config, out io.Writer) error {
	ping, err := waitForStartupPing(ctx, client, cfg, out)
	if err != nil {
		return err
	}
	concurrency := resolveConcurrency(cfg.Concurrency, ping.Concurrency)

	sweepWorkDir(cfg.WorkDir, out)
	fmt.Fprintf(out, "encoder loop starting concurrency=%d idle_poll=%s error_poll=%s max_error_poll=%s\n",
		concurrency, defaultIdlePollInterval, errorPollInterval, maxErrorPollInterval)

	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		warnings := newSampledMessage(logSampleInterval)
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				if _, err := reportAndPing(pingCtx, client, cfg); err != nil {
					warnings.logf(out, "encoder ping warning: %v", err)
				} else {
					warnings.reset()
				}
			}
		}
	}()

	sem := make(chan struct{}, concurrency)
	claimBackoff := newBackoff(errorPollInterval, maxErrorPollInterval)
	claimErrors := newSampledMessage(logSampleInterval)
	idleLogs := newSampledMessage(logSampleInterval)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}

		claim, err := claimOnce(ctx, client, cfg)
		if err != nil {
			<-sem
			delay := claimBackoff.next()
			claimErrors.logf(out, "encoder loop claim_error=%v next_retry=%s", err, delay)
			if err := sleepContext(ctx, delay); err != nil {
				return err
			}
			continue
		}
		claimBackoff.reset()
		claimErrors.reset()
		if claim.MediaID == "" {
			<-sem
			idleLogs.logf(out, "no claimable media")
			if err := sleepContext(ctx, defaultIdlePollInterval); err != nil {
				return err
			}
			continue
		}
		idleLogs.reset()
		if claim.Profile.Name == "" {
			<-sem
			return fmt.Errorf("claim response missing profile config")
		}
		fmt.Fprintf(out, "claimed package=%s media=%s profile=%s\n",
			claim.PackageID, claim.MediaID, claim.RenditionProfile)
		go func(c claimResponse) {
			defer func() { <-sem }()
			if err := encodeJob(ctx, client, cfg, c, out); err != nil {
				fmt.Fprintf(out, "encoder job error package=%s: %v\n", c.PackageID, err)
			}
		}(claim)
	}
}

func waitForStartupPing(ctx context.Context, client *http.Client, cfg config, out io.Writer) (pingResponse, error) {
	backoff := newBackoff(startupPingInterval, maxErrorPollInterval)
	warnings := newSampledMessage(logSampleInterval)
	for {
		ping, err := reportAndPing(ctx, client, cfg)
		if err == nil && ping.OK && ping.EncoderID != "" {
			return ping, nil
		}
		if err == nil {
			err = fmt.Errorf("encoder ping returned incomplete response")
		}
		delay := backoff.next()
		warnings.logf(out, "startup ping failed: %v next_retry=%s", err, delay)
		if err := sleepContext(ctx, delay); err != nil {
			return pingResponse{}, err
		}
	}
}
