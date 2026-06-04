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

func TestAdminPlexStatusDisconnected(t *testing.T) {
	app, _ := testAdminApp(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/plex/status", nil)

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body plexStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Connected {
		t.Fatalf("connected=%v, want false", body.Connected)
	}
}

func TestAdminPlexConfigSetSuccessDoesNotReturnToken(t *testing.T) {
	app, conn := testAdminApp(t)
	srv := testPlexServer(t, "good")
	rec := httptest.NewRecorder()
	payload := `{"url":"` + srv.URL + `","token":" good "}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/plex/config", strings.NewReader(payload))

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "good") {
		t.Fatalf("response leaked token: %s", rec.Body.String())
	}
	var body plexStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Connected || body.Username != "plex-user" || body.ServerName != "Plex Test" {
		t.Fatalf("body=%+v", body)
	}
	token, err := db.GetPlexToken(context.Background(), conn)
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if token != "good" {
		t.Fatalf("stored token=%q, want good", token)
	}
	storedURL, err := db.GetPlexURL(context.Background(), conn)
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	if storedURL != srv.URL {
		t.Fatalf("stored url=%q, want %q", storedURL, srv.URL)
	}
}

func TestAdminPlexConfigSetStoresPathMap(t *testing.T) {
	app, conn := testAdminApp(t)
	srv := testPlexServer(t, "tok")
	rec := httptest.NewRecorder()
	payload := `{"url":"` + srv.URL + `","token":"tok","pathMap":"/plex=/local"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/plex/config", strings.NewReader(payload))

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	pm, err := db.GetPlexPathMap(context.Background(), conn)
	if err != nil {
		t.Fatalf("get path map: %v", err)
	}
	if pm != "/plex=/local" {
		t.Fatalf("stored path map=%q, want /plex=/local", pm)
	}
}

func TestAdminPlexConfigSetRejectsFailedConnectivity(t *testing.T) {
	app, conn := testAdminApp(t)
	srv := testPlexServer(t, "good")
	rec := httptest.NewRecorder()
	payload := `{"url":"` + srv.URL + `","token":"bad"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/plex/config", strings.NewReader(payload))

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "bad") || strings.Contains(rec.Body.String(), "X-Plex-Token") {
		t.Fatalf("response leaked token: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Plex rejected the token") {
		t.Fatalf("response did not include friendly token error: %s", rec.Body.String())
	}
	token, err := db.GetPlexToken(context.Background(), conn)
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if token != "" {
		t.Fatalf("stored token=%q, want empty", token)
	}
}

func TestAdminPlexConfigClearPreservesLogout(t *testing.T) {
	app, conn := testAdminApp(t)
	if err := db.SetPlexToken(context.Background(), conn, "good"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/plex/config", nil)

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	token, err := db.GetPlexToken(context.Background(), conn)
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if token != "" {
		t.Fatalf("stored token=%q, want empty", token)
	}
	exists, err := db.PlexTokenSettingExists(context.Background(), conn)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !exists {
		t.Fatal("clear deleted token row")
	}
}

func TestAdminPlexStatusConnectionErrorIsFriendly(t *testing.T) {
	app, conn := testAdminApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err := db.SetPlexURL(context.Background(), conn, srv.URL); err != nil {
		t.Fatalf("set url: %v", err)
	}
	srv.Close()
	if err := db.SetPlexToken(context.Background(), conn, "secret-token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/plex/status", nil)

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-token") || strings.Contains(rec.Body.String(), "X-Plex-Token") {
		t.Fatalf("response leaked token: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not connect to Plex") {
		t.Fatalf("response did not include friendly connectivity error: %s", rec.Body.String())
	}
}

func testPlexServer(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("X-Plex-Token"); got != wantToken {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`{"MediaContainer":{"friendlyName":"Plex Test","myPlexUsername":"plex-user"}}`))
		case "/library/sections":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"}]}}`))
		case "/library/sections/1/all":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"m1","title":"Movie","type":"movie","year":2024,"Media":[{"videoResolution":"1080","Part":[{"file":"/plex/movie.mkv"}]}]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
