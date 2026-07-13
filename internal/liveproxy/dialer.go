package liveproxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

const (
	guardedMaxIdleConns        = 100
	guardedMaxIdleConnsPerHost = 8
	guardedMaxConnsPerHost     = 32
)

// BlockedAddressError reports that a dial was refused by the adapter's SSRF
// policy because the resolved address is not permitted. It is a terminal
// condition, not a transient upstream flap: the configured upstream resolves
// somewhere the proxy must not reach, so callers fail visibly and do not arm
// the retry cooldown (a blocked address will never "recover").
type BlockedAddressError struct {
	IP     net.IP
	Reason string
}

func (e *BlockedAddressError) Error() string {
	if e.IP == nil {
		return fmt.Sprintf("blocked upstream address: %s", e.Reason)
	}
	return fmt.Sprintf("blocked upstream address %s: %s", e.IP, e.Reason)
}

// IsBlockedAddress reports whether err is, or wraps, a BlockedAddressError.
// The dial error surfaces wrapped in *net.OpError and *url.Error, both of which
// implement Unwrap, so errors.As finds it through the whole chain.
func IsBlockedAddress(err error) bool {
	var b *BlockedAddressError
	return errors.As(err, &b)
}

// IPPolicy decides whether a dial to the resolved IP is allowed. A non-nil
// return blocks the connection and should describe why.
type IPPolicy func(ip net.IP) error

// DenyPrivateNetworks blocks loopback, link-local, private, unique-local,
// multicast, unspecified, and other non-global-unicast addresses. It is the
// default external-HLS policy: a scheduler-supplied upstream URL must not be
// usable to reach the host's own loopback services, the cloud metadata
// endpoint (169.254.169.254), or other hosts on the private network.
func DenyPrivateNetworks(ip net.IP) error {
	switch {
	case ip == nil:
		return &BlockedAddressError{Reason: "nil address"}
	case ip.IsLoopback():
		return &BlockedAddressError{IP: ip, Reason: "loopback"}
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return &BlockedAddressError{IP: ip, Reason: "link-local"}
	case ip.IsPrivate():
		return &BlockedAddressError{IP: ip, Reason: "private"}
	case ip.IsMulticast():
		return &BlockedAddressError{IP: ip, Reason: "multicast"}
	case ip.IsUnspecified():
		return &BlockedAddressError{IP: ip, Reason: "unspecified"}
	case !ip.IsGlobalUnicast():
		// Catches the IPv4 broadcast 255.255.255.255 and similar reserved
		// addresses that are none of the specific cases above.
		return &BlockedAddressError{IP: ip, Reason: "non-global"}
	}
	return nil
}

// AllowAllAddresses permits any resolved address. It is the permissive policy
// for adapters whose upstream is operator-configured (commonly on loopback or
// the LAN), where a deny-private policy would break a legitimate local server.
// The upstream host is operator-configured (not request-influenced), so it is
// trusted. It still runs through the shared guarded client so timeout and
// cooldown behavior stay uniform across adapters.
func AllowAllAddresses(net.IP) error { return nil }

// guardedControl returns a net.Dialer Control hook that applies policy to the
// resolved address of every connection, including each redirect hop, since
// http.Client.Do dials through the same transport per hop. The address passed
// to Control is the post-DNS-resolution ip:port, so this also defeats DNS
// rebinding: the policy sees the IP actually being connected to, not whatever
// the configured URL parsed to.
func guardedControl(policy IPPolicy) func(network, address string, c syscall.RawConn) error {
	return func(_ string, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("split dial address %q: %w", address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return &BlockedAddressError{Reason: fmt.Sprintf("unresolved host %q", host)}
		}
		return policy(ip)
	}
}

// NewGuardedClient builds an *http.Client whose dialer enforces policy on the
// resolved IP of every connection and redirect hop. timeout bounds the whole
// request (per-request context timeouts on the proxy are the tighter limit).
func NewGuardedClient(timeout time.Duration, policy IPPolicy) *http.Client {
	if policy == nil {
		policy = AllowAllAddresses
	}
	d := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   guardedControl(policy),
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           d.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          guardedMaxIdleConns,
			MaxIdleConnsPerHost:   guardedMaxIdleConnsPerHost,
			MaxConnsPerHost:       guardedMaxConnsPerHost,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}
