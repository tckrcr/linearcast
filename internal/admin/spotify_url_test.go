package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

// liveUpstreamServer serves a 200 HLS manifest for any *.m3u8 path (so the
// heartbeat reads "live") plus a now-playing payload.
func liveUpstreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/now-playing":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Song","playing":true}`))
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getSpotifyUrl(t *testing.T, app *App) spotifyURLResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/spotify-url", nil)
	rec := httptest.NewRecorder()
	app.handleSpotifyUrlGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET spotify-url: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp spotifyURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode spotify-url: %v", err)
	}
	return resp
}

func setSpotifyUrl(t *testing.T, app *App, url string) spotifyURLResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/spotify-url",
		strings.NewReader(`{"upstreamHlsUrl":"`+url+`"}`))
	rec := httptest.NewRecorder()
	app.handleSpotifyUrlSet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT spotify-url: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp spotifyURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode spotify-url: %v", err)
	}
	return resp
}

func externalChannelCount(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.dbConn.QueryRow(`SELECT COUNT(*) FROM channels WHERE upstream_hls_url IS NOT NULL`).Scan(&n); err != nil {
		t.Fatalf("count external: %v", err)
	}
	return n
}

func TestSpotifyUrlSingletonUpsertGetClear(t *testing.T) {
	app, _ := testAdminApp(t)
	upstream := liveUpstreamServer(t)

	// Nothing configured yet.
	if got := getSpotifyUrl(t, app); got.Configured {
		t.Fatalf("want unconfigured, got %+v", got)
	}

	// PUT creates the singleton.
	url1 := upstream.URL + "/a/stream.m3u8"
	created := setSpotifyUrl(t, app, url1)
	if !created.Configured || created.UpstreamHLSURL != url1 || created.Status != externalStatusLive {
		t.Fatalf("create response unexpected: %+v", created)
	}
	if created.ChannelID == "" {
		t.Fatalf("create did not assign a channel id: %+v", created)
	}
	if n := externalChannelCount(t, app); n != 1 {
		t.Fatalf("external channel count=%d after create, want 1", n)
	}

	// PUT again updates the URL in place — it must NOT create a second channel.
	url2 := upstream.URL + "/b/stream.m3u8"
	updated := setSpotifyUrl(t, app, url2)
	if updated.ChannelID != created.ChannelID || updated.UpstreamHLSURL != url2 {
		t.Fatalf("update did not upsert in place: created=%+v updated=%+v", created, updated)
	}
	if n := externalChannelCount(t, app); n != 1 {
		t.Fatalf("external channel count=%d after update, want 1 (singleton)", n)
	}

	// GET reflects the updated URL.
	if got := getSpotifyUrl(t, app); !got.Configured || got.UpstreamHLSURL != url2 {
		t.Fatalf("GET after update unexpected: %+v", got)
	}

	// DELETE clears it.
	req := httptest.NewRequest(http.MethodDelete, "/api/spotify-url", nil)
	rec := httptest.NewRecorder()
	app.handleSpotifyUrlClear(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE spotify-url: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := externalChannelCount(t, app); n != 0 {
		t.Fatalf("external channel count=%d after clear, want 0", n)
	}
	if got := getSpotifyUrl(t, app); got.Configured {
		t.Fatalf("want unconfigured after clear, got %+v", got)
	}

	// DELETE again is idempotent.
	rec = httptest.NewRecorder()
	app.handleSpotifyUrlClear(rec, httptest.NewRequest(http.MethodDelete, "/api/spotify-url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent DELETE: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSpotifyUrlSetRejectsBadURL(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodPut, "/api/spotify-url",
		strings.NewReader(`{"upstreamHlsUrl":"ftp://example.com/x.m3u8"}`))
	rec := httptest.NewRecorder()
	app.handleSpotifyUrlSet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for bad scheme, got %d", rec.Code)
	}
	// And nothing should have been created.
	if _, err := db.ExternalChannel(context.Background(), app.dbConn); err != nil {
		t.Fatalf("external lookup: %v", err)
	}
	if n := externalChannelCount(t, app); n != 0 {
		t.Fatalf("external channel count=%d, want 0 after rejected URL", n)
	}
}
