package admin

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

// External-channel health values surfaced in /api/status, /api/guide, and
// /api/playable-sources. These replace the former hardcoded "live", which lied
// whenever the upstream was unreachable.
const (
	externalStatusLive    = "live"
	externalStatusDown    = "down"
	externalStatusUnknown = "unknown"
)

const (
	// A live result is trusted for this long before the upstream is re-probed.
	externalHeartbeatSuccessTTL = 30 * time.Second
	// A down upstream backs off so a hard-down stream is not re-probed on every
	// guide poll. Doubles per consecutive failure up to the max.
	externalHeartbeatFailureBase = 5 * time.Second
	externalHeartbeatFailureMax  = 60 * time.Second
	// Drop cache entries not refreshed within this window. A URL change orphans
	// the old (channelID|url) key; this keeps the map from accreting them.
	externalHeartbeatStaleTTL = 10 * time.Minute
)

// externalHeartbeatCache is a read-through reachability cache for external/live
// channel upstreams, keyed by "channelID|url". It mirrors upstreamStatusCache:
// success has a short TTL, failures back off, and the probe runs in the request
// path on a cache miss (consistent with fetchUpstreamStatus / the per-request
// now-playing fetch already done for these channels).
type externalHeartbeatCache struct {
	mu      sync.Mutex
	entries map[string]*externalHeartbeatEntry
}

type externalHeartbeatEntry struct {
	status      string
	checkedAt   time.Time
	nextAttempt time.Time
	failures    int
}

func newExternalHeartbeatCache() *externalHeartbeatCache {
	return &externalHeartbeatCache{entries: map[string]*externalHeartbeatEntry{}}
}

// externalChannelStatus reports an external channel's upstream health as
// "live", "down", or "unknown". On a cache miss (or once the cached result is
// stale / its failure backoff has elapsed) it probes the upstream synchronously
// through a.httpClient — the same client fetchExternalNowPlaying already uses
// against this host — and caches the verdict.
func (a *App) externalChannelStatus(ctx context.Context, ch db.Channel) string {
	if ch.UpstreamHLSURL == nil {
		return externalStatusUnknown
	}
	rawURL := strings.TrimSpace(*ch.UpstreamHLSURL)
	if !validUpstreamHLSURL(rawURL) {
		return externalStatusDown
	}

	cache := a.externalHeartbeat
	key := ch.ID + "|" + rawURL
	now := a.now().UTC()

	cache.mu.Lock()
	if e := cache.entries[key]; e != nil {
		if e.status == externalStatusLive && now.Sub(e.checkedAt) < externalHeartbeatSuccessTTL {
			cache.mu.Unlock()
			return e.status
		}
		if e.status != externalStatusLive && now.Before(e.nextAttempt) {
			cache.mu.Unlock()
			return e.status
		}
	}
	cache.mu.Unlock()

	// Probe outside the lock so a slow upstream does not block other readers.
	status := externalStatusForProbe(probeUpstreamHLSWith(ctx, a.httpClient, rawURL))

	cache.mu.Lock()
	defer cache.mu.Unlock()
	e := cache.entries[key]
	if e == nil {
		e = &externalHeartbeatEntry{}
		cache.entries[key] = e
	}
	e.status = status
	e.checkedAt = now
	if status == externalStatusLive {
		e.failures = 0
		e.nextAttempt = time.Time{}
	} else {
		e.failures++
		e.nextAttempt = now.Add(externalHeartbeatBackoff(e.failures))
	}
	cache.pruneStaleLocked(now)
	return status
}

// externalStatusForProbe maps a reachability probe to a health value. A
// reachable 2xx/3xx upstream is live; anything else is down. looksLikeHls stays
// advisory — the proxy serves whatever the URL returns, so a non-HLS-shaped but
// reachable response should not read as down.
func externalStatusForProbe(r probeUpstreamResponse) string {
	if !r.Reachable {
		return externalStatusDown
	}
	status := r.Status
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status >= 400 {
		return externalStatusDown
	}
	return externalStatusLive
}

func externalHeartbeatBackoff(failures int) time.Duration {
	d := externalHeartbeatFailureBase
	for i := 1; i < failures && d < externalHeartbeatFailureMax; i++ {
		d *= 2
	}
	if d > externalHeartbeatFailureMax {
		d = externalHeartbeatFailureMax
	}
	return d
}

// pruneStaleLocked drops entries not refreshed within externalHeartbeatStaleTTL.
// The caller must hold c.mu.
func (c *externalHeartbeatCache) pruneStaleLocked(now time.Time) {
	for k, e := range c.entries {
		if now.Sub(e.checkedAt) > externalHeartbeatStaleTTL {
			delete(c.entries, k)
		}
	}
}
