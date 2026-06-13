package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

type routeInventoryCase struct {
	method string
	route  string
	path   string
	public bool
	access string
}

var adminRouteInventory = []routeInventoryCase{
	{http.MethodGet, "/api/auth/status", "/api/auth/status", true, "public read"},
	{http.MethodPost, "/api/auth/login", "/api/auth/login", true, "public write"},
	{http.MethodPost, "/api/auth/logout", "/api/auth/logout", true, "public write"},
	{http.MethodPost, "/api/auth/change-password", "/api/auth/change-password", true, "public write"},
	{http.MethodGet, "/api/admin/write-log", "/api/admin/write-log", false, "protected admin read"},
	{http.MethodGet, "/api/schedule-builder/source-status", "/api/schedule-builder/source-status", false, "protected schedule-builder read"},
	{http.MethodGet, "/api/schedule-builder/package-profiles", "/api/schedule-builder/package-profiles", false, "protected schedule-builder read"},
	{http.MethodGet, "/api/schedule-builder/package-candidates", "/api/schedule-builder/package-candidates", false, "protected schedule-builder read"},
	{http.MethodGet, "/api/schedule-builder/shows", "/api/schedule-builder/shows", false, "protected schedule-builder read"},
	{http.MethodGet, "/api/schedule-builder/albums", "/api/schedule-builder/albums", false, "protected schedule-builder read"},
	{http.MethodGet, "/api/schedule-builder/by-group", "/api/schedule-builder/by-group", false, "protected schedule-builder read"},
	{http.MethodGet, "/api/schedule-builder/filler-candidates", "/api/schedule-builder/filler-candidates", false, "protected schedule-builder read"},
	{http.MethodPost, "/api/schedule-builder/channels", "/api/schedule-builder/channels", false, "protected schedule-builder write"},
	{http.MethodGet, "/api/admin/chain-integrity", "/api/admin/chain-integrity", false, "protected admin read"},
	{http.MethodGet, "/api/admin/maintenance/package-integrity", "/api/admin/maintenance/package-integrity", false, "protected admin read"},
	{http.MethodDelete, "/api/admin/maintenance/missing-media", "/api/admin/maintenance/missing-media", false, "protected admin write"},
	{http.MethodDelete, "/api/admin/maintenance/orphan-packages", "/api/admin/maintenance/orphan-packages", false, "protected admin write"},
	{http.MethodDelete, "/api/admin/maintenance/packages", "/api/admin/maintenance/packages", false, "protected admin write"},
	{http.MethodDelete, "/api/admin/maintenance/orphan-encodes", "/api/admin/maintenance/orphan-encodes", false, "protected admin write"},
	{http.MethodPost, "/api/admin/maintenance/optimize-db", "/api/admin/maintenance/optimize-db", false, "protected admin write"},
	{http.MethodGet, "/api/healthz", "/api/healthz", true, "public read"},
	{http.MethodGet, "/api/status", "/api/status", false, "protected admin read"},
	{http.MethodGet, "/api/playable-sources", "/api/playable-sources", true, "public viewer read"},
	{http.MethodGet, "/api/guide", "/api/guide", true, "public viewer read"},
	{http.MethodGet, "/api/now", "/api/now", false, "protected admin read"},
	{http.MethodGet, "/api/playing", "/api/playing", false, "protected admin read"},
	{http.MethodGet, "/api/queue-depth", "/api/queue-depth", false, "protected admin read"},
	{http.MethodGet, "/api/schedule/gaps", "/api/schedule/gaps", false, "protected admin read"},
	{http.MethodGet, "/api/cache/summary", "/api/cache/summary", false, "protected admin read"},
	{http.MethodDelete, "/api/cache/invalid-profiles", "/api/cache/invalid-profiles", false, "protected admin write"},
	{http.MethodGet, "/api/cache/unreferenced", "/api/cache/unreferenced", false, "protected admin read"},
	{http.MethodGet, "/api/channels/{channelID}/now", "/api/channels/ch/now", true, "public viewer read"},
	{http.MethodGet, "/api/channels/{channelID}/media", "/api/channels/ch/media", false, "protected admin read"},
	{http.MethodGet, "/api/channels/{channelID}/filler-assets", "/api/channels/ch/filler-assets", false, "protected admin read"},
	{http.MethodGet, "/api/channels/{channelID}/schedule", "/api/channels/ch/schedule", false, "protected admin read"},
	{http.MethodGet, "/api/channels/{channelID}/schedule/preview", "/api/channels/ch/schedule/preview", false, "protected admin read"},
	{http.MethodGet, "/api/channels/{channelID}/history", "/api/channels/ch/history?since=0", false, "protected admin read"},
	{http.MethodGet, "/api/channels/{channelID}/policy", "/api/channels/ch/policy", false, "protected admin read"},
	{http.MethodPut, "/api/channels/{channelID}/policy", "/api/channels/ch/policy", false, "protected admin write"},
	{http.MethodPut, "/api/channels/{channelID}/upstream-hls", "/api/channels/ch/upstream-hls", false, "protected admin write"},
	{http.MethodGet, "/api/channels/{channelID}/artwork", "/api/channels/ch/artwork", false, "protected admin read"},
	{http.MethodPut, "/api/channels/{channelID}/artwork", "/api/channels/ch/artwork", false, "protected admin write"},
	{http.MethodDelete, "/api/channels/{channelID}/artwork", "/api/channels/ch/artwork", false, "protected admin write"},
	{http.MethodGet, "/api/admin/plex/status", "/api/admin/plex/status", false, "protected admin read"},
	{http.MethodPut, "/api/admin/plex/config", "/api/admin/plex/config", false, "protected admin write"},
	{http.MethodDelete, "/api/admin/plex/config", "/api/admin/plex/config", false, "protected admin write"},
	{http.MethodGet, "/api/admin/plex/libraries", "/api/admin/plex/libraries", false, "protected admin read"},
	{http.MethodPost, "/api/admin/plex/scan", "/api/admin/plex/scan", false, "protected admin write"},
	{http.MethodGet, "/api/admin/jellyfin/status", "/api/admin/jellyfin/status", false, "protected admin read"},
	{http.MethodPut, "/api/admin/jellyfin/config", "/api/admin/jellyfin/config", false, "protected admin write"},
	{http.MethodDelete, "/api/admin/jellyfin/config", "/api/admin/jellyfin/config", false, "protected admin write"},
	{http.MethodGet, "/api/admin/jellyfin/libraries", "/api/admin/jellyfin/libraries", false, "protected admin read"},
	{http.MethodPost, "/api/admin/jellyfin/scan", "/api/admin/jellyfin/scan", false, "protected admin write"},
	{http.MethodGet, "/api/admin/local-sources", "/api/admin/local-sources", false, "protected admin read"},
	{http.MethodPost, "/api/admin/local-sources", "/api/admin/local-sources", false, "protected admin write"},
	{http.MethodPut, "/api/admin/local-sources/{id}", "/api/admin/local-sources/local1", false, "protected admin write"},
	{http.MethodDelete, "/api/admin/local-sources/{id}", "/api/admin/local-sources/local1", false, "protected admin write"},
	{http.MethodPost, "/api/admin/local-sources/{id}/scan", "/api/admin/local-sources/local1/scan", false, "protected admin write"},
	{http.MethodPost, "/api/channels", "/api/channels", false, "protected admin write"},
	{http.MethodPost, "/api/channels/probe-upstream", "/api/channels/probe-upstream", false, "protected admin write"},
	{http.MethodGet, "/api/channels", "/api/channels", false, "protected admin read"},
	{http.MethodDelete, "/api/channels/{channelID}", "/api/channels/ch", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/clone", "/api/channels/ch/clone", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/disable", "/api/channels/ch/disable", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/enable", "/api/channels/ch/enable", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/hide-from-guide", "/api/channels/ch/hide-from-guide", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/show-in-guide", "/api/channels/ch/show-in-guide", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/extend", "/api/channels/ch/extend", false, "protected admin write"},
	{http.MethodDelete, "/api/channels/{channelID}/schedule", "/api/channels/ch/schedule", false, "protected admin write"},
	{http.MethodDelete, "/api/channels/{channelID}/schedule/range", "/api/channels/ch/schedule/range", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/schedule/gaps/fill", "/api/channels/ch/schedule/gaps/fill", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/schedule/recompose", "/api/channels/ch/schedule/recompose", false, "protected admin write"},
	{http.MethodPut, "/api/channels/{channelID}/schedule/window/order", "/api/channels/ch/schedule/window/order", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/schedule/entries", "/api/channels/ch/schedule/entries", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/schedule/entries/{entryId}/after", "/api/channels/ch/schedule/entries/1000/after", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/schedule/entries/{entryId}/before", "/api/channels/ch/schedule/entries/1000/before", false, "protected admin write"},
	{http.MethodDelete, "/api/channels/{channelID}/schedule/entries/{entryId}", "/api/channels/ch/schedule/entries/1000", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/restart-playback", "/api/channels/ch/restart-playback", false, "protected admin write"},
	{http.MethodGet, "/api/subtitle-settings", "/api/subtitle-settings", true, "public viewer read"},
	{http.MethodPut, "/api/subtitle-settings", "/api/subtitle-settings", false, "protected admin write"},
	{http.MethodGet, "/api/admin/subtitle-scan", "/api/admin/subtitle-scan", false, "protected admin read"},
	{http.MethodPost, "/api/admin/subtitle-scan", "/api/admin/subtitle-scan", false, "protected admin write"},
	{http.MethodGet, "/api/admin/subtitle-extract-all", "/api/admin/subtitle-extract-all", false, "protected admin read"},
	{http.MethodPost, "/api/admin/subtitle-extract-all", "/api/admin/subtitle-extract-all", false, "protected admin write"},
	{http.MethodPost, "/api/media/{mediaID}/subtitles/extract", "/api/media/m1/subtitles/extract", false, "protected admin write"},
	{http.MethodGet, "/api/media/{mediaID}/subtitles", "/api/media/m1/subtitles", false, "protected admin read"},
	{http.MethodDelete, "/api/media/{mediaID}/subtitles/{language}", "/api/media/m1/subtitles/eng", false, "protected admin write"},
	{http.MethodGet, "/api/fs/browse", "/api/fs/browse", false, "protected admin read"},
	{http.MethodPost, "/api/ingest", "/api/ingest", false, "protected admin write"},
	{http.MethodGet, "/api/ingest/{id}", "/api/ingest/job1", false, "protected admin read"},
	{http.MethodPost, "/api/ingest/{id}/cancel", "/api/ingest/job1/cancel", false, "protected admin write"},
	{http.MethodGet, "/api/media", "/api/media", false, "protected admin read"},
	{http.MethodGet, "/api/media/groups", "/api/media/groups", false, "protected admin read"},
	{http.MethodGet, "/api/media/shows", "/api/media/shows", false, "protected admin read"},
	{http.MethodGet, "/api/media/albums", "/api/media/albums", false, "protected admin read"},
	{http.MethodGet, "/api/media/by-group", "/api/media/by-group", false, "protected admin read"},
	{http.MethodGet, "/api/media/package-profiles", "/api/media/package-profiles", false, "protected admin read"},
	{http.MethodPut, "/api/package-profiles/{name}", "/api/package-profiles/h264-main-1080p", false, "protected admin write"},
	{http.MethodDelete, "/api/package-profiles/{name}", "/api/package-profiles/h264-main-1080p", false, "protected admin write"},
	{http.MethodPut, "/api/admin/default-packaged-profile", "/api/admin/default-packaged-profile", false, "protected admin write"},
	{http.MethodGet, "/api/media/package-candidates", "/api/media/package-candidates", false, "protected admin read"},
	{http.MethodPost, "/api/media/package", "/api/media/package", false, "protected admin write"},
	{http.MethodPost, "/api/media/package/cancel", "/api/media/package/cancel", false, "protected admin write"},
	{http.MethodGet, "/api/channels/{channelID}/profile-migration", "/api/channels/ch/profile-migration?profile=h264-main-1080p", false, "protected admin read"},
	{http.MethodPost, "/api/channels/{channelID}/profile-migration", "/api/channels/ch/profile-migration", false, "protected admin write"},
	{http.MethodGet, "/api/filler-assets", "/api/filler-assets", false, "protected admin read"},
	{http.MethodPost, "/api/filler-assets", "/api/filler-assets", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/media", "/api/channels/ch/media", false, "protected admin write"},
	{http.MethodDelete, "/api/channels/{channelID}/media/{mediaID}", "/api/channels/ch/media/m1", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/media/{mediaID}/move", "/api/channels/ch/media/m1/move", false, "protected admin write"},
	{http.MethodPut, "/api/channels/{channelID}/media/order", "/api/channels/ch/media/order", false, "protected admin write"},
	{http.MethodPost, "/api/channels/{channelID}/filler-assets", "/api/channels/ch/filler-assets", false, "protected admin write"},
	{http.MethodDelete, "/api/channels/{channelID}/filler-assets/{assetID}", "/api/channels/ch/filler-assets/a1", false, "protected admin write"},
	{http.MethodPost, "/api/admin/encoders", "/api/admin/encoders", false, "protected admin write"},
	{http.MethodGet, "/api/admin/encoders", "/api/admin/encoders", false, "protected admin read"},
	{http.MethodPatch, "/api/admin/encoders/{id}", "/api/admin/encoders/enc_x", false, "protected admin write"},
	{http.MethodPost, "/api/admin/encoders/{id}/revoke", "/api/admin/encoders/enc_x/revoke", false, "protected admin write"},
	{http.MethodDelete, "/api/admin/encoders/{id}", "/api/admin/encoders/enc_x", false, "protected admin write"},
	{http.MethodGet, "/api/admin/encoders/downloads", "/api/admin/encoders/downloads", false, "protected admin read"},
	{http.MethodGet, "/api/admin/encoders/download/{platform}", "/api/admin/encoders/download/darwin-arm64", false, "protected admin read"},
	{http.MethodPut, "/api/admin/local-worker", "/api/admin/local-worker", false, "protected admin write"},
	{http.MethodGet, "/api/admin/scheduler-tunables", "/api/admin/scheduler-tunables", false, "protected admin read"},
	{http.MethodPut, "/api/admin/scheduler-tunables", "/api/admin/scheduler-tunables", false, "protected admin write"},
	{http.MethodGet, "/api/admin/encoder-sweeper-settings", "/api/admin/encoder-sweeper-settings", false, "protected admin read"},
	{http.MethodPut, "/api/admin/encoder-sweeper-settings", "/api/admin/encoder-sweeper-settings", false, "protected admin write"},
	{http.MethodGet, "/api/admin/on-demand-session-settings", "/api/admin/on-demand-session-settings", false, "protected admin read"},
	{http.MethodPut, "/api/admin/on-demand-session-settings", "/api/admin/on-demand-session-settings", false, "protected admin write"},
	{http.MethodGet, "/api/encoder/ping", "/api/encoder/ping", false, "protected encoder read"},
	{http.MethodPost, "/api/encoder/ping", "/api/encoder/ping", false, "protected encoder write"},
	{http.MethodPost, "/api/encoder/claim", "/api/encoder/claim", false, "protected encoder write"},
	{http.MethodGet, "/api/encoder/media/{mediaID}", "/api/encoder/media/m1", false, "protected encoder read"},
	{http.MethodPost, "/api/encoder/jobs/{packageID}/heartbeat", "/api/encoder/jobs/pkg-x/heartbeat", false, "protected encoder write"},
	{http.MethodPost, "/api/encoder/jobs/{packageID}/complete", "/api/encoder/jobs/pkg-x/complete", false, "protected encoder write"},
	{http.MethodPost, "/api/encoder/jobs/{packageID}/fail", "/api/encoder/jobs/pkg-x/fail", false, "protected encoder write"},
}

func TestAdminRouteInventoryCoversRegisteredRoutes(t *testing.T) {
	source, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	re := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`)
	for _, match := range re.FindAllStringSubmatch(string(source), -1) {
		registered[match[1]+" "+match[2]] = true
	}
	if len(registered) == 0 {
		t.Fatal("no registered routes found in routes.go")
	}
	for _, tt := range adminRouteInventory {
		key := tt.method + " " + tt.route
		if !registered[key] {
			t.Fatalf("inventory includes %s, but routes.go does not register it", key)
		}
		delete(registered, key)
	}
	for key := range registered {
		t.Fatalf("route %s is registered but missing from adminRouteInventory", key)
	}
}

func TestAdminRouteInventoryMatchesPublicBoundary(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)

	for _, tt := range adminRouteInventory {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			got := app.isPublicRoute(req)
			if got != tt.public {
				t.Fatalf("%s classified as public=%v, want %v (%s)", tt.path, got, tt.public, tt.access)
			}
		})
	}
}

func TestAdminRouteInventoryProtectedRoutesRequireSession(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)
	handler := app.Handler()

	for _, tt := range adminRouteInventory {
		if tt.public {
			continue
		}
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("%s returned %d without session, want %d (%s)", tt.path, rr.Code, http.StatusUnauthorized, tt.access)
			}
		})
	}
}

func TestAdminAuthAllowsPublicRoutes(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)

	tests := []string{
		"/api/healthz",
		"/api/playable-sources",
		"/api/guide",
		"/api/channels/ch/now",
		"/api/auth/status",
	}
	for _, path := range tests {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()

		app.Handler().ServeHTTP(rr, req)

		if rr.Code == http.StatusUnauthorized {
			t.Fatalf("%s should be public, got %d", path, rr.Code)
		}
	}
}

func TestAdminAuthRejectsProtectedRoutesWithoutSession(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)

	req := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates", nil)
	rr := httptest.NewRecorder()

	app.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAdminAuthLoginAndLogout(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)
	handler := app.Handler()

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"secret"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)

	if loginRR.Code != http.StatusOK {
		t.Fatalf("login status=%d, want %d body=%s", loginRR.Code, http.StatusOK, loginRR.Body.String())
	}
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}

	authedReq := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates", nil)
	authedReq.AddCookie(cookies[0])
	authedRR := httptest.NewRecorder()
	handler.ServeHTTP(authedRR, authedReq)
	if authedRR.Code == http.StatusUnauthorized {
		t.Fatalf("authenticated request got %d", authedRR.Code)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.AddCookie(cookies[0])
	logoutRR := httptest.NewRecorder()
	handler.ServeHTTP(logoutRR, logoutReq)
	if logoutRR.Code != http.StatusOK {
		t.Fatalf("logout status=%d, want %d", logoutRR.Code, http.StatusOK)
	}

	afterLogoutReq := httptest.NewRequest(http.MethodGet, "/api/media/package-candidates", nil)
	afterLogoutReq.AddCookie(cookies[0])
	afterLogoutRR := httptest.NewRecorder()
	handler.ServeHTTP(afterLogoutRR, afterLogoutReq)
	if afterLogoutRR.Code != http.StatusUnauthorized {
		t.Fatalf("after logout status=%d, want %d", afterLogoutRR.Code, http.StatusUnauthorized)
	}
}

func TestAdminAuthSetsSecureCookieWhenConfigured(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now, true)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	app.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count=%d, want 1", len(cookies))
	}
	if !cookies[0].Secure {
		t.Fatal("session cookie Secure=false, want true")
	}
}

func TestAdminCSRFMiddlewareRejectsCrossOriginProtectedWrite(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)
	handler := app.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://admin.local/api/media/package", nil)
	req.Host = "admin.local"
	req.Header.Set("Origin", "http://evil.local")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestAdminCSRFMiddlewareAllowsSameOriginProtectedWrite(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)
	handler := app.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://admin.local/api/media/package", nil)
	req.Host = "admin.local"
	req.Header.Set("Origin", "http://admin.local")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusNoContent)
	}
}

func TestAdminCSRFMiddlewareRejectsCrossOriginLogout(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)
	handler := app.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://admin.local/api/auth/logout", nil)
	req.Host = "admin.local"
	req.Header.Set("Referer", "http://evil.local/page")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestAdminDoesNotEmitCredentialedCORSHeaders(t *testing.T) {
	app, _ := testAdminApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.Header.Set("Origin", "http://evil.local")
	rr := httptest.NewRecorder()

	app.Handler().ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin=%q, want empty", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials=%q, want empty", got)
	}
}

func TestAdminAuthRejectsBadPassword(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	app.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Fatal("bad password set a cookie")
	}
}

func TestAdminAuthRateLimitsRepeatedBadPasswords(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)
	handler := app.Handler()

	for i := 0; i < maxLoginFailures; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "192.0.2.10")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d, want %d", i+1, rr.Code, http.StatusUnauthorized)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "192.0.2.10")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusTooManyRequests)
	}
}

func TestAdminAuthCorrectPasswordClearsFailureCounter(t *testing.T) {
	app, _ := testAdminApp(t)
	app.auth = newAuthService("secret", app.now)
	handler := app.Handler()

	for i := 0; i < maxLoginFailures; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "192.0.2.20")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("bad attempt %d status=%d, want %d", i+1, rr.Code, http.StatusUnauthorized)
		}
	}

	goodReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"secret"}`))
	goodReq.Header.Set("Content-Type", "application/json")
	goodReq.Header.Set("X-Forwarded-For", "192.0.2.20")
	goodRR := httptest.NewRecorder()
	handler.ServeHTTP(goodRR, goodReq)
	if goodRR.Code != http.StatusOK {
		t.Fatalf("good login status=%d, want %d", goodRR.Code, http.StatusOK)
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-Forwarded-For", "192.0.2.20")
	badRR := httptest.NewRecorder()
	handler.ServeHTTP(badRR, badReq)
	if badRR.Code != http.StatusUnauthorized {
		t.Fatalf("post-success bad login status=%d, want %d", badRR.Code, http.StatusUnauthorized)
	}
}
