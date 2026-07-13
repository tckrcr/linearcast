package liveproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type State struct {
	mu       sync.Mutex
	failures map[string]*Failure
}

type Failure struct {
	NextAttempt    time.Time
	LastLog        time.Time
	LastError      string
	SuppressedLogs int
}

type Proxy struct {
	Client        *http.Client
	Timeout       time.Duration
	Cooldown      time.Duration
	LogSampleEach time.Duration
	State         *State
	LogPrefix     string
	// Logger is the structured logger for failure and blocked-address events.
	// When nil, slog.Default() is used.
	Logger *slog.Logger
}

type Request struct {
	Key          string
	Upstream     string
	LogUpstream  string
	ContentType  string
	CacheControl string
	// LogFields are extra stable, greppable key=value pairs appended to the
	// upstream-failure log line (already formatted, space-separated), e.g.
	// "channel_id=abc". Loki queries group failures by these.
	LogFields     string
	Header        func(*http.Request)
	AfterResponse func(*http.Response) error
	Body          func(*http.Response) (io.Reader, error)
}

// failureEvent bundles the fields needed to record and log one upstream
// failure. It keeps the call sites in Serve from carrying a long positional
// argument list and standardizes the emitted log fields.
type failureEvent struct {
	Key        string
	Source     string
	Upstream   string
	Fields     string
	Err        error
	Cooldown   time.Duration
	SampleEach time.Duration
}

const (
	errorBodyLimit    = 512
	ManifestBodyLimit = 1 << 20
)

// staleEntryTTL bounds the failure map. Failures are keyed per upstream
// (channel/upstream), and a key whose last proxied request failed leaves an
// entry that RecordSuccess never gets to clear once that key stops being
// requested — so without pruning the map grows with key churn over the
// process lifetime. An entry whose cooldown expired more than staleEntryTTL ago
// has not been re-armed by a recent request, so it is dead weight: dropping it
// only means the next failure (if any) starts a fresh cooldown. The TTL is far
// longer than any cooldown + manifest-poll gap, so an actively-polled down
// upstream (entry re-armed every cooldown) is never pruned and its log
// rate-limiting is preserved.
const staleEntryTTL = 10 * time.Minute

func NewState() *State {
	return &State{failures: map[string]*Failure{}}
}

func (p Proxy) Serve(w http.ResponseWriter, r *http.Request, pr Request) {
	state := p.State
	if state == nil {
		state = NewState()
	}
	if retryAt, ok := state.CoolingDown(pr.Key, time.Now()); ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", time.Until(retryAt).Seconds()))
		http.Error(w, p.errorMessage("upstream temporarily unavailable"), http.StatusServiceUnavailable)
		return
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pr.Upstream, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if pr.Header != nil {
		pr.Header(req)
	}

	resp, err := client.Do(req)
	if err != nil {
		if requestCanceled(r, err) {
			return
		}
		if IsBlockedAddress(err) {
			// Terminal: the upstream resolves to an address the adapter's SSRF
			// policy forbids. Fail visibly without arming the transient cooldown:
			// it will never recover, and a cooldown 503 would hide the real
			// cause. The operator must repoint the channel at an allowed host.
			p.logBlocked(pr, err)
			http.Error(w, p.errorMessage("upstream address not allowed"), http.StatusBadGateway)
			return
		}
		p.recordFailure(state, pr, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body := strings.TrimSpace(ReadErrorBody(resp.Body))
		err := fmt.Errorf("status %d", resp.StatusCode)
		if body != "" {
			err = fmt.Errorf("status %d: %s", resp.StatusCode, body)
		}
		p.recordFailure(state, pr, err)
		http.Error(w, err.Error(), resp.StatusCode)
		return
	}
	if pr.AfterResponse != nil {
		if err := pr.AfterResponse(resp); err != nil {
			p.recordFailure(state, pr, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	state.RecordSuccess(pr.Key)

	body := io.Reader(resp.Body)
	if pr.Body != nil {
		body, err = pr.Body(resp)
		if err != nil {
			p.recordFailure(state, pr, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	contentType := pr.ContentType
	if contentType == "" {
		contentType = resp.Header.Get("Content-Type")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	cacheControl := pr.CacheControl
	if cacheControl == "" {
		cacheControl = "no-store"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, body); err != nil {
		if requestCanceled(r, err) {
			return
		}
		p.recordFailure(state, pr, err)
	}
}

func requestCanceled(r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if r != nil && r.Context().Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled)
}

func (p Proxy) recordFailure(state *State, pr Request, err error) {
	state.RecordFailure(failureEvent{
		Key:        pr.Key,
		Source:     p.source(),
		Upstream:   p.logUpstream(pr),
		Fields:     pr.LogFields,
		Err:        err,
		Cooldown:   p.Cooldown,
		SampleEach: p.LogSampleEach,
	})
}

// logBlocked emits a structured log line for an SSRF-policy block. It is not
// rate-limited like recordFailure because a blocked address is terminal and
// each occurrence reflects a distinct (mis)configured request worth seeing.
func (p Proxy) logBlocked(pr Request, err error) {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"source", p.source(),
		"upstream", p.logUpstream(pr),
		"err", err,
	}
	if pr.LogFields != "" {
		attrs = append(attrs, "fields", pr.LogFields)
	}
	logger.Warn("live_proxy upstream blocked", attrs...)
}

func (p Proxy) logUpstream(pr Request) string {
	if pr.LogUpstream != "" {
		return pr.LogUpstream
	}
	return pr.Upstream
}

// source returns the adapter source slug used in failure log fields, e.g.
// "external_hls". It is derived from LogPrefix so the existing per-adapter
// prefix doubles as the greppable source= value.
func (p Proxy) source() string {
	if p.LogPrefix == "" {
		return "live_proxy"
	}
	return strings.ReplaceAll(p.LogPrefix, " ", "_")
}

func (p Proxy) errorMessage(msg string) string {
	if p.LogPrefix == "" {
		return msg
	}
	return p.LogPrefix + " " + msg
}

func (s *State) CoolingDown(key string, now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failures[key]
	if f == nil || now.After(f.NextAttempt) {
		return time.Time{}, false
	}
	return f.NextAttempt, true
}

func (s *State) RecordSuccess(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, key)
}

func (s *State) RecordFailure(ev failureEvent) {
	cooldown := ev.Cooldown
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	sampleEach := ev.SampleEach
	if sampleEach <= 0 {
		sampleEach = 60 * time.Second
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistic GC under the lock we already hold: drop entries whose
	// cooldown lapsed long ago so an ended session's residual failure can't
	// linger forever. Runs only on the failure path, so the healthy path stays
	// allocation-free and the map can't grow while no failures occur.
	defer s.pruneStaleLocked(now)
	if s.failures == nil {
		s.failures = map[string]*Failure{}
	}
	f := s.failures[ev.Key]
	if f == nil {
		f = &Failure{}
		s.failures[ev.Key] = f
	}
	f.NextAttempt = now.Add(cooldown)
	f.LastError = ev.Err.Error()
	if f.LastLog.IsZero() || now.Sub(f.LastLog) >= sampleEach {
		source := ev.Source
		if source == "" {
			source = "live_proxy"
		}
		logger := slog.Default()
		attrs := []any{
			"source", source,
			"upstream", ev.Upstream,
			"err", f.LastError,
			"cooldown_until", f.NextAttempt.Format(time.RFC3339),
		}
		if ev.Fields != "" {
			attrs = append(attrs, "fields", ev.Fields)
		}
		if f.SuppressedLogs > 0 {
			attrs = append(attrs, "suppressed", f.SuppressedLogs)
			logger.Warn("live_proxy upstream failure", attrs...)
		} else {
			logger.Warn("live_proxy upstream failure", attrs...)
		}
		f.LastLog = now
		f.SuppressedLogs = 0
		return
	}
	f.SuppressedLogs++
}

// pruneStaleLocked drops failure entries whose cooldown expired more than
// staleEntryTTL ago. The caller must hold s.mu.
func (s *State) pruneStaleLocked(now time.Time) {
	for key, f := range s.failures {
		if now.Sub(f.NextAttempt) > staleEntryTTL {
			delete(s.failures, key)
		}
	}
}

func ReadErrorBody(r io.Reader) string {
	body, _ := io.ReadAll(io.LimitReader(r, errorBodyLimit))
	return string(body)
}

func ReadManifestBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, ManifestBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > ManifestBodyLimit {
		return nil, fmt.Errorf("manifest body exceeds %d bytes", ManifestBodyLimit)
	}
	return body, nil
}
