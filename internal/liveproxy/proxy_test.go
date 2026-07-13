package liveproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStateCoolingDownAfterFailure(t *testing.T) {
	s := NewState()
	s.RecordFailure(failureEvent{
		Key:        "key",
		Source:     "test",
		Upstream:   "http://upstream.test",
		Err:        errors.New("offline"),
		Cooldown:   time.Minute,
		SampleEach: time.Hour,
	})
	retryAt, ok := s.CoolingDown("key", time.Now())
	if !ok {
		t.Fatalf("cooldown missing")
	}
	if time.Until(retryAt) <= 0 {
		t.Fatalf("retryAt=%s, want future", retryAt)
	}
	s.RecordSuccess("key")
	if _, ok := s.CoolingDown("key", time.Now()); ok {
		t.Fatalf("cooldown remained after success")
	}
}

func TestRecordFailurePrunesStaleEntries(t *testing.T) {
	s := NewState()
	recordFailure := func(key string) {
		s.RecordFailure(failureEvent{
			Key:        key,
			Source:     "test",
			Upstream:   "http://upstream.test",
			Err:        errors.New("offline"),
			Cooldown:   time.Minute,
			SampleEach: time.Hour,
		})
	}

	// An ended viewer session whose last request failed: its cooldown lapsed
	// well past the TTL and no later request re-armed it.
	recordFailure("ended-session")
	s.mu.Lock()
	s.failures["ended-session"].NextAttempt = time.Now().Add(-staleEntryTTL - time.Minute)
	s.mu.Unlock()

	// A fresh failure on a different key triggers the opportunistic prune.
	recordFailure("active-session")

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.failures["ended-session"]; ok {
		t.Fatalf("stale failure entry was not pruned")
	}
	if _, ok := s.failures["active-session"]; !ok {
		t.Fatalf("active failure entry was pruned; cooldown still in effect")
	}
}

func TestProxyLimitsUpstreamErrorBodyAndRecordsCooldown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 1024), http.StatusBadGateway)
	}))
	defer upstream.Close()

	state := NewState()
	p := Proxy{Client: upstream.Client(), State: state, Cooldown: time.Minute}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	p.Serve(res, req, Request{Key: "key", Upstream: upstream.URL})

	if res.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if got := strings.Count(res.Body.String(), "x"); got != errorBodyLimit {
		t.Fatalf("error body copied %d bytes, want %d", got, errorBodyLimit)
	}
	if _, ok := state.CoolingDown("key", time.Now()); !ok {
		t.Fatalf("cooldown missing after upstream status failure")
	}
}

func TestReadManifestBodyRejectsOversizedManifest(t *testing.T) {
	if _, err := ReadManifestBody(strings.NewReader(strings.Repeat("x", ManifestBodyLimit+1))); err == nil {
		t.Fatalf("oversized manifest was accepted")
	}
	body, err := ReadManifestBody(strings.NewReader("#EXTM3U\n"))
	if err != nil {
		t.Fatalf("small manifest rejected: %v", err)
	}
	if string(body) != "#EXTM3U\n" {
		t.Fatalf("body=%q", string(body))
	}
}

// TestProxyBlockedAddressIsTerminal verifies that an SSRF-blocked dial fails
// visibly (502) without arming the transient cooldown: a blocked address is a
// configuration error that will not recover, so a later 503 cooldown response
// would only hide the cause.
func TestProxyBlockedAddressIsTerminal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	state := NewState()
	p := Proxy{
		Client:    NewGuardedClient(2*time.Second, DenyPrivateNetworks),
		State:     state,
		Cooldown:  time.Minute,
		LogPrefix: "external hls",
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	p.Serve(res, req, Request{Key: "key", Upstream: upstream.URL, LogFields: "channel_id=ch1"})

	if res.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s, want 502", res.Code, res.Body.String())
	}
	if res.Header().Get("Retry-After") != "" {
		t.Fatalf("Retry-After set on terminal block: %q", res.Header().Get("Retry-After"))
	}
	if _, ok := state.CoolingDown("key", time.Now()); ok {
		t.Fatalf("blocked address armed a transient cooldown, want terminal failure")
	}
}

func TestProxyTimeoutCooldownAndRecovery(t *testing.T) {
	var requests atomic.Int32
	var healthy atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if healthy.Load() {
			_, _ = w.Write([]byte("ok"))
			return
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	state := NewState()
	p := Proxy{
		Client:   upstream.Client(),
		State:    state,
		Timeout:  10 * time.Millisecond,
		Cooldown: 25 * time.Millisecond,
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	p.Serve(res, req, Request{Key: "key", Upstream: upstream.URL})
	if res.Code != http.StatusBadGateway {
		t.Fatalf("timeout status=%d body=%s, want 502", res.Code, res.Body.String())
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests=%d, want first upstream request", got)
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/proxy", nil)
	p.Serve(res, req, Request{Key: "key", Upstream: upstream.URL})
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("cooldown status=%d body=%s, want 503", res.Code, res.Body.String())
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests=%d, want no upstream request during cooldown", got)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Fatalf("Retry-After missing")
	}

	healthy.Store(true)
	time.Sleep(35 * time.Millisecond)
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/proxy", nil)
	p.Serve(res, req, Request{Key: "key", Upstream: upstream.URL})
	if res.Code != http.StatusOK {
		t.Fatalf("recovery status=%d body=%s, want 200", res.Code, res.Body.String())
	}
	if res.Body.String() != "ok" {
		t.Fatalf("recovery body=%q", res.Body.String())
	}
	if _, ok := state.CoolingDown("key", time.Now()); ok {
		t.Fatalf("cooldown remained after successful recovery")
	}
}

func TestProxyClientCancelDoesNotRecordCooldown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()

	state := NewState()
	p := Proxy{
		Client:   upstream.Client(),
		State:    state,
		Timeout:  time.Second,
		Cooldown: time.Minute,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/proxy", nil).WithContext(ctx)
	p.Serve(res, req, Request{Key: "key", Upstream: upstream.URL})

	if _, ok := state.CoolingDown("key", time.Now()); ok {
		t.Fatalf("client-canceled request armed upstream cooldown")
	}
}
