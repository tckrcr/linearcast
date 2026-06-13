package plexrelay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateSessionStoresAndRewritesPlexHLSManifest(t *testing.T) {
	var sessionID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/video/:/transcode/universal/start":
			sessionID = r.URL.Query().Get("session")
			if sessionID == "" {
				t.Error("start request missing session query")
			}
			if got := r.URL.Query().Get("path"); got != "/library/metadata/101" {
				t.Errorf("path=%q, want /library/metadata/101", got)
			}
			if got := r.URL.Query().Get("offsetMilliseconds"); got != "12000" {
				t.Errorf("offsetMilliseconds=%q, want 12000", got)
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=8000000\nsession/%s/base/index.m3u8?X-Plex-Token=leaked\n", sessionID)
		case strings.HasSuffix(r.URL.Path, "/base/index.m3u8"):
			if got := r.URL.Query().Get("X-Plex-Token"); got != "secret-token" {
				t.Errorf("manifest token=%q, want secret-token", got)
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4?X-Plex-Token=leaked\"\n00000.ts\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr := NewManager(srv.URL, "secret-token", srv.Client())
	sess, err := mgr.CreateSession(context.Background(), "/library/metadata/101", 12000)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.SessionID == "" || sess.SessionID != sessionID {
		t.Fatalf("session id=%q, start session=%q", sess.SessionID, sessionID)
	}

	master := string(sess.MasterManifest("relay"))
	wantVariant := fmt.Sprintf("/channel/relay/plexrelay/%s/session/%s/base/index.m3u8", sess.ViewerToken, sess.SessionID)
	if !strings.Contains(master, wantVariant) {
		t.Fatalf("master manifest did not rewrite variant path:\n%s\nwant %s", master, wantVariant)
	}
	if strings.Contains(master, "leaked") {
		t.Fatalf("master manifest leaked Plex token query:\n%s", master)
	}

	rendition, err := sess.FetchManifest(context.Background(), srv.Client(), "session/"+sess.SessionID+"/base/index.m3u8", "relay")
	if err != nil {
		t.Fatalf("fetch rendition: %v", err)
	}
	got := string(rendition)
	for _, want := range []string{
		fmt.Sprintf("/channel/relay/plexrelay/%s/session/%s/base/init.mp4", sess.ViewerToken, sess.SessionID),
		fmt.Sprintf("/channel/relay/plexrelay/%s/session/%s/base/00000.ts", sess.ViewerToken, sess.SessionID),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendition manifest did not contain %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "leaked") {
		t.Fatalf("rendition manifest leaked Plex token query:\n%s", got)
	}
}
