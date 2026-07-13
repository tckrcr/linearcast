package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tckrcr/linearcast/internal/mediasource"
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
	status, err := client.Status(context.Background())
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
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("Status succeeded, want error")
	}
}

func TestStatusConnectionErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	client := New(url, "secret-token")
	_, err := client.Status(context.Background())
	if err == nil {
		t.Fatal("Status succeeded, want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "secret-token") || strings.Contains(msg, "X-Plex-Token") {
		t.Fatalf("error leaked token: %s", msg)
	}
}

func TestItemsMapsMetadataFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("X-Plex-Token"); got != "tok" {
			t.Fatalf("token=%q, want tok", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/sections":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"2","title":"TV","type":"show"}]}}`))
		case "/library/sections/2/all":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[{
				"ratingKey":"123",
				"title":"A Quiet Offer",
				"type":"episode",
				"grandparentTitle":"Harbor Lights",
				"parentIndex":2,
				"index":4,
				"year":2005,
				"summary":"The lead weighs an offer.",
				"thumb":"/library/metadata/123/thumb/456",
				"contentRating":"TV-MA",
				"Genre":[{"tag":"Comedy"},{"tag":"Drama"}],
				"Media":[{"videoResolution":"1080","Part":[{"file":"/plex/Harbor Lights/S02E04.mkv"}]}]
			}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	items, err := New(srv.URL, "tok").Items(context.Background(), "2", nilScanOptions())
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%+v, want one", items)
	}
	got := items[0]
	if got.SourceRef != "plex://123" || got.Title != "A Quiet Offer" || got.SeriesName != "Harbor Lights" {
		t.Fatalf("basic metadata=%+v", got)
	}
	if got.Description != "The lead weighs an offer." || got.ThumbnailPath != "/library/metadata/123/thumb/456" || got.ContentRating != "TV-MA" {
		t.Fatalf("rich metadata=%+v", got)
	}
	if strings.Join(got.Genres, ",") != "Comedy,Drama" {
		t.Fatalf("genres=%+v", got.Genres)
	}
}

func nilScanOptions() mediasource.ScanOptions {
	return mediasource.ScanOptions{}
}

func TestPathMapperLongestPrefixWins(t *testing.T) {
	mapper, err := ParsePathMap("/plex=/data/media,/plex/tv=/srv/media/tv")
	if err != nil {
		t.Fatalf("ParsePathMap: %v", err)
	}
	got := mapper.Map("/plex/tv/show/episode.mkv")
	if got != "/srv/media/tv/show/episode.mkv" {
		t.Fatalf("mapped path=%q, want /srv/media/tv/show/episode.mkv", got)
	}
}

func TestPathMapperPassthroughAndExactMatch(t *testing.T) {
	mapper, err := ParsePathMap("/plex/tv=/srv/media/tv")
	if err != nil {
		t.Fatalf("ParsePathMap: %v", err)
	}
	if got := mapper.Map("/plex/tv"); got != "/srv/media/tv" {
		t.Fatalf("exact mapped path=%q, want /srv/media/tv", got)
	}
	if got := mapper.Map("/other/file.mkv"); got != "/other/file.mkv" {
		t.Fatalf("passthrough path=%q, want unchanged", got)
	}
}
