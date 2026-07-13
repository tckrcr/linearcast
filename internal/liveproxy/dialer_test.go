package liveproxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDenyPrivateNetworks(t *testing.T) {
	denied := []string{
		"127.0.0.1",       // loopback v4
		"::1",             // loopback v6
		"169.254.169.254", // link-local (cloud metadata)
		"fe80::1",         // link-local v6
		"10.0.0.5",        // private
		"172.16.3.4",      // private
		"192.168.1.10",    // private
		"fd00::1",         // unique-local v6
		"224.0.0.1",       // multicast
		"0.0.0.0",         // unspecified
		"255.255.255.255", // broadcast / non-global
	}
	for _, s := range denied {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if err := DenyPrivateNetworks(ip); err == nil {
			t.Errorf("DenyPrivateNetworks(%s) = nil, want blocked", s)
		} else if !IsBlockedAddress(err) {
			t.Errorf("DenyPrivateNetworks(%s) err=%v, want BlockedAddressError", s, err)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if err := DenyPrivateNetworks(ip); err != nil {
			t.Errorf("DenyPrivateNetworks(%s) = %v, want allowed", s, err)
		}
	}
}

func TestAllowAllAddresses(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "8.8.8.8", "::1"} {
		if err := AllowAllAddresses(net.ParseIP(s)); err != nil {
			t.Errorf("AllowAllAddresses(%s) = %v, want nil", s, err)
		}
	}
}

func TestGuardedClientBlocksLoopback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	client := NewGuardedClient(2*time.Second, DenyPrivateNetworks)
	resp, err := client.Get(upstream.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected blocked dial to loopback upstream, got status %d", resp.StatusCode)
	}
	if !IsBlockedAddress(err) {
		t.Fatalf("err=%v, want BlockedAddressError", err)
	}
}

func TestGuardedClientAllowsWhenPolicyPermits(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	client := NewGuardedClient(2*time.Second, AllowAllAddresses)
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("allow-all client failed to reach loopback upstream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
}

func TestGuardedClientTransportHasConnectionBounds(t *testing.T) {
	client := NewGuardedClient(2*time.Second, AllowAllAddresses)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport=%T, want *http.Transport", client.Transport)
	}
	if transport.MaxIdleConns != guardedMaxIdleConns {
		t.Fatalf("MaxIdleConns=%d, want %d", transport.MaxIdleConns, guardedMaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != guardedMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost=%d, want %d", transport.MaxIdleConnsPerHost, guardedMaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost != guardedMaxConnsPerHost {
		t.Fatalf("MaxConnsPerHost=%d, want %d", transport.MaxConnsPerHost, guardedMaxConnsPerHost)
	}
}

// TestGuardedClientEnforcesPerRedirectHop proves the policy runs at dial time on
// every hop, not just the configured URL: the first dial is allowed and the
// redirect target's dial is blocked, so a redirect into a forbidden address
// fails even though the initial request was permitted.
func TestGuardedClientEnforcesPerRedirectHop(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	var dials atomic.Int32
	// Allow the first dial (the redirector) and block every dial after it (the
	// redirect target). Both servers are on loopback, so the hop count, not the
	// IP — is what this exercises.
	policy := func(net.IP) error {
		if dials.Add(1) > 1 {
			return &BlockedAddressError{Reason: "redirect hop blocked by test policy"}
		}
		return nil
	}
	client := NewGuardedClient(2*time.Second, policy)
	resp, err := client.Get(redirector.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected redirect hop to be blocked, got status %d", resp.StatusCode)
	}
	if !IsBlockedAddress(err) {
		t.Fatalf("err=%v, want BlockedAddressError on redirect hop", err)
	}
	if got := dials.Load(); got < 2 {
		t.Fatalf("policy invoked %d times, want >=2 (per-hop enforcement)", got)
	}
}
