package plex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusUsesAuthenticatedRootEndpoint(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("X-Plex-Token")
		if r.URL.Path != "/" {
			t.Fatalf("path=%q, want /", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"friendlyName":"Living Room","myPlexUsername":"admin"}}`))
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "tok")
	status, err := client.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if gotToken != "tok" {
		t.Fatalf("token query=%q, want tok", gotToken)
	}
	if status.ServerName != "Living Room" || status.Username != "admin" {
		t.Fatalf("status=%+v", status)
	}
}

func TestStatusReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "bad")
	if _, err := client.Status(); err == nil {
		t.Fatal("Status succeeded, want error")
	}
}

func TestStatusConnectionErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	client := New(url, "secret-token")
	_, err := client.Status()
	if err == nil {
		t.Fatal("Status succeeded, want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "secret-token") || strings.Contains(msg, "X-Plex-Token") {
		t.Fatalf("error leaked token: %s", msg)
	}
}

func TestPathMapperLongestPrefixWins(t *testing.T) {
	mapper, err := ParsePathMap("/plex=/data/media,/plex/tv=/srv/tv")
	if err != nil {
		t.Fatalf("ParsePathMap: %v", err)
	}
	got := mapper.Map("/plex/tv/show/episode.mkv")
	if got != "/srv/tv/show/episode.mkv" {
		t.Fatalf("mapped path=%q, want /srv/tv/show/episode.mkv", got)
	}
}

func TestPathMapperPassthroughAndExactMatch(t *testing.T) {
	mapper, err := ParsePathMap("/plex/tv=/srv/tv")
	if err != nil {
		t.Fatalf("ParsePathMap: %v", err)
	}
	if got := mapper.Map("/plex/tv"); got != "/srv/tv" {
		t.Fatalf("exact mapped path=%q, want /srv/tv", got)
	}
	if got := mapper.Map("/other/file.mkv"); got != "/other/file.mkv" {
		t.Fatalf("passthrough path=%q, want unchanged", got)
	}
}
