package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleChannelProbeUpstreamReachable(t *testing.T) {
	app, _ := testAdminApp(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"))
	}))
	defer upstream.Close()

	resp := runProbe(t, app, upstream.URL+"/stream.m3u8")
	if !resp.Reachable {
		t.Fatalf("want reachable, got %+v", resp)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("want status 200, got %+v", resp)
	}
	if !resp.LooksLikeHLS {
		t.Fatalf("want looksLikeHls, got %+v", resp)
	}
}

func TestHandleChannelProbeUpstreamDetectsHLSByBody(t *testing.T) {
	app, _ := testAdminApp(t)
	// Generic content type, but the body is an HLS playlist.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer upstream.Close()

	resp := runProbe(t, app, upstream.URL+"/stream.m3u8")
	if !resp.Reachable || !resp.LooksLikeHLS {
		t.Fatalf("want reachable HLS, got %+v", resp)
	}
}

func TestHandleChannelProbeUpstreamUnreachable(t *testing.T) {
	app, _ := testAdminApp(t)
	// Start then immediately close so the address refuses connections.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := upstream.URL
	upstream.Close()

	resp := runProbe(t, app, addr+"/stream.m3u8")
	if resp.Reachable {
		t.Fatalf("want unreachable, got %+v", resp)
	}
	if resp.Error == "" {
		t.Fatalf("want an error message, got %+v", resp)
	}
}

func TestHandleChannelProbeUpstreamAllowsPrivateAddresses(t *testing.T) {
	// The Spotify URL is an operator-set, private-by-nature service (e.g. a LAN
	// Spotify→HLS bridge), so the probe must reach loopback/LAN rather than refuse
	// it. httptest serves on loopback; a deny-private policy would block it.
	app, _ := testAdminApp(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer upstream.Close()

	resp := runProbe(t, app, upstream.URL+"/stream.m3u8")
	if !resp.Reachable {
		t.Fatalf("want loopback upstream reachable, got %+v", resp)
	}
}

func TestHandleChannelProbeUpstreamRejectsBadScheme(t *testing.T) {
	app, _ := testAdminApp(t)
	body := strings.NewReader(`{"upstreamHlsUrl": "ftp://example.com/x.m3u8"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/probe-upstream", body)
	rec := httptest.NewRecorder()
	app.handleChannelProbeUpstream(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func runProbe(t *testing.T, app *App, url string) probeUpstreamResponse {
	t.Helper()
	body := strings.NewReader(`{"upstreamHlsUrl": "` + url + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/channels/probe-upstream", body)
	rec := httptest.NewRecorder()
	app.handleChannelProbeUpstream(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 advisory response, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp probeUpstreamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode probe response: %v", err)
	}
	return resp
}
