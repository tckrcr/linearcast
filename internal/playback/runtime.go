package playback

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

const (
	lookaheadMs           int64 = 3 * 60 * 1000
	manifestAheadMs       int64 = 72 * 1000
	packagedManifestLimit       = 24
	streamPath                  = "streams"
	encodingPath                = "encoding"
	onDemandSubtitlePath        = "subs-channel-encoding"
	channelRefreshPeriod        = 60 * time.Second
)

// Config contains the process-owned dependencies and startup settings needed
// by the playback runtime.
type Config struct {
	DB                    *sql.DB
	Encodings             *ondemand.Manager
	PackagedProfile       string
	OnDemandPlaybackLagMs int64
	OnDemandWarmupMs      int64
	Cache                 layout.Cache
	StartedAt             time.Time
}

// ChannelSnapshot is the playback state needed by process-level observers.
// It deliberately omits mutable channel internals.
type ChannelSnapshot struct {
	ID                     string
	RequiredPackageProfile string
}

type channelRuntime struct {
	ID                     string
	DisplayName            string
	RequiredPackageProfile string
	ABRLadder              []string
	// PrefillMode is "eager" or "on_demand". On-demand uses ephemeral channel
	// encodings for schedule entries without ready packages.
	PrefillMode string
}

// Runtime owns all in-process channel playback state and behavior.
type Runtime struct {
	dbConn          *sql.DB
	encodings       *ondemand.Manager
	packagedProfile string
	// on-demand timing tunables are read once at startup. Keep them explicit on
	// Runtime construction so missing config wiring fails visibly in playback paths.
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

// NewRuntime constructs the complete playback runtime and loads its initial
// enabled-channel snapshot.
func NewRuntime(ctx context.Context, cfg Config) (*Runtime, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("playback database is required")
	}
	if cfg.Encodings == nil {
		return nil, fmt.Errorf("playback on-demand encoding manager is required")
	}
	if cfg.OnDemandPlaybackLagMs <= 0 || cfg.OnDemandWarmupMs <= 0 {
		return nil, fmt.Errorf("on-demand playback timing must be positive")
	}
	cfg.PackagedProfile = strings.TrimSpace(cfg.PackagedProfile)
	if cfg.PackagedProfile == "" {
		cfg.PackagedProfile = db.DefaultPackageProfile
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now().UTC()
	}

	runtime := &Runtime{
		dbConn:                cfg.DB,
		encodings:             cfg.Encodings,
		packagedProfile:       cfg.PackagedProfile,
		onDemandPlaybackLagMs: cfg.OnDemandPlaybackLagMs,
		onDemandWarmupMs:      cfg.OnDemandWarmupMs,
		cache:                 cfg.Cache,
		startedAt:             cfg.StartedAt,
		channels:              make(map[string]*channelRuntime),
	}
	if err := runtime.refreshChannels(ctx); err != nil {
		return nil, fmt.Errorf("load playback channels: %w", err)
	}
	return runtime, nil
}

func (runtime *Runtime) encodingManagerForChannel(channelID string) *ondemand.Manager {
	return runtime.encodings
}

func (runtime *Runtime) onDemandTiming() (int64, int64, error) {
	if runtime.onDemandPlaybackLagMs <= 0 || runtime.onDemandWarmupMs <= 0 {
		return 0, 0, fmt.Errorf("on-demand playback timing not configured")
	}
	return runtime.onDemandPlaybackLagMs, runtime.onDemandWarmupMs, nil
}

func (runtime *Runtime) snapshotChannels() []*channelRuntime {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	out := make([]*channelRuntime, 0, len(runtime.channels))
	for _, c := range runtime.channels {
		out = append(out, cloneChannel(c))
	}
	return out
}

func (runtime *Runtime) channel(id string) *channelRuntime {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	channel := runtime.channels[id]
	if channel == nil {
		return nil
	}
	return cloneChannel(channel)
}

func cloneChannel(channel *channelRuntime) *channelRuntime {
	snapshot := *channel
	snapshot.ABRLadder = append([]string(nil), channel.ABRLadder...)
	return &snapshot
}

// ChannelSnapshots returns the immutable channel fields consumed by metrics
// and other process-level observers.
func (runtime *Runtime) ChannelSnapshots() []ChannelSnapshot {
	channels := runtime.snapshotChannels()
	out := make([]ChannelSnapshot, 0, len(channels))
	for _, channel := range channels {
		out = append(out, ChannelSnapshot{
			ID:                     channel.ID,
			RequiredPackageProfile: channel.RequiredPackageProfile,
		})
	}
	return out
}

func (runtime *Runtime) refreshChannels(ctx context.Context) error {
	rows, err := db.EnabledChannels(ctx, runtime.dbConn)
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	// Snapshot the active profile names once so each channel's configured profile
	// (and ABR ladder) can be validated without an extra query per channel. A
	// channel can outlive the profile it names — e.g. a built-in profile renamed
	// in code (h264-main-1080p -> h264-1080p-8mbps) leaves the old channel row
	// dangling, and a dangling required profile silently 503s the encoder with no
	// log. resolveChannelProfile falls back to the default and logs the mismatch.
	validProfiles, err := db.AllPackageProfileNames(ctx, runtime.dbConn)
	if err != nil {
		return err
	}
	valid := make(map[string]bool, len(validProfiles))
	for _, n := range validProfiles {
		valid[n] = true
	}

	seen := map[string]bool{}
	for _, ch := range rows {
		seen[ch.ID] = true
		profile := runtime.resolveChannelProfile(ch.ID, packagedProfileForChannel(ch, runtime.packagedProfile), valid)
		ladder := runtime.validateLadder(ch.ID, packagedLadderForChannel(ch, profile), valid)
		if existing, ok := runtime.channels[ch.ID]; ok {
			existing.DisplayName = ch.DisplayName
			existing.RequiredPackageProfile = profile
			existing.ABRLadder = ladder
			existing.PrefillMode = ch.PrefillMode
			continue
		}
		runtime.channels[ch.ID] = &channelRuntime{
			ID:                     ch.ID,
			DisplayName:            ch.DisplayName,
			RequiredPackageProfile: profile,
			ABRLadder:              ladder,
			PrefillMode:            ch.PrefillMode,
		}
		log.Printf("channel loaded id=%s display=%q profile=%s", ch.ID, ch.DisplayName, profile)
	}
	for id := range runtime.channels {
		if !seen[id] {
			delete(runtime.channels, id)
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
// The configured default (from the default_packaged_profile setting) is not
// validated on write, so it can dangle too — that is exactly
// how a channel ended up "fixed" onto another missing profile. So the fallback
// is the configured default only when it is itself active, else the canonical
// built-in default, which is always present.
func (runtime *Runtime) resolveChannelProfile(channelID, candidate string, valid map[string]bool) string {
	if runtime.profileActive(candidate, valid) {
		return candidate
	}
	fallback := runtime.packagedProfile
	if !runtime.profileActive(fallback, valid) {
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
func (runtime *Runtime) profileActive(name string, valid map[string]bool) bool {
	if valid != nil {
		return valid[name]
	}
	p, _ := db.GetPackageProfile(context.Background(), runtime.dbConn, name)
	return p != nil
}

// validateLadder drops ABR ladder entries whose profile no longer exists so a
// stale rendition never advertises a variant the packager can't build. The
// required profile is already validated by resolveChannelProfile and stays in
// the ladder (NormalizeABRLadder anchors it first).
func (runtime *Runtime) validateLadder(channelID string, ladder []string, valid map[string]bool) []string {
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

func (runtime *Runtime) channelRefreshLoop(ctx context.Context) {
	t := time.NewTicker(channelRefreshPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := runtime.refreshChannels(ctx); err != nil {
				log.Printf("channel refresh failed err=%v", err)
			}
		}
	}
}

// Run refreshes playback channel state until ctx is canceled.
func (runtime *Runtime) Run(ctx context.Context) {
	runtime.channelRefreshLoop(ctx)
}
