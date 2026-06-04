package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func channelIDSlug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "channel"
	}
	return out
}

type channelListRow struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	Enabled         bool   `json:"enabled"`
	Ordering        string `json:"ordering"`
	HiddenFromGuide bool   `json:"hiddenFromGuide"`
	ArtworkURL      string `json:"artworkUrl,omitempty"`
	MediaKind       string `json:"mediaKind"`
}

type channelPolicyResponse struct {
	ChannelID              string `json:"channelId"`
	PlaybackMode           string `json:"playbackMode"`
	RequiredPackageProfile string `json:"requiredPackageProfile"`
	PackagePrefillMs       *int64 `json:"packagePrefillMs"`
	MediaKind              string `json:"mediaKind"`
}

type channelPolicyUpdateRequest struct {
	RequiredPackageProfile *string `json:"requiredPackageProfile"`
	PackagePrefillMs       *int64  `json:"packagePrefillMs"`
	MediaKind              *string `json:"mediaKind"`
	Force                  bool    `json:"force"`
}

type channelExtendRequest struct {
	Hours int `json:"hours"`
}

type channelCloneResponse struct {
	SourceChannelID string `json:"sourceChannelID"`
	ChannelID       string `json:"channelID"`
	DisplayName     string `json:"displayName"`
	Enabled         bool   `json:"enabled"`
	MediaCount      int    `json:"mediaCount"`
}

type createChannelRequest struct {
	DisplayName    string   `json:"displayName"`
	PackageProfile string   `json:"packageProfile"`
	MediaIDs       []string `json:"mediaIds"`
	Ordering       string   `json:"ordering,omitempty"`
	UpstreamHLSURL string   `json:"upstreamHlsUrl,omitempty"`
}

type createChannelResponse struct {
	ChannelID       string `json:"channelID"`
	DisplayName     string `json:"displayName"`
	Created         bool   `json:"created"`
	SyncedMedia     int    `json:"syncedMedia"`
	ScheduleEntries int    `json:"scheduleEntries"`
}

type adminHTTPError struct {
	Status  int
	Code    string
	Message string
}

func (a *App) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req createChannelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	resp, herr := a.createChannel(r.Context(), req)
	if herr != nil {
		writeError(w, herr.Status, herr.Code, herr.Message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusCreated, resp)
}

func (a *App) createChannel(ctx context.Context, req createChannelRequest) (createChannelResponse, *adminHTTPError) {
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.PackageProfile = strings.TrimSpace(req.PackageProfile)
	req.Ordering = strings.TrimSpace(req.Ordering)
	req.UpstreamHLSURL = strings.TrimSpace(req.UpstreamHLSURL)

	if req.DisplayName == "" {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "missing_display_name", Message: "displayName is required"}
	}
	if req.Ordering == "" {
		req.Ordering = "alphabetical"
	}

	// Auto-generate a unique channel ID from the display name.
	base := channelIDSlug(req.DisplayName)
	channelID := ""
	for i := 0; ; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		existing, err := db.ChannelByID(ctx, a.dbConn, candidate)
		if err != nil {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: err.Error()}
		}
		if existing == nil {
			channelID = candidate
			break
		}
	}

	if req.UpstreamHLSURL != "" {
		if !validUpstreamHLSURL(req.UpstreamHLSURL) {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_upstream_hls_url", Message: "upstreamHlsUrl must be an http or https URL"}
		}
		if err := db.InsertChannel(ctx, a.dbConn, db.ChannelWrite{
			ID:             channelID,
			DisplayName:    req.DisplayName,
			Ordering:       req.Ordering,
			MediaKind:      db.MediaKindMusic,
			UpstreamHLSURL: &req.UpstreamHLSURL,
		}); err != nil {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("create channel: %v", err)}
		}
		return createChannelResponse{ChannelID: channelID, DisplayName: req.DisplayName, Created: true}, nil
	}

	if req.PackageProfile == "" {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "missing_profile", Message: "packageProfile is required"}
	}
	if len(req.MediaIDs) == 0 {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "empty_media", Message: "at least one mediaId is required"}
	}

	// Validate profile and derive media kind from it.
	profileRecord, err := db.PackageProfileByName(ctx, a.dbConn, req.PackageProfile)
	if err != nil {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: err.Error()}
	}
	if profileRecord == nil || profileRecord.Disabled {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_profile", Message: fmt.Sprintf("package profile %q is not available", req.PackageProfile)}
	}
	mediaKind := db.NormalizeMediaKind(db.MediaKind(profileRecord.Profile.MediaKind))

	// Resolve media IDs to eligible rows (already ingested, pass codec/kind check).
	nowMs := a.now().UTC().UnixMilli()
	mediaMap, err := db.MediaByIDs(ctx, a.dbConn, req.MediaIDs)
	if err != nil {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: err.Error()}
	}
	members := make([]db.ChannelMediaRow, 0, len(req.MediaIDs))
	seen := make(map[string]bool)
	for _, id := range req.MediaIDs {
		m, ok := mediaMap[id]
		if !ok || seen[m.ID] {
			continue
		}
		if !m.CodecCheckPassed && db.NormalizeMediaKind(m.MediaKind) != db.MediaKindMusic {
			continue
		}
		seen[m.ID] = true
		members = append(members, db.ChannelMediaRow{
			ChannelID: channelID,
			MediaID:   m.ID,
			AddedAtMs: nowMs,
		})
	}
	if len(members) == 0 {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusUnprocessableEntity, Code: "no_eligible_media", Message: "none of the supplied mediaIds are eligible (not ingested or failed codec check)"}
	}

	// Create the channel.
	if err := db.InsertChannel(ctx, a.dbConn, db.ChannelWrite{
		ID:                     channelID,
		DisplayName:            req.DisplayName,
		Ordering:               req.Ordering,
		RequiredPackageProfile: req.PackageProfile,
		MediaKind:              mediaKind,
	}); err != nil {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("create channel: %v", err)}
	}

	// Write channel_media membership.
	if err := db.WithTx(ctx, a.dbConn, func(tx db.Execer) error {
		return db.ReplaceChannelMedia(ctx, tx, channelID, members)
	}); err != nil {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("write channel_media: %v", err)}
	}

	// Best-effort schedule extension — no ready packages yet is fine.
	scheduleEntries := 0
	if sched, err := scheduler.ExtendChannel(ctx, a.dbConn, channelID, scheduler.ServiceOptions{HorizonHours: 24}); err == nil {
		scheduleEntries = sched.Inserted
	}

	return createChannelResponse{
		ChannelID:       channelID,
		DisplayName:     req.DisplayName,
		Created:         true,
		SyncedMedia:     len(members),
		ScheduleEntries: scheduleEntries,
	}, nil
}

func (a *App) handleChannelList(w http.ResponseWriter, r *http.Request) {
	channels, err := db.AllChannelsOrderedByDisplayName(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]channelListRow, 0, len(channels))
	for _, c := range channels {
		out = append(out, channelListRow{
			ID:              c.ID,
			DisplayName:     c.DisplayName,
			Ordering:        c.Ordering,
			Enabled:         c.Enabled,
			HiddenFromGuide: c.HiddenFromGuide,
			ArtworkURL:      c.ArtworkURL,
			MediaKind:       string(db.NormalizeMediaKind(c.MediaKind)),
		})
	}
	writeJSON(w, map[string]any{"channels": out})
}

func (a *App) setChannelEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	channelID := r.PathValue("channelID")
	existing, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	// This is intentionally the admin sidecar's narrow write path. It does not
	// clear schedule rows; linearcast observes the enabled flag on its periodic
	// channel refresh.
	if _, err := db.SetChannelEnabled(r.Context(), a.dbConn, channelID, enabled); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	note := "linearcast refreshes its channel list every ~60s; the in-memory runtime drops then. No restart required."
	if enabled {
		note = "linearcast refreshes its channel list every ~60s; the channel will start serving then. No restart required."
	}
	writeJSON(w, map[string]any{
		"channelID":  channelID,
		"enabled":    enabled,
		"wasEnabled": existing.Enabled,
		"note":       note,
	})
}

func (a *App) handleChannelEnable(w http.ResponseWriter, r *http.Request) {
	a.setChannelEnabled(w, r, true)
}

func (a *App) handleChannelDisable(w http.ResponseWriter, r *http.Request) {
	a.setChannelEnabled(w, r, false)
}

func (a *App) setChannelHiddenFromGuide(w http.ResponseWriter, r *http.Request, hidden bool) {
	channelID := r.PathValue("channelID")
	existing, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	if _, err := db.SetChannelHiddenFromGuide(r.Context(), a.dbConn, channelID, hidden); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"channelID":          channelID,
		"hiddenFromGuide":    hidden,
		"wasHiddenFromGuide": existing.HiddenFromGuide,
		"note":               "public guide/lineup listings update immediately; direct stream URLs keep working.",
	})
}

func (a *App) handleChannelHideFromGuide(w http.ResponseWriter, r *http.Request) {
	a.setChannelHiddenFromGuide(w, r, true)
}

func (a *App) handleChannelShowInGuide(w http.ResponseWriter, r *http.Request) {
	a.setChannelHiddenFromGuide(w, r, false)
}

func (a *App) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	existing, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	if existing.Enabled {
		writeError(w, http.StatusConflict, "channel_enabled", "disable the channel before deleting it")
		return
	}

	deleted, err := db.DeleteChannel(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"channelID": channelID,
		"deleted":   deleted > 0,
		"note":      "channel row, playlist membership, and schedule entries were deleted; media packages were kept.",
	})
}

func (a *App) handleChannelClone(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	clone, err := db.CloneChannel(r.Context(), a.dbConn, channelID, a.now().UTC().UnixMilli())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	media, err := db.ChannelMediaList(r.Context(), a.dbConn, clone.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, channelCloneResponse{
		SourceChannelID: channelID,
		ChannelID:       clone.ID,
		DisplayName:     clone.DisplayName,
		Enabled:         clone.Enabled,
		MediaCount:      len(media),
	})
}

// handleChannelExtend triggers a schedule extension pass for one channel.
// Mirrors the linearcast-extender daemon's per-channel work, but on demand.
// On ErrNoReadyPackages we return 200 with inserted=0 so the caller (UI) can
// surface a friendly "no packages yet" message — same contract as the
// import-time soft-success path.
func (a *App) handleChannelExtend(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")
	hours := 24
	if r.ContentLength > 0 {
		var req channelExtendRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		if req.Hours < 0 {
			writeError(w, http.StatusBadRequest, "invalid_hours", "hours must be >= 0")
			return
		}
		if req.Hours > 0 {
			hours = req.Hours
		}
	}

	existing, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}

	res, err := scheduler.ExtendChannel(r.Context(), a.dbConn, channelID, scheduler.ServiceOptions{HorizonHours: hours})
	if err != nil {
		if errors.Is(err, scheduler.ErrNoReadyPackages) {
			writeJSON(w, map[string]any{
				"channelID": channelID,
				"inserted":  0,
				"note":      "no ready packages yet; queue packages in the Encoding tab and wait for the packager-worker",
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "extend_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"channelID":       channelID,
		"inserted":        res.Inserted,
		"existingEndMs":   res.ExistingEndMs,
		"lastEndMs":       res.LastEndMs,
		"remainingMs":     res.RemainingMs,
		"skippedLowWater": res.SkippedLowWater,
	})
}

// handleChannelClearSchedule removes schedule_entries for the channel.
// Optional ?after=<unix-ms> narrows to entries at or after that timestamp;
// otherwise all entries are cleared. linearcast picks up the change on its
// next ~60s refresh tick.
func (a *App) handleChannelClearSchedule(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	existing, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}

	var cleared int64
	if afterStr := r.URL.Query().Get("after"); afterStr != "" {
		var afterMs int64
		if _, err := fmt.Sscanf(afterStr, "%d", &afterMs); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_after", "after must be a unix-ms integer")
			return
		}
		cleared, err = db.ClearScheduleAfter(r.Context(), a.dbConn, channelID, afterMs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	} else {
		cleared, err = db.ClearSchedule(r.Context(), a.dbConn, channelID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	writeJSON(w, map[string]any{
		"channelID": channelID,
		"cleared":   cleared,
		"note":      "linearcast drops the in-memory runtime on its next ~60s refresh tick.",
	})
}

func (a *App) handleChannelPolicy(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	ch, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if ch == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}

	profile := ch.RequiredPackageProfile
	if profile == "" {
		profile = db.DefaultPackageProfileForMediaKind(ch.MediaKind)
	}

	prefillMs := ch.PackagePrefillMs

	writeJSON(w, channelPolicyResponse{
		ChannelID:              ch.ID,
		PlaybackMode:           "packaged",
		RequiredPackageProfile: profile,
		PackagePrefillMs:       prefillMs,
		MediaKind:              string(db.NormalizeMediaKind(ch.MediaKind)),
	})
}

func (a *App) handleChannelPolicyUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")

	existing, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}

	var req channelPolicyUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	mediaKind := db.NormalizeMediaKind(existing.MediaKind)
	if req.MediaKind != nil {
		switch strings.ToLower(strings.TrimSpace(*req.MediaKind)) {
		case "", string(db.MediaKindVideo):
			mediaKind = db.MediaKindVideo
		case string(db.MediaKindMusic):
			mediaKind = db.MediaKindMusic
		default:
			writeError(w, http.StatusBadRequest, "invalid_media_kind", "mediaKind must be video or music")
			return
		}
	}

	profileName := strings.TrimSpace(existing.RequiredPackageProfile)
	if req.RequiredPackageProfile != nil {
		profileName = strings.TrimSpace(*req.RequiredPackageProfile)
	}
	if profileName == "" || (req.MediaKind != nil && db.NormalizeMediaKind(existing.MediaKind) != mediaKind && req.RequiredPackageProfile == nil) {
		profileName = db.DefaultPackageProfileForMediaKind(mediaKind)
	}
	record, err := db.PackageProfileByName(r.Context(), a.dbConn, profileName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if record == nil || record.Disabled {
		writeError(w, http.StatusBadRequest, "invalid_profile", "profile is not available")
		return
	}
	if string(record.Profile.MediaKind) != string(mediaKind) {
		writeError(w, http.StatusBadRequest, "profile_kind_mismatch",
			fmt.Sprintf("profile %s is for %s media, but channel %s is %s", profileName, record.Profile.MediaKind, channelID, mediaKind))
		return
	}
	profile := profileName

	var prefillMs *int64
	if req.PackagePrefillMs != nil {
		if *req.PackagePrefillMs <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_prefill", "packagePrefillMs must be positive")
			return
		}
		prefillMs = req.PackagePrefillMs
	}

	// Guard: if switching profiles, check the next 48 h of schedule. A non-zero
	// unready count means playback will break immediately after the switch.
	// Force=true skips the guard for operators who know what they're doing.
	currentProfile := existing.RequiredPackageProfile
	if currentProfile == "" {
		currentProfile = db.DefaultPackageProfileForMediaKind(existing.MediaKind)
	}
	newProfile := profile
	kindChanged := db.NormalizeMediaKind(existing.MediaKind) != mediaKind
	if !req.Force && !kindChanged && newProfile != currentProfile {
		const horizonMs = 48 * 3600 * 1000
		nowMs := a.now().UTC().UnixMilli()
		unready, err := db.ScheduleUnreadyCount(r.Context(), a.dbConn, channelID, newProfile, nowMs, horizonMs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		if unready > 0 {
			readiness, _ := db.ChannelProfileReadiness(r.Context(), a.dbConn, channelID, newProfile)
			w.Header().Set("Content-Type", "application/json")
			writeJSONStatus(w, http.StatusConflict, map[string]any{
				"code":    "profile_not_ready",
				"message": fmt.Sprintf("%d schedule entries in the next 48h lack a ready package at %s — queue packaging first or pass force:true", unready, newProfile),
				"readiness": map[string]any{
					"profile":    readiness.Profile,
					"total":      readiness.Total,
					"ready":      readiness.Ready,
					"pending":    readiness.Pending,
					"processing": readiness.Processing,
					"failed":     readiness.Failed,
					"missing":    readiness.Missing,
				},
			})
			return
		}
	}

	updated, err := db.UpdateChannelPlaybackPolicy(r.Context(), a.dbConn, channelID, db.PlaybackModePackaged, profile, prefillMs, mediaKind)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}

	ch, _ := db.ChannelByID(r.Context(), a.dbConn, channelID)
	var respProfile string
	if ch.RequiredPackageProfile != "" {
		respProfile = ch.RequiredPackageProfile
	} else {
		respProfile = db.DefaultPackageProfileForMediaKind(ch.MediaKind)
	}

	respPrefill := ch.PackagePrefillMs

	writeJSON(w, channelPolicyResponse{
		ChannelID:              channelID,
		PlaybackMode:           "packaged",
		RequiredPackageProfile: respProfile,
		PackagePrefillMs:       respPrefill,
		MediaKind:              string(db.NormalizeMediaKind(ch.MediaKind)),
	})
}

func (a *App) handleScheduleGaps(w http.ResponseWriter, r *http.Request) {
	channelID := r.URL.Query().Get("channel")
	horizonHours := 48
	if h := r.URL.Query().Get("hours"); h != "" {
		if _, err := fmt.Sscanf(h, "%d", &horizonHours); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_hours", "hours must be an integer")
			return
		}
	}
	nowMs := a.now().UTC().UnixMilli()
	endMs := nowMs + int64(horizonHours)*3600*1000

	type gapInfo struct {
		ChannelID  string `json:"channelId"`
		StartMs    int64  `json:"startMs"`
		EndMs      int64  `json:"endMs"`
		DurationMs int64  `json:"durationMs"`
	}
	type gapsResponse struct {
		NowMs int64     `json:"nowMs"`
		Gaps  []gapInfo `json:"gaps"`
	}

	var gaps []gapInfo
	var channels []db.Channel

	if channelID != "" {
		ch, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		if ch == nil {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		channels = []db.Channel{*ch}
	} else {
		var err error
		channels, err = db.EnabledChannels(r.Context(), a.dbConn)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}

	for _, ch := range channels {
		channelGaps, err := db.ScheduleGaps(r.Context(), a.dbConn, ch.ID, nowMs, endMs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		for _, g := range channelGaps {
			gaps = append(gaps, gapInfo{
				ChannelID:  ch.ID,
				StartMs:    g.StartMs,
				EndMs:      g.EndMs,
				DurationMs: g.DurationMs,
			})
		}
	}

	writeJSON(w, gapsResponse{NowMs: nowMs, Gaps: gaps})
}
