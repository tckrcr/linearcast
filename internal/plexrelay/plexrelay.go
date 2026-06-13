package plexrelay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout  = 30 * time.Second
	proxyTimeout    = 15 * time.Second
	plexClientID    = "linearcast-plexrelay"
	plexProduct     = "linearcast"
	plexVersion     = "1.0"
	plexDevice      = "Linux"
	plexDeviceName  = "linearcast"
	sessionKeyChars = 16
)

// Session represents a single viewer's Plex transcode session.
// Each viewer gets their own session seeked to the current wall-clock offset.
type Session struct {
	ViewerToken string
	SessionID   string
	MediaKey    string
	OffsetMs    int64
	Master      []byte
	CreatedAt   time.Time
	baseURL     string
	token       string
}

// Manager manages viewer Plex transcode sessions.
type Manager struct {
	client   *http.Client
	baseURL  string
	token    string
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager creates a Manager that creates Plex transcode sessions at baseURL
// authenticated with token. If client is nil, http.DefaultClient is used.
func NewManager(baseURL, token string, client *http.Client) *Manager {
	if client == nil {
		client = http.DefaultClient
	}
	return &Manager{
		client:   client,
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		sessions: make(map[string]*Session),
	}
}

// BaseURL returns the configured Plex server URL.
func (m *Manager) BaseURL() string {
	return m.baseURL
}

// Token returns the configured Plex token.
func (m *Manager) Token() string {
	return m.token
}

// CreateSession starts a Plex transcode session for the given mediaKey at the
// given offset. Plex returns the master HLS manifest from the start request; the
// session stores it and later proxy requests resolve its relative paths under
// /video/:/transcode/universal.
func (m *Manager) CreateSession(ctx context.Context, mediaKey string, offsetMs int64) (*Session, error) {
	sessionID, err := randomHex(sessionKeyChars)
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}

	u := fmt.Sprintf("%s/video/:/transcode/universal/start", m.baseURL)
	q := url.Values{}
	q.Set("path", mediaKey)
	q.Set("mediaIndex", "0")
	q.Set("partIndex", "0")
	q.Set("protocol", "hls")
	q.Set("offsetMilliseconds", fmt.Sprintf("%d", offsetMs))
	q.Set("fastSeek", "1")
	q.Set("directPlay", "0")
	q.Set("directStream", "0")
	q.Set("subtitleSize", "100")
	q.Set("audioBoost", "100")
	q.Set("session", sessionID)
	q.Set("X-Plex-Product", plexProduct)
	q.Set("X-Plex-Version", plexVersion)
	q.Set("X-Plex-Client-Identifier", plexClientID)
	q.Set("X-Plex-Platform", "Chrome")
	q.Set("X-Plex-Platform-Version", "")
	q.Set("X-Plex-Device", plexDevice)
	q.Set("X-Plex-Device-Name", plexDeviceName)
	q.Set("X-Plex-Token", m.token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("create plex session request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.apple.mpegurl")
	setPlexClientHeaders(req, m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("create plex session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("plex transcode start: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	master, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read plex transcode manifest: %w", err)
	}
	if !strings.Contains(string(master), "#EXTM3U") {
		return nil, fmt.Errorf("plex transcode start did not return an HLS manifest")
	}

	viewerToken, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("generate viewer token: %w", err)
	}

	s := &Session{
		ViewerToken: viewerToken,
		SessionID:   sessionID,
		MediaKey:    mediaKey,
		OffsetMs:    offsetMs,
		Master:      master,
		CreatedAt:   time.Now(),
		baseURL:     m.baseURL,
		token:       m.token,
	}

	m.mu.Lock()
	m.sessions[viewerToken] = s
	m.mu.Unlock()

	return s, nil
}

// Lookup retrieves a session by viewer token.
func (m *Manager) Lookup(viewerToken string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[viewerToken]
}

// Remove deletes a session by viewer token.
func (m *Manager) Remove(viewerToken string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, viewerToken)
}

const sessionMaxAge = 15 * time.Minute

// Run starts a background goroutine that sweeps stale sessions every 5 minutes.
// Call with a cancellable context; stops when ctx is done.
func (m *Manager) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.sweepStale()
			}
		}
	}()
}

func (m *Manager) sweepStale() {
	deadline := time.Now().Add(-sessionMaxAge)
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, s := range m.sessions {
		if s.CreatedAt.Before(deadline) {
			delete(m.sessions, token)
		}
	}
}

// Token returns the Plex auth token for this session.
func (s *Session) Token() string {
	return s.token
}

// ProxyPathPrefix returns the linearcast path prefix for proxied Plex content
// for this viewer session.
func (s *Session) ProxyPathPrefix(channelID string) string {
	return fmt.Sprintf("/channel/%s/plexrelay/%s", channelID, s.ViewerToken)
}

// MasterManifest returns the stored Plex start response rewritten through the
// linearcast proxy.
func (s *Session) MasterManifest(channelID string) []byte {
	return s.rewriteManifest(s.Master, channelID, "")
}

// PlexResourceURL returns the full Plex URL for a proxied HLS path. restPath is
// relative to Plex's /video/:/transcode/universal endpoint, for example
// session/<id>/base/index.m3u8 or session/<id>/base/00000.ts.
func (s *Session) PlexResourceURL(restPath string) string {
	restPath = strings.TrimPrefix(strings.TrimSpace(restPath), "/")
	restPath = strings.TrimPrefix(restPath, "video/:/transcode/universal/")
	q := url.Values{}
	q.Set("X-Plex-Token", s.token)
	return fmt.Sprintf("%s/video/:/transcode/universal/%s?%s", s.baseURL, restPath, q.Encode())
}

// FetchManifest fetches a Plex HLS manifest and rewrites relative and absolute
// Plex session paths to point at the linearcast proxy instead.
func (s *Session) FetchManifest(ctx context.Context, client *http.Client, restPath, channelID string) ([]byte, error) {
	manifestURL := s.PlexResourceURL(restPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	setPlexClientHeaders(req, s.token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch plex manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("plex manifest: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read plex manifest body: %w", err)
	}

	return s.rewriteManifest(body, channelID, restPath), nil
}

// ProxySegmentURL returns the full Plex URL for a proxied segment path.
// segmentPath is the portion of the path after the proxy prefix, including
// any rendition subdirectory and the segment filename.
func (s *Session) ProxySegmentURL(segmentPath string) string {
	return s.PlexResourceURL(segmentPath)
}

// FetchSegment fetches a segment from Plex and returns the response for proxying.
func (s *Session) FetchSegment(ctx context.Context, client *http.Client, segmentURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, segmentURL, nil)
	if err != nil {
		return nil, err
	}
	setPlexClientHeaders(req, s.token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch plex segment: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("plex segment: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// setPlexClientHeaders sets the standard Plex client identification headers on
// req. Plex uses these headers (not just query params) for session registration
// and activity tracking in its dashboard.
func setPlexClientHeaders(req *http.Request, token string) {
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Version", plexVersion)
	req.Header.Set("X-Plex-Client-Identifier", plexClientID)
	req.Header.Set("X-Plex-Platform", "Chrome")
	req.Header.Set("X-Plex-Device", plexDevice)
	req.Header.Set("X-Plex-Device-Name", plexDeviceName)
}

// randomHex returns n random bytes encoded as hex.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Session) rewriteManifest(body []byte, channelID, sourcePath string) []byte {
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = rewriteURIAttributes(line, func(raw string) string {
				return s.proxyURI(raw, channelID, sourcePath)
			})
			continue
		}
		prefixLen := len(line) - len(strings.TrimLeft(line, " \t"))
		suffixLen := len(line) - len(strings.TrimRight(line, " \t\r"))
		prefix := line[:prefixLen]
		suffix := line[len(line)-suffixLen:]
		lines[i] = prefix + s.proxyURI(trimmed, channelID, sourcePath) + suffix
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteURIAttributes(line string, rewrite func(string) string) string {
	const key = `URI="`
	var b strings.Builder
	rest := line
	for {
		idx := strings.Index(rest, key)
		if idx < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:idx+len(key)])
		rest = rest[idx+len(key):]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rewrite(rest[:end]))
		rest = rest[end:]
	}
}

func (s *Session) proxyURI(raw, channelID, sourcePath string) string {
	rel, ok := plexUniversalRelativePath(raw, sourcePath)
	if !ok {
		return raw
	}
	return s.ProxyPathPrefix(channelID) + "/" + rel
}

func plexUniversalRelativePath(raw, sourcePath string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	if u.Scheme != "" || u.Host != "" {
		prefix := "/video/:/transcode/universal/"
		if !strings.HasPrefix(u.Path, prefix) {
			return "", false
		}
		return appendQuery(strings.TrimPrefix(u.Path, prefix), u.RawQuery), true
	}

	rawPath := u.Path
	const prefix = "/video/:/transcode/universal/"
	switch {
	case strings.HasPrefix(rawPath, prefix):
		return appendQuery(strings.TrimPrefix(rawPath, prefix), u.RawQuery), true
	case strings.HasPrefix(rawPath, "video/:/transcode/universal/"):
		return appendQuery(strings.TrimPrefix(rawPath, "video/:/transcode/universal/"), u.RawQuery), true
	case strings.HasPrefix(rawPath, "session/"):
		return appendQuery(rawPath, u.RawQuery), true
	case rawPath != "" && sourcePath != "":
		base := path.Dir(strings.TrimPrefix(sourcePath, "/"))
		return appendQuery(path.Clean(path.Join(base, rawPath)), u.RawQuery), true
	default:
		return "", false
	}
}

func appendQuery(rel, rawQuery string) string {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		if rawQuery == "" {
			return rel
		}
		return rel + "?" + rawQuery
	}
	for key := range q {
		if strings.EqualFold(key, "X-Plex-Token") {
			q.Del(key)
		}
	}
	if enc := q.Encode(); enc != "" {
		return rel + "?" + enc
	}
	return rel
}
