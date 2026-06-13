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
	ScheduleMode    string `json:"scheduleMode"`
	SlotDurationMs  *int64 `json:"slotDurationMs,omitempty"`
	HiddenFromGuide bool   `json:"hiddenFromGuide"`
	ArtworkURL      string `json:"artworkUrl,omitempty"`
	MediaKind       string `json:"mediaKind"`
}

type channelPolicyResponse struct {
	ChannelID              string `json:"channelId"`
	PlaybackMode           string `json:"playbackMode"`
	RequiredPackageProfile string `json:"requiredPackageProfile"`
	AdaptiveBitrate        bool   `json:"adaptiveBitrate"`
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
	PlaybackMode   string   `json:"playbackMode,omitempty"` // "packaged" (default) or "plex_relay"
	PackageProfile string   `json:"packageProfile"`
	MediaIDs       []string `json:"mediaIds"`
	Ordering       string   `json:"ordering,omitempty"`
	ScheduleMode   string   `json:"scheduleMode,omitempty"`
	SlotDurationMs *int64   `json:"slotDurationMs,omitempty"`
	UpstreamHLSURL string   `json:"upstreamHlsUrl,omitempty"`
	// PrefillMode is "eager" (default) or "on_demand". On-demand channels defer
	// packaging until a viewer tunes in; the schedule still builds ahead.
	PrefillMode string `json:"prefillMode,omitempty"`
	// AdaptiveBitrate selects a prepackaged video ladder at channel creation
	// time: "" (off), "cpu" (libx264), or "nvenc" (h264_nvenc).
	// It is intentionally not a mutable channel policy.
	AdaptiveBitrate string `json:"adaptiveBitrate,omitempty"`
	// Entries, when present, is an explicit wall-clock-ordered schedule the
	// client has already composed (primary programming plus the filler that
	// covers every slot gap). The server lays them contiguously from 0, so the
	// schedule is gap-free by construction; FillerMediaIDs marks which entries
	// are filler so the slot grid can be validated. Empty Entries falls back to
	// server-built scheduling (ExtendChannel).
	Entries        []scheduleEntryInput `json:"entries,omitempty"`
	FillerMediaIDs []string             `json:"fillerMediaIds,omitempty"`
}

// scheduleEntryInput is one client-composed schedule row. start_ms is implied by
// contiguous laying (sum of preceding durations), so only the media, its play
// offset, and its on-air duration are sent.
type scheduleEntryInput struct {
	MediaID    string `json:"mediaId"`
	OffsetMs   int64  `json:"offsetMs"`
	DurationMs int64  `json:"durationMs"`
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
	req.ScheduleMode = strings.TrimSpace(req.ScheduleMode)
	req.UpstreamHLSURL = strings.TrimSpace(req.UpstreamHLSURL)
	req.PrefillMode = strings.TrimSpace(req.PrefillMode)

	if req.DisplayName == "" {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "missing_display_name", Message: "displayName is required"}
	}
	if req.Ordering == "" {
		req.Ordering = "alphabetical"
	}
	if req.ScheduleMode == "" {
		req.ScheduleMode = "back_to_back"
	}
	if req.ScheduleMode != "back_to_back" && req.ScheduleMode != "slot_grid" {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_schedule_mode", Message: "scheduleMode must be back_to_back or slot_grid"}
	}
	if req.SlotDurationMs != nil && (*req.SlotDurationMs <= 0 || *req.SlotDurationMs%scheduler.TargetSegmentMs != 0) {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_slot_duration", Message: "slotDurationMs must be a positive 6000ms-aligned value"}
	}
	if req.PrefillMode == "" {
		req.PrefillMode = "eager"
	}
	if req.PrefillMode != "eager" && req.PrefillMode != "on_demand" {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_prefill_mode", Message: "prefillMode must be eager or on_demand"}
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
			ScheduleMode:   req.ScheduleMode,
			SlotDurationMs: req.SlotDurationMs,
			UpstreamHLSURL: &req.UpstreamHLSURL,
		}); err != nil {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("create channel: %v", err)}
		}
		return createChannelResponse{ChannelID: channelID, DisplayName: req.DisplayName, Created: true}, nil
	}

	if req.PlaybackMode == string(db.PlaybackModePlexRelay) {
		if len(req.MediaIDs) == 0 {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "empty_media", Message: "at least one mediaId is required for schedule"}
		}
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
			if !strings.HasPrefix(strings.TrimSpace(m.SourceRef), "plex://") {
				return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "non_plex_media", Message: "plex_relay channels can only use media imported from Plex"}
			}
			if !m.CodecCheckPassed {
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
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusUnprocessableEntity, Code: "no_eligible_media", Message: "none of the supplied mediaIds are eligible Plex media"}
		}
		if err := db.InsertChannel(ctx, a.dbConn, db.ChannelWrite{
			ID:           channelID,
			DisplayName:  req.DisplayName,
			PlaybackMode: db.PlaybackModePlexRelay,
			Ordering:     req.Ordering,
			MediaKind:    db.MediaKindVideo,
			ScheduleMode: req.ScheduleMode,
		}); err != nil {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("create channel: %v", err)}
		}
		if err := db.WithTx(ctx, a.dbConn, func(tx db.Execer) error {
			return db.ReplaceChannelMedia(ctx, tx, channelID, members)
		}); err != nil {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("write channel_media: %v", err)}
		}
		scheduleEntries := 0
		if len(req.Entries) > 0 {
			explicitEntries, fillerAssetIDs, herr := a.validateExplicitSchedule(ctx, channelID, req, nowMs)
			if herr != nil {
				return createChannelResponse{}, herr
			}
			n, herr := a.writeExplicitSchedule(ctx, channelID, explicitEntries, fillerAssetIDs)
			if herr != nil {
				return createChannelResponse{}, herr
			}
			scheduleEntries = n
		}
		if len(req.Entries) == 0 {
			if sched, err := scheduler.ExtendChannel(ctx, a.dbConn, channelID, scheduler.ServiceOptions{HorizonHours: 24}); err == nil {
				scheduleEntries = sched.Inserted
			}
		}
		return createChannelResponse{
			ChannelID:       channelID,
			DisplayName:     req.DisplayName,
			Created:         true,
			SyncedMedia:     len(members),
			ScheduleEntries: scheduleEntries,
		}, nil
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
	var abrLadder []string
	if req.AdaptiveBitrate != "" {
		if mediaKind != db.MediaKindVideo {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_abr_media_kind", Message: "adaptive bitrate is only available for video channels"}
		}
		if req.PrefillMode == "on_demand" {
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_abr_prefill", Message: "adaptive bitrate is only available for pre-encoded channels"}
		}
		switch req.AdaptiveBitrate {
		case "cpu":
			abrLadder = db.StandardVideoABRLadder
		case "nvenc":
			abrLadder = db.StandardVideoNVENCABRLadder
		case "hdr":
			abrLadder = db.StandardVideoHDRABRLadder
		default:
			return createChannelResponse{}, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_abr_mode", Message: "adaptiveBitrate must be \"cpu\", \"nvenc\", or \"hdr\""}
		}
		if req.PackageProfile != db.DefaultPackageProfile {
			abrLadder = append([]string{req.PackageProfile}, abrLadder...)
		}
	}

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

	// Validate an explicit client-composed schedule up front, before any channel
	// row is written, so a malformed or gappy payload never leaves a half-built
	// channel behind.
	var explicitEntries []db.ScheduleEntry
	var fillerAssetIDs []string
	if len(req.Entries) > 0 {
		var herr *adminHTTPError
		explicitEntries, fillerAssetIDs, herr = a.validateExplicitSchedule(ctx, channelID, req, nowMs)
		if herr != nil {
			return createChannelResponse{}, herr
		}
	}

	// Create the channel.
	if err := db.InsertChannel(ctx, a.dbConn, db.ChannelWrite{
		ID:                     channelID,
		DisplayName:            req.DisplayName,
		Ordering:               req.Ordering,
		RequiredPackageProfile: req.PackageProfile,
		ABRLadder:              abrLadder,
		MediaKind:              mediaKind,
		ScheduleMode:           req.ScheduleMode,
		SlotDurationMs:         req.SlotDurationMs,
		PrefillMode:            req.PrefillMode,
	}); err != nil {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("create channel: %v", err)}
	}

	// Write channel_media membership.
	if err := db.WithTx(ctx, a.dbConn, func(tx db.Execer) error {
		return db.ReplaceChannelMedia(ctx, tx, channelID, members)
	}); err != nil {
		return createChannelResponse{}, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("write channel_media: %v", err)}
	}

	scheduleEntries := 0
	if len(explicitEntries) > 0 {
		// Client owns the layout: persist the explicit, already-contiguous
		// schedule and skip the server-built (gappy) slot grid.
		n, herr := a.writeExplicitSchedule(ctx, channelID, explicitEntries, fillerAssetIDs)
		if herr != nil {
			return createChannelResponse{}, herr
		}
		scheduleEntries = n
	} else {
		// Best-effort schedule extension — no ready packages yet is fine.
		if sched, err := scheduler.ExtendChannel(ctx, a.dbConn, channelID, scheduler.ServiceOptions{HorizonHours: 24}); err == nil {
			scheduleEntries = sched.Inserted
		}
	}

	return createChannelResponse{
		ChannelID:       channelID,
		DisplayName:     req.DisplayName,
		Created:         true,
		SyncedMedia:     len(members),
		ScheduleEntries: scheduleEntries,
	}, nil
}

// validateExplicitSchedule turns a client-composed schedule into persistable
// entries. It is read-only so it can run before the channel row is written.
// Entries are laid contiguously from 0 (gap-free by construction); for slot-grid
// channels it also enforces that every primary (non-filler) entry lands on a slot
// boundary — a primary falling off-boundary means an upstream gap was never
// filled, which is exactly the "never save a channel with gaps" invariant. The
// returned fillerAssetIDs are the channel_filler_assets rows writeExplicitSchedule
// must attach.
func (a *App) validateExplicitSchedule(ctx context.Context, channelID string, req createChannelRequest, nowMs int64) ([]db.ScheduleEntry, []string, *adminHTTPError) {
	ids := make([]string, 0, len(req.Entries))
	for _, e := range req.Entries {
		ids = append(ids, strings.TrimSpace(e.MediaID))
	}
	mediaMap, err := db.MediaByIDs(ctx, a.dbConn, ids)
	if err != nil {
		return nil, nil, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: err.Error()}
	}

	fillerSet := make(map[string]bool, len(req.FillerMediaIDs))
	for _, id := range req.FillerMediaIDs {
		fillerSet[strings.TrimSpace(id)] = true
	}

	slotMs := int64(0)
	if req.ScheduleMode == "slot_grid" {
		slotMs = 30 * 60 * 1000
		if req.SlotDurationMs != nil {
			slotMs = *req.SlotDurationMs
		}
	}

	// schedule_entries.start_ms is wall-clock epoch ms. The client composes a
	// 0-based layout, so rebase onto the current 6s grid; for slot-grid snap that
	// base forward to the next real slot boundary so primaries land on :00/:30
	// wall-clock, matching BuildEntriesSlotGrid.
	base := scheduler.Align6s(nowMs)
	if slotMs > 0 {
		base = scheduler.AlignToSlot(base, slotMs)
	}

	entries := make([]db.ScheduleEntry, 0, len(req.Entries))
	cursor := int64(0)
	for i, in := range req.Entries {
		mediaID := strings.TrimSpace(in.MediaID)
		if mediaID == "" {
			return nil, nil, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_entry", Message: fmt.Sprintf("entry %d has an empty mediaId", i)}
		}
		if _, ok := mediaMap[mediaID]; !ok {
			return nil, nil, &adminHTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_entry", Message: fmt.Sprintf("entry %d references unknown media %q", i, mediaID)}
		}
		if in.DurationMs <= 0 || in.DurationMs%scheduler.TargetSegmentMs != 0 {
			return nil, nil, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_entry", Message: fmt.Sprintf("entry %d duration_ms=%d must be a positive %dms-aligned value", i, in.DurationMs, scheduler.TargetSegmentMs)}
		}
		if in.OffsetMs < 0 || in.OffsetMs%scheduler.TargetSegmentMs != 0 {
			return nil, nil, &adminHTTPError{Status: http.StatusBadRequest, Code: "invalid_entry", Message: fmt.Sprintf("entry %d offset_ms=%d must be a non-negative %dms-aligned value", i, in.OffsetMs, scheduler.TargetSegmentMs)}
		}
		// Slot-grid integrity: a primary entry must begin on a slot boundary. If
		// it does not, a preceding gap was left unfilled.
		if slotMs > 0 && !fillerSet[mediaID] && cursor%slotMs != 0 {
			return nil, nil, &adminHTTPError{Status: http.StatusBadRequest, Code: "schedule_has_gaps", Message: fmt.Sprintf("entry %d (%s) starts at %dms, off the %dms slot grid — fill every gap before creating the channel", i, mediaID, cursor, slotMs)}
		}
		kind := "primary"
		if fillerSet[mediaID] {
			kind = "filler"
		}
		entries = append(entries, db.ScheduleEntry{
			ChannelID:   channelID,
			StartMs:     base + cursor,
			MediaID:     mediaID,
			OffsetMs:    in.OffsetMs,
			DurationMs:  in.DurationMs,
			CreatedAtMs: nowMs,
			Kind:        kind,
		})
		cursor += in.DurationMs
	}
	if len(entries) == 0 {
		return nil, nil, &adminHTTPError{Status: http.StatusBadRequest, Code: "empty_schedule", Message: "explicit schedule must contain at least one entry"}
	}

	// Resolve filler media to their asset IDs (so writeExplicitSchedule can attach
	// them). This mirrors the auto-attach FillGap performs at fill time.
	fillerAssetIDs := make([]string, 0, len(fillerSet))
	for mediaID := range fillerSet {
		if mediaID == "" {
			continue
		}
		asset, assetErr := db.FillerAssetByMediaID(ctx, a.dbConn, mediaID)
		if assetErr != nil {
			if errors.Is(assetErr, sql.ErrNoRows) {
				return nil, nil, &adminHTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_filler", Message: fmt.Sprintf("filler media %q is not a registered filler asset", mediaID)}
			}
			return nil, nil, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: assetErr.Error()}
		}
		fillerAssetIDs = append(fillerAssetIDs, asset.ID)
	}

	return entries, fillerAssetIDs, nil
}

// writeExplicitSchedule attaches the channel's filler assets and persists the
// validated entries. Entries insert atomically; InsertScheduleEntries chains
// anchors and re-checks 6s alignment.
func (a *App) writeExplicitSchedule(ctx context.Context, channelID string, entries []db.ScheduleEntry, fillerAssetIDs []string) (int, *adminHTTPError) {
	for _, assetID := range fillerAssetIDs {
		if attachErr := db.AttachChannelFillerAsset(ctx, a.dbConn, channelID, assetID, 1, true); attachErr != nil {
			return 0, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: attachErr.Error()}
		}
	}

	var inserted int
	if txErr := db.WithImmediateTx(ctx, a.dbConn, func(tx db.Execer) error {
		n, err := db.InsertScheduleEntries(ctx, tx, entries)
		inserted = n
		return err
	}); txErr != nil {
		return 0, &adminHTTPError{Status: http.StatusInternalServerError, Code: "db_error", Message: fmt.Sprintf("insert schedule: %v", txErr)}
	}

	// Defense-in-depth: contiguous laying cannot leave a hole, but verify before
	// declaring the schedule complete in case of an insert/anchor regression.
	firstStart := entries[0].StartMs
	lastEnd := entries[len(entries)-1].StartMs + entries[len(entries)-1].DurationMs
	if gaps, gapErr := db.ScheduleGaps(ctx, a.dbConn, channelID, firstStart, lastEnd); gapErr == nil && len(gaps) > 0 {
		return 0, &adminHTTPError{Status: http.StatusInternalServerError, Code: "schedule_has_gaps", Message: "persisted schedule unexpectedly contains gaps"}
	}
	return inserted, nil
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
			ScheduleMode:    c.ScheduleMode,
			SlotDurationMs:  c.SlotDurationMs,
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

	playbackMode := string(ch.PlaybackMode)
	if playbackMode == "" {
		playbackMode = "packaged"
	}

	writeJSON(w, channelPolicyResponse{
		ChannelID:              ch.ID,
		PlaybackMode:           playbackMode,
		RequiredPackageProfile: profile,
		AdaptiveBitrate:        db.ABRLadderEnabled(profile, ch.ABRLadder),
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

	updated, err := db.UpdateChannelPlaybackPolicy(r.Context(), a.dbConn, channelID, db.PlaybackModePackaged, profile, existing.ABRLadder, prefillMs, mediaKind)
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
		AdaptiveBitrate:        db.ABRLadderEnabled(respProfile, ch.ABRLadder),
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
