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

func TestAdminJellyfinStatusDisconnectedIncludesURL(t *testing.T) {
	app, _ := testAdminApp(t)
	app.jellyfinURL = "http://jellyfin.example"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/jellyfin/status", nil)

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body mediaServerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Connected || body.Configured || body.URL != "http://jellyfin.example" {
		t.Fatalf("body=%+v", body)
	}
}

func TestAdminJellyfinConfigSetSuccessDoesNotReturnAPIKey(t *testing.T) {
	app, conn := testAdminApp(t)
	srv := testJellyfinServer(t, "good")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/admin/jellyfin/config", strings.NewReader(`{"url":"`+srv.URL+`","apiKey":" good "}`))

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "good") {
		t.Fatalf("response leaked api key: %s", rec.Body.String())
	}
	var body mediaServerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Connected || !body.Configured || body.ServerName != "Jellyfin Test" || body.Version != "10.9.0" {
		t.Fatalf("body=%+v", body)
	}
	apiKey, err := db.GetJellyfinAPIKey(context.Background(), conn)
	if err != nil {
		t.Fatalf("get api key: %v", err)
	}
	if apiKey != "good" {
		t.Fatalf("stored api key=%q, want good", apiKey)
	}
	baseURL, err := db.GetJellyfinURL(context.Background(), conn)
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	if baseURL != srv.URL {
		t.Fatalf("stored url=%q, want %q", baseURL, srv.URL)
	}
}

func TestAdminJellyfinConfigSetRejectsFailedConnectivity(t *testing.T) {
	app, conn := testAdminApp(t)
	srv := testJellyfinServer(t, "good")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/admin/jellyfin/config", strings.NewReader(`{"url":"`+srv.URL+`","apiKey":"bad"}`))

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "bad") || strings.Contains(rec.Body.String(), "X-Emby-Token") {
		t.Fatalf("response leaked api key: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Jellyfin rejected the API key") {
		t.Fatalf("response did not include friendly key error: %s", rec.Body.String())
	}
	apiKey, err := db.GetJellyfinAPIKey(context.Background(), conn)
	if err != nil {
		t.Fatalf("get api key: %v", err)
	}
	if apiKey != "" {
		t.Fatalf("stored api key=%q, want empty", apiKey)
	}
}

func TestAdminJellyfinConfigClearPreservesURL(t *testing.T) {
	app, conn := testAdminApp(t)
	if err := db.SetJellyfinURL(context.Background(), conn, "http://jellyfin.example"); err != nil {
		t.Fatalf("set url: %v", err)
	}
	if err := db.SetJellyfinAPIKey(context.Background(), conn, "good"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/jellyfin/config", nil)

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	apiKey, err := db.GetJellyfinAPIKey(context.Background(), conn)
	if err != nil {
		t.Fatalf("get api key: %v", err)
	}
	if apiKey != "" {
		t.Fatalf("stored api key=%q, want empty", apiKey)
	}
	baseURL, err := db.GetJellyfinURL(context.Background(), conn)
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	if baseURL != "http://jellyfin.example" {
		t.Fatalf("url=%q, want preserved", baseURL)
	}
}

func testJellyfinServer(t *testing.T, wantAPIKey string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Emby-Token"); got != wantAPIKey {
			http.Error(w, "bad api key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/System/Info":
			_, _ = w.Write([]byte(`{"ServerName":"Jellyfin Test","Version":"10.9.0","Id":"server-id"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
