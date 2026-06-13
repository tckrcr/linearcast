package main

import (
	"context"
	dbsql "database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/plexrelay"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

type channelRuntime struct {
	ID                     string
	DisplayName            string
	PlaybackMode           db.PlaybackMode
	RequiredPackageProfile string
	ABRLadder              []string
	// PrefillMode is "eager" or "on_demand". On-demand channels use ephemeral
	// live sessions for schedule entries without ready packages.
	PrefillMode string
}

type app struct {
	dbConn          *dbsql.DB
	addr            string
	httpClient      *http.Client
	externalHLS     *externalHLSProxyState
	sessions        *ondemand.Manager
	plexRelay       *plexrelay.Manager
	packagedProfile string
	startedAt       time.Time

	mu       sync.RWMutex
	channels map[string]*channelRuntime

	// codecCache maps init.mp4 path → HLS CODECS attribute string. Self-synchronized.
	codecCache sync.Map
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

	seen := map[string]bool{}
	for _, ch := range rows {
		if ch.UpstreamHLSURL != nil {
			continue
		}
		seen[ch.ID] = true
		profile := packagedProfileForChannel(ch, a.packagedProfile)
		if existing, ok := a.channels[ch.ID]; ok {
			existing.DisplayName = ch.DisplayName
			existing.PlaybackMode = ch.PlaybackMode
			existing.RequiredPackageProfile = profile
			existing.ABRLadder = packagedLadderForChannel(ch, profile)
			existing.PrefillMode = ch.PrefillMode
			continue
		}
		a.channels[ch.ID] = &channelRuntime{
			ID:                     ch.ID,
			DisplayName:            ch.DisplayName,
			PlaybackMode:           ch.PlaybackMode,
			RequiredPackageProfile: profile,
			ABRLadder:              packagedLadderForChannel(ch, profile),
			PrefillMode:            ch.PrefillMode,
		}
		if ch.PlaybackMode == db.PlaybackModePlexRelay {
			log.Printf("channel loaded id=%s display=%q playback_mode=%s",
				ch.ID, ch.DisplayName, ch.PlaybackMode)
			continue
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
