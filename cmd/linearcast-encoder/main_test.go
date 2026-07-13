package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestBackoffStateCapsAndResets(t *testing.T) {
	backoff := newBackoff(2*time.Second, 5*time.Second)
	if got := backoff.next(); got != 2*time.Second {
		t.Fatalf("first backoff=%s, want 2s", got)
	}
	if got := backoff.next(); got != 4*time.Second {
		t.Fatalf("second backoff=%s, want 4s", got)
	}
	if got := backoff.next(); got != 5*time.Second {
		t.Fatalf("third backoff=%s, want capped 5s", got)
	}
	backoff.reset()
	if got := backoff.next(); got != 2*time.Second {
		t.Fatalf("reset backoff=%s, want 2s", got)
	}
}

func TestSampledMessageSuppressesRepeatedLogs(t *testing.T) {
	var out bytes.Buffer
	sampled := newSampledMessage(time.Hour)
	sampled.logf(&out, "warning %d", 1)
	sampled.logf(&out, "warning %d", 2)
	sampled.logf(&out, "warning %d", 3)
	if got := out.String(); got != "warning 1\n" {
		t.Fatalf("output=%q, want first warning only", got)
	}
	sampled.lastLog = time.Now().Add(-2 * time.Hour)
	sampled.logf(&out, "warning %d", 4)
	if got := out.String(); !strings.Contains(got, "warning 4 suppressed=2") {
		t.Fatalf("output=%q, want suppressed count", got)
	}
}

func TestWaitForStartupPingRetriesUntilContextCanceled(t *testing.T) {
	var requests atomic.Int32
	firstFailure := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "offline", http.StatusBadGateway)
		close(firstFailure)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-firstFailure
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	defer cancel()
	var out bytes.Buffer
	_, err := waitForStartupPing(ctx, srv.Client(), config{
		AdminURL: srv.URL,
		APIKey:   "lcenc_test",
		WorkDir:  t.TempDir(),
	}, &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context canceled", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d, want one failed startup ping", requests.Load())
	}
	if !strings.Contains(out.String(), "startup ping failed") {
		t.Fatalf("output=%q, want startup ping failure log", out.String())
	}
}

func TestRunCheckSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
		case "/api/encoder/ping":
			if got := r.Header.Get("Authorization"); got != "Bearer lcenc_test" {
				t.Fatalf("Authorization=%q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"encoderId":"enc_test","name":"test","status":"online"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := run(t.Context(), []string{"check"}, testEnv(map[string]string{
		envAdminURL: srv.URL,
		envAPIKey:   "lcenc_test",
	}), &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "healthz ok") || !strings.Contains(got, "encoder ping ok id=enc_test") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestLoadConfigRequiresEnv(t *testing.T) {
	_, err := loadConfig(testEnv(map[string]string{
		envAdminURL: "http://admin.local",
	}))
	if err == nil || !strings.Contains(err.Error(), envAPIKey) {
		t.Fatalf("err=%v, want missing api key", err)
	}
}

func TestRunDownloadOnceSuccess(t *testing.T) {
	var sawClaim, sawDownload, sawFail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/encoder/claim":
			sawClaim = true
			if got := r.Header.Get("Authorization"); got != "Bearer lcenc_test" {
				t.Fatalf("claim Authorization=%q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"packageId":"pkg_1","mediaId":"m1","mediaPath":"/srv/media/movie.mkv","renditionProfile":"h264-1080p-8mbps"}`))
		case "/api/encoder/media/m1":
			sawDownload = true
			if got := r.Header.Get("Authorization"); got != "Bearer lcenc_test" {
				t.Fatalf("download Authorization=%q", got)
			}
			_, _ = w.Write([]byte("media bytes"))
		case "/api/encoder/jobs/pkg_1/fail":
			sawFail = true
			if got := r.Header.Get("Authorization"); got != "Bearer lcenc_test" {
				t.Fatalf("fail Authorization=%q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"newStatus":"pending"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	workDir := t.TempDir()
	var out bytes.Buffer
	err := run(t.Context(), []string{"download-once"}, testEnv(map[string]string{
		envAdminURL: srv.URL,
		envAPIKey:   "lcenc_test",
		envWorkDir:  workDir,
	}), &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sawClaim || !sawDownload || !sawFail {
		t.Fatalf("sawClaim=%v sawDownload=%v sawFail=%v", sawClaim, sawDownload, sawFail)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "movie.mkv"))
	if err != nil {
		t.Fatalf("read downloaded media: %v", err)
	}
	if string(got) != "media bytes" {
		t.Fatalf("downloaded=%q", got)
	}
	if !strings.Contains(out.String(), "downloaded media=m1") {
		t.Fatalf("unexpected output: %q", out.String())
	}
	if !strings.Contains(out.String(), "released package=pkg_1") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunDownloadOnceNoClaimableMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/encoder/claim" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := run(t.Context(), []string{"download-once"}, testEnv(map[string]string{
		envAdminURL: srv.URL,
		envAPIKey:   "lcenc_test",
		envWorkDir:  t.TempDir(),
	}), &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "no claimable media") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunEncodeOnceSuccess(t *testing.T) {
	requireFFmpeg(t)
	source := filepath.Join(t.TempDir(), "source.mp4")
	generateTinyMedia(t, source)
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatal("default profile missing")
	}
	var sawComplete bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/encoder/claim":
			body, _ := json.Marshal(map[string]any{
				"packageId":        "pkg_1",
				"mediaId":          "m1",
				"mediaPath":        "/srv/media/source.mp4",
				"renditionProfile": profile.Name,
				"profile":          profile,
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case "/api/encoder/media/m1":
			_, _ = w.Write(sourceBytes)
		case "/api/encoder/jobs/pkg_1/complete":
			sawComplete = true
			if got := r.Header.Get("Authorization"); got != "Bearer lcenc_test" {
				t.Fatalf("complete Authorization=%q", got)
			}
			names := readTarNames(t, r.Body)
			for _, want := range []string{"init.mp4", "stream.m3u8"} {
				if !names[want] {
					t.Fatalf("complete tar missing %s: %+v", want, names)
				}
			}
			hasSeg := false
			for name := range names {
				if strings.HasPrefix(name, "seg") && strings.HasSuffix(name, ".m4s") {
					hasSeg = true
				}
			}
			if !hasSeg {
				t.Fatalf("complete tar missing segment: %+v", names)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"segmentCount":1,"durationMs":2000}`))
		case "/api/encoder/jobs/pkg_1/fail":
			t.Fatalf("unexpected fail request")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err = run(t.Context(), []string{"run-once"}, testEnv(map[string]string{
		envAdminURL: srv.URL,
		envAPIKey:   "lcenc_test",
		envWorkDir:  t.TempDir(),
	}), &out)
	if err != nil {
		t.Fatalf("run: %v\noutput:\n%s", err, out.String())
	}
	if !sawComplete {
		t.Fatal("complete was not called")
	}
	if !strings.Contains(out.String(), "completed package=pkg_1") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunEncodeOnceTerminalFailureForIncompatibleSource(t *testing.T) {
	requireFFmpeg(t)
	source := filepath.Join(t.TempDir(), "source.mp4")
	generateTinyMedia(t, source)
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatal("default profile missing")
	}
	profile.Video.Mode = packageprofile.VideoModeCopy
	profile.Video.CodecRequired = "hevc"

	var failBody struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/encoder/claim":
			body, _ := json.Marshal(map[string]any{
				"packageId":        "pkg_1",
				"mediaId":          "m1",
				"mediaPath":        "/srv/media/source.mp4",
				"renditionProfile": profile.Name,
				"profile":          profile,
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case "/api/encoder/media/m1":
			_, _ = w.Write(sourceBytes)
		case "/api/encoder/jobs/pkg_1/fail":
			if err := json.NewDecoder(r.Body).Decode(&failBody); err != nil {
				t.Fatalf("decode fail body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"newStatus":"failed"}`))
		case "/api/encoder/jobs/pkg_1/complete":
			t.Fatalf("unexpected complete request")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err = run(t.Context(), []string{"run-once"}, testEnv(map[string]string{
		envAdminURL: srv.URL,
		envAPIKey:   "lcenc_test",
		envWorkDir:  t.TempDir(),
	}), &out)
	if err == nil || !strings.Contains(err.Error(), "source video codec") {
		t.Fatalf("run err=%v, want source video codec failure\noutput:\n%s", err, out.String())
	}
	if failBody.Kind != "terminal" {
		t.Fatalf("fail kind=%q, want terminal; body=%+v", failBody.Kind, failBody)
	}
	if !strings.Contains(failBody.Reason, "source video codec") {
		t.Fatalf("fail reason=%q, want codec validation detail", failBody.Reason)
	}
}

func TestRunEncodeOnceTerminalFailureForMissingRemoteMedia(t *testing.T) {
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatal("default profile missing")
	}

	var failBody struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/encoder/claim":
			body, _ := json.Marshal(map[string]any{
				"packageId":        "pkg_1",
				"mediaId":          "m1",
				"mediaPath":        "/srv/media/missing.mp4",
				"renditionProfile": profile.Name,
				"profile":          profile,
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case "/api/encoder/media/m1":
			http.Error(w, "media file not found", http.StatusNotFound)
		case "/api/encoder/jobs/pkg_1/fail":
			if err := json.NewDecoder(r.Body).Decode(&failBody); err != nil {
				t.Fatalf("decode fail body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"newStatus":"failed"}`))
		case "/api/encoder/jobs/pkg_1/complete":
			t.Fatalf("unexpected complete request")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := run(t.Context(), []string{"run-once"}, testEnv(map[string]string{
		envAdminURL: srv.URL,
		envAPIKey:   "lcenc_test",
		envWorkDir:  t.TempDir(),
	}), &out)
	if err == nil || !strings.Contains(err.Error(), "download media returned 404") {
		t.Fatalf("run err=%v, want download 404 failure\noutput:\n%s", err, out.String())
	}
	if failBody.Kind != "terminal" {
		t.Fatalf("fail kind=%q, want terminal; body=%+v", failBody.Kind, failBody)
	}
	if !strings.Contains(failBody.Reason, "download media returned 404") {
		t.Fatalf("fail reason=%q, want download 404 detail", failBody.Reason)
	}
}

func TestRunEncodeOnceTransientFailureForCompleteUpload(t *testing.T) {
	requireFFmpeg(t)
	source := filepath.Join(t.TempDir(), "source.mp4")
	generateTinyMedia(t, source)
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatal("default profile missing")
	}

	var failBody struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/encoder/claim":
			body, _ := json.Marshal(map[string]any{
				"packageId":        "pkg_1",
				"mediaId":          "m1",
				"mediaPath":        "/srv/media/source.mp4",
				"renditionProfile": profile.Name,
				"profile":          profile,
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case "/api/encoder/media/m1":
			_, _ = w.Write(sourceBytes)
		case "/api/encoder/jobs/pkg_1/complete":
			_, _ = io.Copy(io.Discard, r.Body)
			http.Error(w, "temporary object store error", http.StatusBadGateway)
		case "/api/encoder/jobs/pkg_1/fail":
			if err := json.NewDecoder(r.Body).Decode(&failBody); err != nil {
				t.Fatalf("decode fail body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"newStatus":"pending"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err = run(t.Context(), []string{"run-once"}, testEnv(map[string]string{
		envAdminURL: srv.URL,
		envAPIKey:   "lcenc_test",
		envWorkDir:  t.TempDir(),
	}), &out)
	if err == nil || !strings.Contains(err.Error(), "complete returned 502") {
		t.Fatalf("run err=%v, want complete upload failure\noutput:\n%s", err, out.String())
	}
	if failBody.Kind != "transient" {
		t.Fatalf("fail kind=%q, want transient; body=%+v", failBody.Kind, failBody)
	}
}

// TestRunEncodeLoopParallel verifies that the loop maintains multiple in-flight
// jobs when the server's concurrency is > 1. It does this by blocking job 1's
// source download until job 2 is claimed: if the loop were serial, job 2 would
// never be claimed while job 1's download is stuck, causing a deadlock/timeout.
func TestRunEncodeLoopParallel(t *testing.T) {
	requireFFmpeg(t)

	source := filepath.Join(t.TempDir(), "source.mp4")
	generateTinyMedia(t, source)
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatal("default profile missing")
	}

	// Closed when the coordinator claims job 2. Job 1's download blocks on this.
	job2Claimed := make(chan struct{})

	var claimIdx atomic.Int32
	var completeCount atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	jobs := []map[string]any{
		{"packageId": "pkg_1", "mediaId": "m1", "mediaPath": "/srv/media/source.mp4",
			"renditionProfile": profile.Name, "profile": profile},
		{"packageId": "pkg_2", "mediaId": "m2", "mediaPath": "/srv/media/source.mp4",
			"renditionProfile": profile.Name, "profile": profile},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/encoder/ping":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"encoderId":"enc_test","name":"test","status":"online","concurrency":2}`))

		case r.URL.Path == "/api/encoder/claim":
			idx := int(claimIdx.Add(1)) - 1
			if idx < len(jobs) {
				if idx == 1 {
					close(job2Claimed)
				}
				body, _ := json.Marshal(jobs[idx])
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}

		case r.URL.Path == "/api/encoder/media/m1":
			// Block until job 2 is claimed. A serial loop would deadlock here.
			select {
			case <-job2Claimed:
			case <-ctx.Done():
				http.Error(w, "context canceled", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(sourceBytes)

		case r.URL.Path == "/api/encoder/media/m2":
			_, _ = w.Write(sourceBytes)

		case strings.HasSuffix(r.URL.Path, "/complete"):
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"segmentCount":1,"durationMs":2000}`))
			if completeCount.Add(1) == 2 {
				cancel()
			}

		case strings.HasSuffix(r.URL.Path, "/fail"):
			t.Errorf("unexpected fail: %s", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))

		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err = runEncodeLoop(ctx, &http.Client{}, config{
		AdminURL: srv.URL,
		APIKey:   "lcenc_test",
		WorkDir:  t.TempDir(),
	}, &out)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runEncodeLoop: %v\noutput:\n%s", err, out.String())
	}
	if n := completeCount.Load(); n != 2 {
		t.Fatalf("expected 2 completes, got %d\noutput:\n%s", n, out.String())
	}
}

func TestInstallUninstallNonWindows(t *testing.T) {
	var out bytes.Buffer
	err := run(t.Context(), []string{"install"}, testEnv(map[string]string{
		envAdminURL: "http://admin.local",
		envAPIKey:   "lcenc_test",
	}), &out)
	if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
		t.Fatalf("install on non-Windows: err=%v", err)
	}

	out.Reset()
	err = run(t.Context(), []string{"uninstall"}, testEnv(map[string]string{
		envAdminURL: "http://admin.local",
		envAPIKey:   "lcenc_test",
	}), &out)
	if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
		t.Fatalf("uninstall on non-Windows: err=%v", err)
	}
}

func testEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
}

func generateTinyMedia(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=128x72:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate media: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func readTarNames(t *testing.T, r io.Reader) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read complete tar: %v", err)
		}
		out[hdr.Name] = true
	}
	return out
}
