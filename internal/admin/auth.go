package admin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tckrcr/linearcast/internal/db"
)

// bcryptCost is the work factor passed to bcrypt.GenerateFromPassword. Tests
// set this to bcrypt.MinCost to keep the suite fast.
var bcryptCost = bcrypt.DefaultCost

const (
	adminSessionCookie = "linearcast_admin_session"
	adminSessionTTL    = 30 * 24 * time.Hour
	loginFailureWindow = 5 * time.Minute
	maxLoginFailures   = 5
)

type authService struct {
	passwordHash  string
	mustChange    bool
	cookieSecure  bool
	now           func() time.Time
	mu            sync.Mutex
	sessions      map[string]time.Time
	loginFailures map[string]loginFailure
}

type loginFailure struct {
	Count       int
	FirstSeenAt time.Time
}

// newAuthService creates an authService by hashing password with bcryptCost.
// Pass an empty password to disable auth. Tests set bcryptCost = bcrypt.MinCost
// in TestMain to keep the suite fast; production uses bcrypt.DefaultCost.
func newAuthService(password string, now func() time.Time, cookieSecure ...bool) *authService {
	password = strings.TrimSpace(password)
	var hash string
	if password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
		if err != nil {
			panic(fmt.Sprintf("bcrypt admin password: %v", err))
		}
		hash = string(h)
	}
	return newAuthServiceFromHash(hash, false, now, cookieSecure...)
}

// newAuthServiceFromHash creates an authService from a pre-computed bcrypt hash.
// Used by production code that loads the hash from the DB. Pass mustChange=true
// to force the operator to set a new password on first login.
func newAuthServiceFromHash(passwordHash string, mustChange bool, now func() time.Time, cookieSecure ...bool) *authService {
	secure := false
	if len(cookieSecure) > 0 {
		secure = cookieSecure[0]
	}
	return &authService{
		passwordHash:  passwordHash,
		mustChange:    mustChange,
		cookieSecure:  secure,
		now:           now,
		sessions:      make(map[string]time.Time),
		loginFailures: make(map[string]loginFailure),
	}
}

func (s *authService) enabled() bool {
	return s != nil && s.passwordHash != ""
}

func (s *authService) authenticated(r *http.Request) bool {
	if !s.enabled() {
		return true
	}
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[cookie.Value]
	if !ok || !expires.After(s.now()) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func (s *authService) login(password string, client string) (string, loginResult) {
	if !s.enabled() {
		return "", loginOK
	}
	s.mu.Lock()
	hash := s.passwordHash
	s.mu.Unlock()
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		if s.tooManyFailures(client) {
			return "", loginRateLimited
		}
		s.recordFailure(client)
		return "", loginInvalid
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", loginInvalid
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	s.mu.Lock()
	s.sessions[token] = s.now().Add(adminSessionTTL)
	delete(s.loginFailures, client)
	s.mu.Unlock()
	return token, loginOK
}

type loginResult int

const (
	loginOK loginResult = iota
	loginInvalid
	loginRateLimited
)

func (s *authService) tooManyFailures(client string) bool {
	if client == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loginFailures[client]
	if f.Count == 0 {
		return false
	}
	if !s.now().Before(f.FirstSeenAt.Add(loginFailureWindow)) {
		delete(s.loginFailures, client)
		return false
	}
	return f.Count >= maxLoginFailures
}

func (s *authService) recordFailure(client string) {
	if client == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	f := s.loginFailures[client]
	if f.Count == 0 || !now.Before(f.FirstSeenAt.Add(loginFailureWindow)) {
		s.loginFailures[client] = loginFailure{Count: 1, FirstSeenAt: now}
		return
	}
	f.Count++
	s.loginFailures[client] = f
}

func (s *authService) logout(r *http.Request) {
	if !s.enabled() {
		return
	}
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, cookie.Value)
	s.mu.Unlock()
}

func (s *authService) mustChangePassword() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mustChange
}

// verifyPassword checks a plaintext password against the stored bcrypt hash.
func (s *authService) verifyPassword(password string) bool {
	s.mu.Lock()
	hash := s.passwordHash
	s.mu.Unlock()
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// updateHash replaces the in-memory hash, clears the must-change flag, and
// revokes all other sessions after a successful password change. Callers must
// persist to DB before calling this.
func (s *authService) updateHash(hash, keepSession string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.passwordHash = hash
	s.mustChange = false
	for token := range s.sessions {
		if token != keepSession {
			delete(s.sessions, token)
		}
	}
}

func (a *App) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authed := a.auth.authenticated(r)
	writeJSON(w, map[string]any{
		"enabled":       a.auth.enabled(),
		"authenticated": authed,
		"mustChange":    authed && a.auth.mustChangePassword(),
	})
}

func (a *App) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return
	}
	token, result := a.auth.login(req.Password, clientID(r))
	switch result {
	case loginRateLimited:
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed login attempts")
		return
	case loginInvalid:
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid admin password")
		return
	}
	if a.auth.enabled() {
		http.SetCookie(w, &http.Cookie{
			Name:     adminSessionCookie,
			Value:    token,
			Path:     "/",
			MaxAge:   int(adminSessionTTL.Seconds()),
			HttpOnly: true,
			Secure:   a.auth.cookieSecure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	writeJSON(w, map[string]any{
		"enabled":       a.auth.enabled(),
		"authenticated": true,
		"mustChange":    a.auth.mustChangePassword(),
	})
}

func clientID(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return ""
	}
	return cookie.Value
}

func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !a.auth.authenticated(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return
	}
	req.NewPassword = strings.TrimSpace(req.NewPassword)
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "password_too_short", "new password must be at least 8 characters")
		return
	}
	if !a.auth.verifyPassword(req.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "current password is incorrect")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", "failed to hash new password")
		return
	}
	if err := db.SetAdminPasswordHash(r.Context(), a.dbConn, string(hash)); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store new password")
		return
	}
	if err := db.SetAdminPasswordMustChange(r.Context(), a.dbConn, false); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to clear must-change flag")
		return
	}
	a.auth.updateHash(string(hash), sessionToken(r))
	writeJSON(w, map[string]any{
		"enabled":       true,
		"authenticated": true,
		"mustChange":    false,
	})
}

func (a *App) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	a.auth.logout(r)
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.auth.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]bool{"authenticated": false})
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Encoder routes use bearer-token auth (headless polling client) and
		// must not go through cookie auth. The middleware short-circuits here
		// so the rest of the cookie/CSRF stack doesn't have to special-case
		// them at every check.
		if isEncoderRoute(r) {
			r2, ok := a.requireEncoderAuth(w, r)
			if !ok {
				return
			}
			next.ServeHTTP(w, r2)
			return
		}
		if a.isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.auth.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
			return
		}
		if a.auth.mustChangePassword() {
			writeError(w, http.StatusForbidden, "password_must_change", "admin password must be changed before using this endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.auth.enabled() && a.needsSameOriginCheck(r) && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "forbidden_origin", "admin write request must be same-origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) needsSameOriginCheck(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false
	}
	// Encoder polling clients don't use cookies, so there's no CSRF surface.
	// Skip the same-origin check rather than refuse all encoder writes.
	if isEncoderRoute(r) {
		return false
	}
	switch r.URL.Path {
	case "/api/auth/logout", "/api/auth/change-password":
		return true
	}
	return !a.isPublicRoute(r)
}

func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return originHostMatches(origin, r.Host)
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return originHostMatches(referer, r.Host)
	}
	return true
}

func originHostMatches(raw, host string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

func (a *App) isPublicRoute(r *http.Request) bool {
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/auth/") {
		return true
	}
	if r.Method == http.MethodGet {
		switch path {
		case "/api/healthz", "/api/playable-sources", "/api/guide", "/api/public-server-url", "/api/subtitle-settings", "/api/m3u", "/api/xmltv":
			return true
		}
		if strings.HasPrefix(path, "/api/art/media/") {
			return true
		}
		if strings.HasPrefix(path, "/api/channels/") && strings.HasSuffix(path, "/now") {
			return true
		}
	}
	return false
}
