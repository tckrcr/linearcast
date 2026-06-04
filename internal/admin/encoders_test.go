package admin

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
)

// encoderHandlerEnv mirrors testAdminApp but pre-seeds an authenticated
// admin session, plus a media row and channel so the /claim handler has
// real candidates to walk. Auth is disabled (no password) so the cookie
// middleware short-circuits — encoder routes still go through bearer auth.
type encoderHandlerEnv struct {
	app  *App
	conn *sql.DB
}

func newEncoderHandlerEnv(t *testing.T) *encoderHandlerEnv {
	t.Helper()
	app, conn := testAdminApp(t)
	mediaPath := filepath.Join(t.TempDir(), "m1.mkv")
	if err := os.WriteFile(mediaPath, []byte("fake media"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', ?, ?, 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`, mediaPath, filepath.Dir(mediaPath)); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms, playback_mode, required_package_profile)
		VALUES ('ch1', 'Test', '/tmp', 'linear', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}
	return &encoderHandlerEnv{app: app, conn: conn}
}

func (e *encoderHandlerEnv) registerEncoder(t *testing.T, name string) (id, rawKey string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/encoders", strings.NewReader(`{"name":"`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	e.app.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp encoderRegisterResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.ID, resp.APIKey
}

func (e *encoderHandlerEnv) authedPost(t *testing.T, path, apiKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rr := httptest.NewRecorder()
	e.app.Handler().ServeHTTP(rr, req)
	return rr
}

func (e *encoderHandlerEnv) authedGet(t *testing.T, path, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rr := httptest.NewRecorder()
	e.app.Handler().ServeHTTP(rr, req)
	return rr
}

func (e *encoderHandlerEnv) claim(t *testing.T, key string) encoderClaimResponse {
	t.Helper()
	claimRR := e.authedPost(t, "/api/encoder/claim", key, `{}`)
	if claimRR.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claimRR.Code, claimRR.Body.String())
	}
	var claim encoderClaimResponse
	if err := json.NewDecoder(claimRR.Body).Decode(&claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	return claim
}

func TestEncoderAdmin_RegisterReturnsRawKeyOnce(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	id, key := env.registerEncoder(t, "test")
	if !strings.HasPrefix(id, "enc_") {
		t.Fatalf("id=%q, want enc_ prefix", id)
	}
	if !strings.HasPrefix(key, "lcenc_") {
		t.Fatalf("key=%q, want lcenc_ prefix", key)
	}
	// List should include the encoder but never the raw key.
	listReq := httptest.NewRequest(http.MethodGet, "/api/admin/encoders", nil)
	listRR := httptest.NewRecorder()
	env.app.Handler().ServeHTTP(listRR, listReq)
	body := listRR.Body.String()
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRR.Code, body)
	}
	if strings.Contains(body, key) {
		t.Fatalf("list response leaked raw key: %s", body)
	}
	var resp encoderListResponse
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(resp.Encoders) != 1 || resp.Encoders[0].ID != id {
		t.Fatalf("list mismatch: %+v", resp)
	}
}

func TestEncoderAdmin_RevokeMarksRevoked(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	id, _ := env.registerEncoder(t, "test")
	rr := httptest.NewRecorder()
	env.app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/encoders/"+id+"/revoke", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", rr.Code, rr.Body.String())
	}
	// List should now show revokedAtMs set.
	listRR := httptest.NewRecorder()
	env.app.Handler().ServeHTTP(listRR, httptest.NewRequest(http.MethodGet, "/api/admin/encoders", nil))
	var resp encoderListResponse
	if err := json.NewDecoder(listRR.Body).Decode(&resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(resp.Encoders) != 1 || resp.Encoders[0].RevokedAtMs == nil {
		t.Fatalf("encoder not marked revoked: %+v", resp.Encoders)
	}
}

func TestEncoderAdmin_RevokeUnknownReturns404(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	rr := httptest.NewRecorder()
	env.app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/encoders/enc_nonexistent/revoke", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderBearerAuth_MissingTokenRejected(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	rr := env.authedPost(t, "/api/encoder/claim", "", `{}`)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderBearerAuth_UnknownTokenRejected(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	rr := env.authedPost(t, "/api/encoder/claim", "lcenc_deadbeef", `{}`)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderBearerAuth_RevokedTokenForbidden(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	id, key := env.registerEncoder(t, "test")
	if err := db.RevokeEncoder(context.Background(), env.conn, id, env.app.now().UTC().UnixMilli()); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	rr := env.authedPost(t, "/api/encoder/claim", key, `{}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderPing_ValidBearerReturnsEncoderIdentity(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	id, key := env.registerEncoder(t, "test")

	rr := env.authedGet(t, "/api/encoder/ping", key)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	var resp encoderPingResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || resp.EncoderID != id || resp.Name != "test" {
		t.Fatalf("ping response mismatch: %+v", resp)
	}
}

func TestEncoderPing_PostUpdatesCapabilities(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")

	req := httptest.NewRequest(http.MethodPost, "/api/encoder/ping", strings.NewReader(`{
		"hostname":"gpu-box",
		"os":"windows",
		"arch":"amd64",
		"encoders":["h264_nvenc"],
		"nvidiaGpus":[{"name":"RTX 4090","driverVersion":"555.1"}]
	}`))
	req.RemoteAddr = "192.168.1.50:50000"
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	env.app.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 body=%s", rr.Code, rr.Body.String())
	}

	listRR := httptest.NewRecorder()
	env.app.Handler().ServeHTTP(listRR, httptest.NewRequest(http.MethodGet, "/api/admin/encoders", nil))
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRR.Code, listRR.Body.String())
	}
	var list encoderListResponse
	if err := json.NewDecoder(listRR.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	raw := string(list.Encoders[0].Capabilities)
	for _, want := range []string{"gpu-box", "windows", "h264_nvenc", "RTX 4090", "192.168.1.50"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("capabilities missing %q: %s", want, raw)
		}
	}
}

func TestEncoderClaim_HappyPathReturnsJobAndCreatesLease(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")
	rr := env.authedPost(t, "/api/encoder/claim", key, `{"leaseTtlSeconds":30}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	var resp encoderClaimResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.MediaID != "m1" || resp.RenditionProfile != "h264-main-1080p" {
		t.Fatalf("claim mismatch: %+v", resp)
	}
	if resp.Profile.Name == "" {
		t.Fatalf("claim response missing resolved profile config")
	}
	// Lease row must exist.
	var n int
	if err := env.conn.QueryRow(`SELECT COUNT(*) FROM encoder_jobs WHERE package_id = ?`, resp.PackageID).Scan(&n); err != nil {
		t.Fatalf("count lease: %v", err)
	}
	if n != 1 {
		t.Fatalf("lease count=%d, want 1", n)
	}
}

func TestEncoderClaim_NoCandidatesReturns204(t *testing.T) {
	app, _ := testAdminApp(t)
	_, key := registerEncoderHelper(t, app, "test")
	req := httptest.NewRequest(http.MethodPost, "/api/encoder/claim", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 body=%s", rr.Code, rr.Body.String())
	}
}

// registerEncoderHelper for cases without an env scaffold (no media/channel).
func registerEncoderHelper(t *testing.T, app *App, name string) (string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/encoders", strings.NewReader(`{"name":"`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp encoderRegisterResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	return resp.ID, resp.APIKey
}

func TestEncoderClaim_LocalOnlyPolicyBlocksAndReturns204(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	if _, err := env.conn.Exec(`UPDATE channels SET encoder_policy = 'local_only' WHERE id = 'ch1'`); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	_, key := env.registerEncoder(t, "test")
	rr := env.authedPost(t, "/api/encoder/claim", key, `{}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (policy rejected) body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderMediaDownload_RequiresActiveLease(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")

	rr := env.authedGet(t, "/api/encoder/media/m1", key)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderMediaDownload_ServesClaimedMedia(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")
	claim := env.claim(t, key)

	rr := env.authedGet(t, "/api/encoder/media/"+claim.MediaID, key)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "fake media" {
		t.Fatalf("body=%q, want media contents", rr.Body.String())
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, "m1.mkv") {
		t.Fatalf("Content-Disposition=%q", got)
	}
}

func TestEncoderMediaDownload_WrongEncoderRejected(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, keyA := env.registerEncoder(t, "alpha")
	_, keyB := env.registerEncoder(t, "beta")
	claim := env.claim(t, keyA)

	rr := env.authedGet(t, "/api/encoder/media/"+claim.MediaID, keyB)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderComplete_FinalizesUploadedPackage(t *testing.T) {
	requireFFmpeg(t)
	app, conn := testAdminApp(t)
	app.packageRoot = filepath.Join(t.TempDir(), "packages")
	mediaPath := filepath.Join(t.TempDir(), "source.mp4")
	generateTinyMedia(t, mediaPath)
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', ?, ?, 2000, 'mp4', 'h264', 72, 'aac', 1, 0)`, mediaPath, filepath.Dir(mediaPath)); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms, playback_mode, required_package_profile)
		VALUES ('ch1', 'Test', '/tmp', 'linear', 1, 0, 'packaged', 'h264-main-1080p')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}
	_, key := registerEncoderHelper(t, app, "test")
	req := httptest.NewRequest(http.MethodPost, "/api/encoder/claim", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	claimRR := httptest.NewRecorder()
	app.Handler().ServeHTTP(claimRR, req)
	if claimRR.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claimRR.Code, claimRR.Body.String())
	}
	var claim encoderClaimResponse
	if err := json.NewDecoder(claimRR.Body).Decode(&claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	encodedDir := filepath.Join(t.TempDir(), "encoded")
	if err := packager.EncodePackageOutput(t.Context(), mediaPath, encodedDir, 6000, "veryfast", claim.Profile); err != nil {
		t.Fatalf("encode package output: %v", err)
	}
	var body bytes.Buffer
	writeTestPackageTar(t, &body, encodedDir)
	completeReq := httptest.NewRequest(http.MethodPost, "/api/encoder/jobs/"+claim.PackageID+"/complete", &body)
	completeReq.Header.Set("Authorization", "Bearer "+key)
	completeReq.Header.Set("Content-Type", "application/x-tar")
	completeRR := httptest.NewRecorder()
	app.Handler().ServeHTTP(completeRR, completeReq)
	if completeRR.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completeRR.Code, completeRR.Body.String())
	}
	var complete encoderCompleteResponse
	if err := json.NewDecoder(completeRR.Body).Decode(&complete); err != nil {
		t.Fatalf("decode complete: %v", err)
	}
	if !complete.OK || complete.SegmentCount == 0 || complete.DurationMs == 0 {
		t.Fatalf("complete response mismatch: %+v", complete)
	}
	pkg, err := db.MediaPackageByID(context.Background(), conn, claim.PackageID)
	if err != nil {
		t.Fatalf("lookup package: %v", err)
	}
	if pkg == nil || pkg.Status != db.PackageStatusReady {
		t.Fatalf("package not ready: %+v", pkg)
	}
	var jobs int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM encoder_jobs WHERE package_id = ?`, claim.PackageID).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 0 {
		t.Fatalf("encoder job count=%d, want 0", jobs)
	}
}

func TestEncoderHeartbeat_ExtendsLease(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")
	// Claim first.
	claimRR := env.authedPost(t, "/api/encoder/claim", key, `{"leaseTtlSeconds":10}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)

	hbRR := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/heartbeat", key, `{"leaseTtlSeconds":60,"progressPct":50}`)
	if hbRR.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", hbRR.Code, hbRR.Body.String())
	}
	var hb encoderHeartbeatResponse
	_ = json.NewDecoder(hbRR.Body).Decode(&hb)
	if hb.LeaseExpiresMs <= claim.LeaseExpiresMs {
		t.Fatalf("lease did not extend: claim=%d hb=%d", claim.LeaseExpiresMs, hb.LeaseExpiresMs)
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

func writeTestPackageTar(t *testing.T, w io.Writer, dir string) {
	t.Helper()
	tw := tar.NewWriter(w)
	for _, name := range []string{"init.mp4", "stream.m3u8"} {
		addTestTarFile(t, tw, filepath.Join(dir, name), name)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "seg*.m4s"))
	if err != nil {
		t.Fatalf("glob segments: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no encoded segments")
	}
	for _, p := range matches {
		addTestTarFile(t, tw, p, filepath.Base(p))
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
}

func addTestTarFile(t *testing.T, tw *tar.Writer, filePath, name string) {
	t.Helper()
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("open %s: %v", filePath, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", filePath, err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: info.Size()}); err != nil {
		t.Fatalf("write header %s: %v", name, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		t.Fatalf("write tar %s: %v", name, err)
	}
}

func TestEncoderHeartbeat_InvalidProgressRejected(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")
	claimRR := env.authedPost(t, "/api/encoder/claim", key, `{}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)

	rr := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/heartbeat", key, `{"progressPct":101}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderHeartbeat_WrongEncoderReturns409(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, keyA := env.registerEncoder(t, "alpha")
	_, keyB := env.registerEncoder(t, "beta")
	claimRR := env.authedPost(t, "/api/encoder/claim", keyA, `{}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)
	rr := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/heartbeat", keyB, `{}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderFail_TransientGoesToPending(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")
	claimRR := env.authedPost(t, "/api/encoder/claim", key, `{}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)

	failRR := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/fail", key,
		`{"kind":"transient","reason":"ffmpeg crashed"}`)
	if failRR.Code != http.StatusOK {
		t.Fatalf("fail status=%d body=%s", failRR.Code, failRR.Body.String())
	}
	var resp encoderFailResponse
	_ = json.NewDecoder(failRR.Body).Decode(&resp)
	if resp.NewStatus != string(db.PackageStatusPending) {
		t.Fatalf("status=%s, want pending", resp.NewStatus)
	}
}

func TestEncoderFail_TerminalGoesToFailed(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")
	claimRR := env.authedPost(t, "/api/encoder/claim", key, `{}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)

	failRR := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/fail", key,
		`{"kind":"terminal","reason":"source missing"}`)
	if failRR.Code != http.StatusOK {
		t.Fatalf("fail status=%d body=%s", failRR.Code, failRR.Body.String())
	}
	var resp encoderFailResponse
	_ = json.NewDecoder(failRR.Body).Decode(&resp)
	if resp.NewStatus != string(db.PackageStatusFailed) {
		t.Fatalf("status=%s, want failed", resp.NewStatus)
	}
}

func TestEncoderFail_WrongEncoderReturns409(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, keyA := env.registerEncoder(t, "alpha")
	_, keyB := env.registerEncoder(t, "beta")
	claimRR := env.authedPost(t, "/api/encoder/claim", keyA, `{}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)
	rr := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/fail", keyB,
		`{"kind":"transient","reason":"sneaky"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 body=%s", rr.Code, rr.Body.String())
	}
}

func TestEncoderFail_InvalidKindRejected(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	_, key := env.registerEncoder(t, "test")
	claimRR := env.authedPost(t, "/api/encoder/claim", key, `{}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)
	rr := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/fail", key,
		`{"kind":"meh","reason":"oops"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 body=%s", rr.Code, rr.Body.String())
	}
}

// TestEncoderClaim_RevokedEncoderRejectedBetweenClaims tests the race where
// an encoder claims, then gets revoked, then tries to heartbeat. The
// heartbeat must fail with 403 even though the lease still exists.
func TestEncoderClaim_RevokedAfterClaimBlocksHeartbeat(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	id, key := env.registerEncoder(t, "test")
	claimRR := env.authedPost(t, "/api/encoder/claim", key, `{}`)
	var claim encoderClaimResponse
	_ = json.NewDecoder(claimRR.Body).Decode(&claim)
	// Revoke after the claim succeeded.
	if err := db.RevokeEncoder(context.Background(), env.conn, id, env.app.now().UTC().UnixMilli()); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	rr := env.authedPost(t, "/api/encoder/jobs/"+claim.PackageID+"/heartbeat", key, `{}`)
	// The bearer-auth middleware itself rejects revoked keys with 403, before
	// the handler runs — so this is a middleware-level test as much as a
	// handler-level one.
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 body=%s", rr.Code, rr.Body.String())
	}
}

// Sanity that encoder routes use bearer auth, not cookie auth, even when the
// admin server has password auth and CSRF checks enabled.
func TestEncoderRoutes_BearerAuthEvenWithAdminPasswordEnabled(t *testing.T) {
	env := newEncoderHandlerEnv(t)
	// Register the encoder via the DB layer directly so this test doesn't
	// depend on the admin CRUD route being accessible. We want to isolate
	// the bearer-auth behavior of /api/encoder/*.
	_, key, err := db.RegisterEncoder(context.Background(), env.conn, "test", "{}", env.app.now().UTC().UnixMilli())
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	env.app.auth = newAuthService("secret", env.app.now)

	// Missing bearer → 401.
	rr := env.authedPost(t, "/api/encoder/claim", "", `{}`)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 body=%s", rr.Code, rr.Body.String())
	}
	// Valid bearer works even though no cookie is present, and an off-origin
	// request is not blocked by cookie CSRF checks because encoder clients do
	// not use cookies.
	req := httptest.NewRequest(http.MethodPost, "http://admin.local/api/encoder/claim", strings.NewReader(`{}`))
	req.Host = "admin.local"
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://encoder.local")
	rr2 := httptest.NewRecorder()
	env.app.Handler().ServeHTTP(rr2, req)
	if rr2.Code != http.StatusOK && rr2.Code != http.StatusNoContent {
		t.Fatalf("encoder route refused valid bearer when admin password enabled: status=%d body=%s",
			rr2.Code, rr2.Body.String())
	}
}
