package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/sysinfo"
)

func reportAndPing(ctx context.Context, client *http.Client, cfg config) (pingResponse, error) {
	body, err := json.Marshal(collectEncoderReport(ctx, cfg.WorkDir))
	if err != nil {
		return pingResponse{}, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, controlRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.AdminURL+"/api/encoder/ping", strings.NewReader(string(body)))
	if err != nil {
		return pingResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return pingResponse{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return pingResponse{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var ping pingResponse
	if err := json.Unmarshal(respBody, &ping); err != nil {
		return pingResponse{}, fmt.Errorf("decode encoder ping: %w", err)
	}
	return ping, nil
}

func collectEncoderReport(ctx context.Context, workDir string) encoderReport {
	hostname, _ := os.Hostname()
	ffmpegPath, _ := ffmpegexec.Resolve("ffmpeg")
	ffprobePath, _ := ffmpegexec.Resolve("ffprobe")
	return encoderReport{
		Hostname:    hostname,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
		Encoders:    sysinfo.DetectHardwareEncoders(ctx),
		NVIDIAGPUs:  sysinfo.DetectNVIDIAGPUs(ctx),
		WorkDir:     workDir,
		DiskFreeGB:  sysinfo.DiskFreeGB(workDir),
	}
}

func claimOnce(ctx context.Context, client *http.Client, cfg config) (claimResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, controlRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.AdminURL+"/api/encoder/claim", strings.NewReader(`{}`))
	if err != nil {
		return claimResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return claimResponse{}, fmt.Errorf("claim failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, resp.Body)
		return claimResponse{}, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return claimResponse{}, fmt.Errorf("claim returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var claim claimResponse
	if err := json.Unmarshal(body, &claim); err != nil {
		return claimResponse{}, fmt.Errorf("decode claim: %w", err)
	}
	if claim.PackageID == "" || claim.MediaID == "" {
		return claimResponse{}, fmt.Errorf("claim returned incomplete response: %s", strings.TrimSpace(string(body)))
	}
	return claim, nil
}

func downloadMedia(ctx context.Context, client *http.Client, cfg config, mediaID, target string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.AdminURL+"/api/encoder/media/"+url.PathEscape(mediaID), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download media failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("download media returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	f, err := os.Create(target)
	if err != nil {
		return 0, fmt.Errorf("create download file: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return n, fmt.Errorf("write download file: %w", err)
	}
	return n, nil
}

func failClaim(ctx context.Context, client *http.Client, cfg config, packageID, reason string) error {
	return failClaimWithKind(ctx, client, cfg, packageID, "transient", reason)
}

func failClaimWithKind(ctx context.Context, client *http.Client, cfg config, packageID, kind, reason string) error {
	if kind != "terminal" {
		kind = "transient"
	}
	body := strings.NewReader(`{"kind":` + quoteJSON(kind) + `,"reason":` + quoteJSON(reason) + `}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.AdminURL+"/api/encoder/jobs/"+url.PathEscape(packageID)+"/fail", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("release claim failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release claim returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func completeClaim(ctx context.Context, client *http.Client, cfg config, packageID, packageDir string) (completeResponse, error) {
	pr, pw := io.Pipe()
	go func() {
		err := writePackageTar(pw, packageDir)
		_ = pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.AdminURL+"/api/encoder/jobs/"+url.PathEscape(packageID)+"/complete", pr)
	if err != nil {
		return completeResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := client.Do(req)
	if err != nil {
		return completeResponse{}, fmt.Errorf("complete upload failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return completeResponse{}, fmt.Errorf("complete returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out completeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return completeResponse{}, fmt.Errorf("decode complete response: %w", err)
	}
	if !out.OK {
		return completeResponse{}, fmt.Errorf("complete response was not ok: %s", strings.TrimSpace(string(body)))
	}
	return out, nil
}

func writePackageTar(w io.Writer, packageDir string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()
	for _, name := range []string{layout.InitName, layout.PlaylistName} {
		if err := addTarFile(tw, filepath.Join(packageDir, name), name); err != nil {
			return err
		}
	}
	matches, err := filepath.Glob(filepath.Join(packageDir, layout.SegmentGlob))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("encoded package has no %s files", layout.SegmentGlob)
	}
	for _, p := range matches {
		if err := addTarFile(tw, p, filepath.Base(p)); err != nil {
			return err
		}
	}
	return nil
}

func addTarFile(tw *tar.Writer, filePath, name string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", filePath)
	}
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func startHeartbeat(ctx context.Context, client *http.Client, cfg config, packageID string, out io.Writer) func() {
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		warnings := newSampledMessage(logSampleInterval)
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		if err := heartbeat(hctx, client, cfg, packageID); err != nil {
			warnings.logf(out, "heartbeat package=%s error=%v", packageID, err)
		} else {
			warnings.reset()
		}
		for {
			select {
			case <-hctx.Done():
				return
			case <-ticker.C:
				if err := heartbeat(hctx, client, cfg, packageID); err != nil {
					warnings.logf(out, "heartbeat package=%s error=%v", packageID, err)
				} else {
					warnings.reset()
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func heartbeat(ctx context.Context, client *http.Client, cfg config, packageID string) error {
	reqCtx, cancel := context.WithTimeout(ctx, controlRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.AdminURL+"/api/encoder/jobs/"+url.PathEscape(packageID)+"/heartbeat", strings.NewReader(`{"leaseTtlSeconds":60}`))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func sourceFilename(mediaPath, mediaID string) string {
	name := path.Base(strings.ReplaceAll(mediaPath, `\`, `/`))
	if name == "." || name == "/" || name == "" {
		return mediaID
	}
	return name
}

func getOK(ctx context.Context, client *http.Client, endpoint, bearer string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}
