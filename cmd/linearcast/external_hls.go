package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

const (
	externalHLSTimeout       = 5 * time.Second
	externalHLSCooldown      = 5 * time.Second
	externalHLSLogSampleEach = 60 * time.Second
)

type externalHLSProxyState struct {
	mu       sync.Mutex
	failures map[string]*externalHLSFailure
}

type externalHLSFailure struct {
	nextAttempt    time.Time
	lastLog        time.Time
	lastError      string
	suppressedLogs int
}

func (a *app) handleExternalHLSManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	upstream, ok := a.externalHLSURL(w, r, channelID)
	if !ok {
		return
	}
	a.proxyExternalHLS(w, r, channelID, upstream, "application/vnd.apple.mpegurl")
}

func (a *app) handleExternalHLSSegment(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	name := r.PathValue("name")
	if !validExternalHLSSegmentName(name) {
		http.NotFound(w, r)
		return
	}
	upstream, ok := a.externalHLSURL(w, r, channelID)
	if !ok {
		return
	}
	segmentURL := upstream.ResolveReference(&url.URL{Path: name})
	a.proxyExternalHLS(w, r, channelID, segmentURL, "video/mp2t")
}

func (a *app) externalHLSURL(w http.ResponseWriter, r *http.Request, channelID string) (*url.URL, bool) {
	ch, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("channel lookup: %v", err), http.StatusInternalServerError)
		return nil, false
	}
	if ch == nil || !ch.Enabled || ch.UpstreamHLSURL == nil {
		http.NotFound(w, r)
		return nil, false
	}
	u, err := url.Parse(*ch.UpstreamHLSURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "invalid upstream hls url", http.StatusBadGateway)
		return nil, false
	}
	return u, true
}

func (a *app) proxyExternalHLS(w http.ResponseWriter, r *http.Request, channelID string, upstream *url.URL, contentType string) {
	state := a.externalHLSState()
	key := externalHLSFailureKey(channelID, upstream)
	if retryAt, ok := state.coolingDown(key, time.Now()); ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", time.Until(retryAt).Seconds()))
		http.Error(w, "external hls upstream temporarily unavailable", http.StatusServiceUnavailable)
		return
	}

	client := a.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(r.Context(), externalHLSTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		state.recordFailure(key, upstream.String(), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		state.recordFailure(key, upstream.String(), fmt.Errorf("status %d", resp.StatusCode))
	} else {
		state.recordSuccess(key)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		state.recordFailure(key, upstream.String(), err)
	}
}

func (a *app) externalHLSState() *externalHLSProxyState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.externalHLS == nil {
		a.externalHLS = &externalHLSProxyState{failures: map[string]*externalHLSFailure{}}
	}
	return a.externalHLS
}

func externalHLSFailureKey(channelID string, upstream *url.URL) string {
	return channelID + "|" + upstream.Scheme + "://" + upstream.Host
}

func (s *externalHLSProxyState) coolingDown(key string, now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failures[key]
	if f == nil || now.After(f.nextAttempt) {
		return time.Time{}, false
	}
	return f.nextAttempt, true
}

func (s *externalHLSProxyState) recordSuccess(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, key)
}

func (s *externalHLSProxyState) recordFailure(key, upstream string, err error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = map[string]*externalHLSFailure{}
	}
	f := s.failures[key]
	if f == nil {
		f = &externalHLSFailure{}
		s.failures[key] = f
	}
	f.nextAttempt = now.Add(externalHLSCooldown)
	f.lastError = err.Error()
	if f.lastLog.IsZero() || now.Sub(f.lastLog) >= externalHLSLogSampleEach {
		if f.suppressedLogs > 0 {
			log.Printf("external hls upstream failure upstream=%q err=%q next_retry=%s suppressed=%d",
				upstream, f.lastError, f.nextAttempt.Format(time.RFC3339), f.suppressedLogs)
		} else {
			log.Printf("external hls upstream failure upstream=%q err=%q next_retry=%s",
				upstream, f.lastError, f.nextAttempt.Format(time.RFC3339))
		}
		f.lastLog = now
		f.suppressedLogs = 0
		return
	}
	f.suppressedLogs++
}

func validExternalHLSSegmentName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return strings.HasSuffix(name, ".ts")
}
