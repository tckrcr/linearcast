package playback

import (
	"context"
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

func (runtime *Runtime) handleOnDemandRestart(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	if runtime.encodings != nil {
		runtime.encodings.RestartChannel(channelID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// StopOnDemandEncoding implements Controller for protected admin controls.
// Unlike a viewer-requested restart, an operator stop gates new admission
// briefly so the encoder does not immediately respawn.
func (runtime *Runtime) StopOnDemandEncoding(channelID string) {
	if runtime.encodings != nil {
		runtime.encodings.KillChannel(channelID)
	}
}

func (runtime *Runtime) lookupChannelOr404(ctx context.Context, w http.ResponseWriter, channelID string) *channelRuntime {
	if channelID == "" {
		http.NotFound(w, nil)
		return nil
	}
	if rt := runtime.channel(channelID); rt != nil {
		return rt
	}
	row, err := db.ChannelByID(ctx, runtime.dbConn, channelID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return nil
	}
	if row == nil || !row.Enabled {
		http.NotFound(w, nil)
		return nil
	}
	rt := &channelRuntime{
		ID:                     row.ID,
		DisplayName:            row.DisplayName,
		RequiredPackageProfile: runtime.resolveChannelProfile(row.ID, packagedProfileForChannel(*row, runtime.packagedProfile), nil),
		PrefillMode:            row.PrefillMode,
	}
	runtime.mu.Lock()
	if runtime.channels == nil {
		runtime.channels = map[string]*channelRuntime{}
	}
	if existing := runtime.channels[channelID]; existing != nil {
		snapshot := cloneChannel(existing)
		runtime.mu.Unlock()
		return snapshot
	}
	runtime.channels[channelID] = cloneChannel(rt)
	runtime.mu.Unlock()
	return rt
}
