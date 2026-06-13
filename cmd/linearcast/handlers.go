package main

import (
	"context"
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

func (a *app) lookupChannelOr404(ctx context.Context, w http.ResponseWriter, channelID string) *channelRuntime {
	if channelID == "" {
		http.NotFound(w, nil)
		return nil
	}
	if rt := a.channel(channelID); rt != nil {
		return rt
	}
	row, err := db.ChannelByID(ctx, a.dbConn, channelID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return nil
	}
	if row == nil || !row.Enabled {
		http.NotFound(w, nil)
		return nil
	}
	if row.UpstreamHLSURL != nil {
		http.NotFound(w, nil)
		return nil
	}
	rt := &channelRuntime{
		ID:                     row.ID,
		DisplayName:            row.DisplayName,
		PlaybackMode:           row.PlaybackMode,
		RequiredPackageProfile: packagedProfileForChannel(*row, a.packagedProfile),
		PrefillMode:            row.PrefillMode,
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.channels == nil {
		a.channels = map[string]*channelRuntime{}
	}
	if existing := a.channels[channelID]; existing != nil {
		return existing
	}
	a.channels[channelID] = rt
	return rt
}
