package main

import (
	"context"
	dbsql "database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/liveproxy"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

type channelRuntime struct {
	ID                     string
	DisplayName            string
	PlaybackMode           db.PlaybackMode
	RequiredPackageProfile string
	ABRLadder              []string
	// PrefillMode is "eager" or "on_demand". On-demand uses ephemeral channel
	// encodings for schedule entries without ready packages.
	PrefillMode string
}

type app struct {
	dbConn *dbsql.DB
	addr   string
	// httpClient is the general-purpose client.
	httpClient        *http.Client
	externalHLSClient *http.Client
	externalHLS       *liveproxy.State
	encodings         *ondemand.Manager
	packagedProfile   string
	// on-demand timing tunables are read once at startup. Keep them explicit on
	// app construction so missing config wiring fails visibly in playback paths.
	onDemandPlaybackLagMs int64
	onDemandWarmupMs      int64
	// cache is the package cache root (CACHE_DIR). Packaged subtitle sidecars
	// live inside each package root; on-demand subtitle renditions are remuxed
	// into the ephemeral channel encoding.
	cache     layout.Cache
	startedAt time.Time

	mu       sync.RWMutex
	channels map[string]*channelRuntime

	// codecCache maps init.mp4 path → HLS CODECS attribute string. Self-synchronized.
	codecCache sync.Map

	// subtitleStreamCache maps mediaID → []packager.SubtitleStreamInfo. Probed
	// once per media so on-demand encoding-spawn paths request identical subtitle
	// options and don't churn the encoding by disagreeing. Self-synchronized.
	subtitleStreamCache sync.Map
}

func (a *app) encodingManagerForChannel(channelID string) *ondemand.Manager {
	return a.encodings
}

func (a *app) onDemandTiming() (int64, int64, error) {
	if a.onDemandPlaybackLagMs <= 0 || a.onDemandWarmupMs <= 0 {
		return 0, 0, fmt.Errorf("on-demand playback timing not configured")
	}
	return a.onDemandPlaybackLagMs, a.onDemandWarmupMs, nil
}

func (a *app) snapshotChannels() []*channelRuntime {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*channelRuntime, 0, len(a.channels))
	for _, c := range a.channels {
		out = append(out, c)
	}
	return out
}

func (a *app) channel(id string) *channelRuntime {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.channels[id]
}

func (a *app) refreshChannels(ctx context.Context) error {
	rows, err := db.EnabledChannels(ctx, a.dbConn)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	// Snapshot the active profile names once so each channel's configured profile
	// (and ABR ladder) can be validated without an extra query per channel. A
	// channel can outlive the profile it names — e.g. a built-in profile renamed
	// in code (h264-main-1080p -> h264-1080p-8mbps) leaves the old channel row
	// dangling, and a dangling required profile silently 503s the encoder with no
	// log. resolveChannelProfile falls back to the default and logs the mismatch.
	validProfiles, err := db.AllPackageProfileNames(ctx, a.dbConn)
	if err != nil {
		return err
	}
	valid := make(map[string]bool, len(validProfiles))
	for _, n := range validProfiles {
		valid[n] = true
	}

	seen := map[string]bool{}
	for _, ch := range rows {
		if ch.UpstreamHLSURL != nil {
			continue
		}
		seen[ch.ID] = true
		profile := a.resolveChannelProfile(ch.ID, packagedProfileForChannel(ch, a.packagedProfile), valid)
		ladder := a.validateLadder(ch.ID, packagedLadderForChannel(ch, profile), valid)
		if existing, ok := a.channels[ch.ID]; ok {
			existing.DisplayName = ch.DisplayName
			existing.PlaybackMode = ch.PlaybackMode
			existing.RequiredPackageProfile = profile
			existing.ABRLadder = ladder
			existing.PrefillMode = ch.PrefillMode
			continue
		}
		a.channels[ch.ID] = &channelRuntime{
			ID:                     ch.ID,
			DisplayName:            ch.DisplayName,
			PlaybackMode:           ch.PlaybackMode,
			RequiredPackageProfile: profile,
			ABRLadder:              ladder,
			PrefillMode:            ch.PrefillMode,
		}
		if ch.PlaybackMode != db.PlaybackModePackaged {
			log.Printf("channel loaded id=%s display=%q playback_mode=%s unsupported_by_playback=true profile=%s",
				ch.ID, ch.DisplayName, ch.PlaybackMode, profile)
			continue
		}
		log.Printf("channel loaded id=%s display=%q playback_mode=%s profile=%s",
			ch.ID, ch.DisplayName, ch.PlaybackMode, profile)
	}
	for id := range a.channels {
		if !seen[id] {
			delete(a.channels, id)
			log.Printf("channel unloaded id=%s", id)
		}
	}
	return nil
}

func packagedProfileForChannel(ch db.Channel, fallback string) string {
	if strings.TrimSpace(ch.RequiredPackageProfile) != "" {
		return strings.TrimSpace(ch.RequiredPackageProfile)
	}
	return fallback
}

func packagedLadderForChannel(ch db.Channel, requiredProfile string) []string {
	b, _ := json.Marshal(ch.ABRLadder)
	return db.NormalizeABRLadder(requiredProfile, string(b))
}

// resolveChannelProfile returns candidate if it is an active package profile,
// otherwise a valid fallback. A channel referencing a profile that no longer
// exists (renamed/deleted built-in, disabled custom profile) would otherwise
// dead-end the encoder at the manifest stage with no recovery; the fallback
// keeps the channel playable while the WARN names the channel and the stale
// profile so the operator can correct the row in admin. valid is the set of
// active profile names; pass nil to look profiles up directly.
//
// The configured default (a.packagedProfile, from the default_packaged_profile
// setting) is not validated on write, so it can dangle too — that is exactly
// how a channel ended up "fixed" onto another missing profile. So the fallback
// is the configured default only when it is itself active, else the canonical
// built-in default, which is always present.
func (a *app) resolveChannelProfile(channelID, candidate string, valid map[string]bool) string {
	if a.profileActive(candidate, valid) {
		return candidate
	}
	fallback := a.packagedProfile
	if !a.profileActive(fallback, valid) {
		log.Printf("default packaged profile %q is not an active profile (renamed/deleted); using built-in %q as the safety fallback",
			fallback, packageprofile.DefaultName)
		fallback = packageprofile.DefaultName
	}
	log.Printf("channel %s references unknown package profile %q (renamed or deleted); falling back to %q — update the channel's profile in admin",
		channelID, candidate, fallback)
	return fallback
}

// profileActive reports whether name is an active (non-disabled) package
// profile. valid is the prefetched name set; pass nil to query directly.
func (a *app) profileActive(name string, valid map[string]bool) bool {
	if valid != nil {
		return valid[name]
	}
	p, _ := db.GetPackageProfile(context.Background(), a.dbConn, name)
	return p != nil
}

// validateLadder drops ABR ladder entries whose profile no longer exists so a
// stale rendition never advertises a variant the packager can't build. The
// required profile is already validated by resolveChannelProfile and stays in
// the ladder (NormalizeABRLadder anchors it first).
func (a *app) validateLadder(channelID string, ladder []string, valid map[string]bool) []string {
	out := ladder[:0:0]
	for _, name := range ladder {
		if valid[name] {
			out = append(out, name)
			continue
		}
		log.Printf("channel %s ABR ladder drops unknown package profile %q", channelID, name)
	}
	return out
}

func (a *app) channelRefreshLoop(ctx context.Context) {
	t := time.NewTicker(channelRefreshPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.refreshChannels(ctx); err != nil {
				log.Printf("channel refresh failed err=%v", err)
			}
		}
	}
}

func (a *app) metricsRefreshLoop(ctx context.Context) {
	a.recordAllMetrics(ctx)
	t := time.NewTicker(channelRefreshPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.recordAllMetrics(ctx)
		}
	}
}

func (a *app) recordAllMetrics(ctx context.Context) {
	for _, ch := range a.snapshotChannels() {
		scheduler.RecordChannelMetrics(ctx, a.dbConn, ch.ID, ch.RequiredPackageProfile)
	}
}
