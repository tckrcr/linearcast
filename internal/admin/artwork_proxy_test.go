package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
)

func TestHandleMediaArtworkProxiesPlexThumbnail(t *testing.T) {
	app, conn := testAdminApp(t)
	var gotToken string
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("X-Plex-Token")
		if r.URL.Path != "/library/metadata/123/thumb/456" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-bytes"))
	}))
	t.Cleanup(plex.Close)
	if err := db.SetPlexURL(context.Background(), conn, plex.URL); err != nil {
		t.Fatalf("set plex url: %v", err)
	}
	if err := db.SetPlexToken(context.Background(), conn, "secret-token"); err != nil {
		t.Fatalf("set plex token: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms,
		source_ref, thumb_path)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 18000, 'mkv', 'h264', 1080, 'aac', 1, 0,
		        'plex://123', '/library/metadata/123/thumb/456')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/art/media/m1", nil)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if gotToken != "secret-token" {
		t.Fatalf("plex token=%q", gotToken)
	}
	if res.Body.String() != "jpeg-bytes" {
		t.Fatalf("body=%q", res.Body.String())
	}
	if res.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("content-type=%q", res.Header().Get("Content-Type"))
	}
	if res.Header().Get("Cache-Control") == "" {
		t.Fatal("missing cache-control")
	}
}

func TestPlexArtworkURLRequiresRelativePlexPath(t *testing.T) {
	if _, err := plexArtworkURL("http://plex.test", "http://evil.test/x", "tok"); err == nil {
		t.Fatal("plexArtworkURL accepted absolute thumbnail URL")
	}
}
