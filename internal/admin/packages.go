package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/lcingest"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

const allPackageProfiles = "all"

type mediaPackageCandidateResponse struct {
	Profile      string                       `json:"profile"`
	Count        int64                        `json:"count"`
	StatusCounts []cachePackageStatusSummary  `json:"statusCounts"`
	Media        []mediaPackageCandidateEntry `json:"media"`
}

type mediaPackageCandidateEntry struct {
	MediaID            string `json:"mediaId"`
	Title              string `json:"title"`
	Path               string `json:"path"`
	SchedulingGroup    string `json:"schedulingGroup"`
	DurationMs         int64  `json:"durationMs"`
	PackageID          string `json:"packageId,omitempty"`
	PackageStatus      string `json:"packageStatus"`
	PackageProfile     string `json:"packageProfile"`
	PackageError       string `json:"packageError,omitempty"`
	PackagedDurationMs *int64 `json:"packagedDurationMs,omitempty"`
	UpdatedAtMs        *int64 `json:"updatedAtMs,omitempty"`
	Selectable         bool   `json:"selectable"`
}

type mediaPackageRequest struct {
	MediaIDs []string `json:"mediaIds"`
	Profile  string   `json:"profile,omitempty"`
}

type mediaPackageCancelRequest struct {
	MediaIDs []string `json:"mediaIds"`
	Profile  string   `json:"profile,omitempty"`
	All      bool     `json:"all,omitempty"`
}

type mediaPackageFailureResponse struct {
	MediaID string `json:"mediaId"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type mediaPackageResponse struct {
	Profile        string                        `json:"profile"`
	Queued         []string                      `json:"queued"`
	AlreadyPending []string                      `json:"alreadyPending"`
	AlreadyReady   []string                      `json:"alreadyReady"`
	Failed         []mediaPackageFailureResponse `json:"failed"`
}

type mediaPackageCancelResponse struct {
	Profile            string   `json:"profile"`
	CanceledPending    int64    `json:"canceledPending"`
	CanceledProcessing int64    `json:"canceledProcessing"`
	SkippedReady       int64    `json:"skippedReady"`
	SkippedFailed      int64    `json:"skippedFailed"`
	SkippedMissing     int64    `json:"skippedMissing"`
	AffectedMediaIDs   []string `json:"affectedMediaIds"`
}

func (a *App) handleMediaSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "missing_q", "q is required")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
	}
	channelID := r.URL.Query().Get("channelId")

	rows, err := db.SearchMedia(r.Context(), a.dbConn, q, limit, channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	type mediaResult struct {
		MediaID          string `json:"mediaId"`
		Title            string `json:"title"`
		Path             string `json:"path"`
		SchedulingGroup  string `json:"schedulingGroup"`
		DurationMs       int64  `json:"durationMs"`
		CodecCheckPassed bool   `json:"codecCheckPassed"`
	}
	results := make([]mediaResult, len(rows))
	for i, m := range rows {
		results[i] = mediaResult{
			MediaID:          m.ID,
			Title:            m.Title,
			Path:             m.Path,
			SchedulingGroup:  m.SchedulingGroup,
			DurationMs:       m.DurationMs,
			CodecCheckPassed: m.CodecCheckPassed,
		}
	}
	writeJSON(w, results)
}

func (a *App) handleMediaGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := db.DistinctSchedulingGroups(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, map[string]any{"groups": groups})
}

type showHalfSummary struct {
	Half         int    `json:"half"`
	Group        string `json:"group"`
	EpisodeCount int    `json:"episodeCount"`
	DurationMs   int64  `json:"durationMs"`
}

type showSeasonSummary struct {
	Season       int               `json:"season"`
	EpisodeCount int               `json:"episodeCount"`
	DurationMs   int64             `json:"durationMs"`
	Halves       []showHalfSummary `json:"halves"`
}

type showSummary struct {
	ShowName     string              `json:"showName"`
	EpisodeCount int                 `json:"episodeCount"`
	DurationMs   int64               `json:"durationMs"`
	SeasonCount  int                 `json:"seasonCount"`
	Seasons      []showSeasonSummary `json:"seasons"`
}

func (a *App) handleMediaShows(w http.ResponseWriter, r *http.Request) {
	stats, err := db.SchedulingGroupRollup(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// Two-level fold: show -> season -> half.
	shows := map[string]*showSummary{}
	seasonsBy := map[string]map[int]*showSeasonSummary{}
	for _, s := range stats {
		show, season, half, ok := lcingest.ParseSchedulingGroup(s.Group)
		if !ok {
			continue
		}
		summary, found := shows[show]
		if !found {
			summary = &showSummary{ShowName: show}
			shows[show] = summary
			seasonsBy[show] = map[int]*showSeasonSummary{}
		}
		seasonSummary, found := seasonsBy[show][season]
		if !found {
			seasonSummary = &showSeasonSummary{Season: season}
			seasonsBy[show][season] = seasonSummary
		}
		seasonSummary.Halves = append(seasonSummary.Halves, showHalfSummary{
			Half:         half,
			Group:        s.Group,
			EpisodeCount: s.EpisodeCount,
			DurationMs:   s.DurationMs,
		})
		seasonSummary.EpisodeCount += s.EpisodeCount
		seasonSummary.DurationMs += s.DurationMs
		summary.EpisodeCount += s.EpisodeCount
		summary.DurationMs += s.DurationMs
	}
	out := make([]showSummary, 0, len(shows))
	for name, summary := range shows {
		seasonsMap := seasonsBy[name]
		seasonNums := make([]int, 0, len(seasonsMap))
		for k := range seasonsMap {
			seasonNums = append(seasonNums, k)
		}
		sort.Ints(seasonNums)
		for _, n := range seasonNums {
			season := seasonsMap[n]
			sort.Slice(season.Halves, func(i, j int) bool {
				return season.Halves[i].Half < season.Halves[j].Half
			})
			summary.Seasons = append(summary.Seasons, *season)
		}
		summary.SeasonCount = len(summary.Seasons)
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ShowName) < strings.ToLower(out[j].ShowName)
	})
	writeJSON(w, map[string]any{"shows": out})
}

type musicAlbumSummary struct {
	AlbumName  string `json:"albumName"`
	Group      string `json:"group"`
	TrackCount int    `json:"trackCount"`
	DurationMs int64  `json:"durationMs"`
}

type musicArtistSummary struct {
	ArtistName string              `json:"artistName"`
	AlbumCount int                 `json:"albumCount"`
	TrackCount int                 `json:"trackCount"`
	DurationMs int64               `json:"durationMs"`
	Albums     []musicAlbumSummary `json:"albums"`
}

// parseMusicGroup splits a music scheduling group into (artist, album).
// Expected format: "Artist — Album" (space + em-dash + space).
// If no separator is found, artist is "" and album is the full group string.
func parseMusicGroup(group string) (artist, album string) {
	const sep = " — "
	if idx := strings.Index(group, sep); idx >= 0 {
		return strings.TrimSpace(group[:idx]), strings.TrimSpace(group[idx+len(sep):])
	}
	return "", group
}

func (a *App) handleMediaAlbums(w http.ResponseWriter, r *http.Request) {
	stats, err := db.MusicAlbumRollup(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	artists := map[string]*musicArtistSummary{}
	albumsByArtist := map[string][]musicAlbumSummary{}

	for _, s := range stats {
		artistName, albumName := parseMusicGroup(s.Group)
		summary, found := artists[artistName]
		if !found {
			summary = &musicArtistSummary{ArtistName: artistName}
			artists[artistName] = summary
		}
		album := musicAlbumSummary{
			AlbumName:  albumName,
			Group:      s.Group,
			TrackCount: s.EpisodeCount,
			DurationMs: s.DurationMs,
		}
		albumsByArtist[artistName] = append(albumsByArtist[artistName], album)
		summary.TrackCount += s.EpisodeCount
		summary.DurationMs += s.DurationMs
	}

	out := make([]musicArtistSummary, 0, len(artists))
	for name, summary := range artists {
		albums := albumsByArtist[name]
		sort.Slice(albums, func(i, j int) bool {
			return strings.ToLower(albums[i].AlbumName) < strings.ToLower(albums[j].AlbumName)
		})
		summary.Albums = albums
		summary.AlbumCount = len(albums)
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		ai := out[i].ArtistName
		aj := out[j].ArtistName
		// Unknown artist (empty string) sorts last.
		if ai == "" && aj != "" {
			return false
		}
		if ai != "" && aj == "" {
			return true
		}
		return strings.ToLower(ai) < strings.ToLower(aj)
	})

	writeJSON(w, map[string]any{"artists": out})
}

func (a *App) handleMediaByGroup(w http.ResponseWriter, r *http.Request) {
	group := strings.TrimSpace(r.URL.Query().Get("group"))
	if group == "" {
		writeError(w, http.StatusBadRequest, "missing_group", "group is required")
		return
	}
	rows, err := db.MediaByGroup(r.Context(), a.dbConn, group)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	type mediaResult struct {
		MediaID         string `json:"mediaId"`
		Title           string `json:"title"`
		Path            string `json:"path"`
		SchedulingGroup string `json:"schedulingGroup"`
		DurationMs      int64  `json:"durationMs"`
	}
	results := make([]mediaResult, len(rows))
	for i, m := range rows {
		results[i] = mediaResult{
			MediaID:         m.ID,
			Title:           m.Title,
			Path:            m.Path,
			SchedulingGroup: m.SchedulingGroup,
			DurationMs:      m.DurationMs,
		}
	}
	writeJSON(w, results)
}

type profileDetailResponse struct {
	packageprofile.Profile
	IsBuiltin  bool                        `json:"isBuiltin"`
	Disabled   bool                        `json:"disabled"`
	References db.PackageProfileReferences `json:"references"`
}

func (a *App) handleMediaPackageProfiles(w http.ResponseWriter, r *http.Request) {
	details, err := db.AllPackageProfileRecords(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	profiles := make([]string, 0, len(details))
	enriched := make([]profileDetailResponse, 0, len(details))
	for _, record := range details {
		p := record.Profile
		if !record.Disabled {
			profiles = append(profiles, p.Name)
		}
		refs, err := db.PackageProfileReferencesForName(r.Context(), a.dbConn, p.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		enriched = append(enriched, profileDetailResponse{
			Profile:    p,
			IsBuiltin:  record.IsBuiltin || packageprofile.Known(p.Name),
			Disabled:   record.Disabled,
			References: refs,
		})
	}
	writeJSON(w, map[string]any{
		"profiles":       profiles,
		"profileDetails": enriched,
		"defaultProfile": a.defaultMediaPackageProfile(r.Context(), profiles),
	})
}

func (a *App) handleMediaPackageCandidates(w http.ResponseWriter, r *http.Request) {
	rawProfile := strings.TrimSpace(r.URL.Query().Get("profile"))
	allProfiles := strings.EqualFold(rawProfile, allPackageProfiles)
	profile := allPackageProfiles
	var ok bool
	if !allProfiles {
		profile, ok = a.resolveMediaPackageProfile(r.Context(), w, rawProfile)
		if !ok {
			return
		}
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &offset); err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be a non-negative integer")
			return
		}
	}

	f := db.CandidateFilter{
		Search: r.URL.Query().Get("search"),
		Status: r.URL.Query().Get("status"),
	}
	var rows []db.MediaPackageCandidate
	var err error
	if allProfiles {
		rows, err = db.MediaPackageCandidatesAllProfiles(r.Context(), a.dbConn, limit, offset, f)
	} else {
		rows, err = db.MediaPackageCandidates(r.Context(), a.dbConn, profile, limit, offset, f)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	var statusRows []db.PackageStatusSummary
	if allProfiles {
		statusRows, err = db.MediaPackageCandidateStatusCountsAllProfiles(r.Context(), a.dbConn)
	} else {
		statusRows, err = db.MediaPackageCandidateStatusCounts(r.Context(), a.dbConn, profile)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	filterStatus := strings.TrimSpace(f.Status)
	var nonReadyCount, matchingStatusCount, allStatusCount int64
	resp := mediaPackageCandidateResponse{
		Profile:      profile,
		StatusCounts: make([]cachePackageStatusSummary, 0, len(statusRows)),
		Media:        make([]mediaPackageCandidateEntry, 0, len(rows)),
	}
	for _, row := range statusRows {
		allStatusCount += row.Count
		if row.Status != string(db.PackageStatusReady) {
			nonReadyCount += row.Count
		}
		if filterStatus != "" && row.Status == filterStatus {
			matchingStatusCount = row.Count
		}
		resp.StatusCounts = append(resp.StatusCounts, cachePackageStatusSummary{
			Status: row.Status,
			Count:  row.Count,
		})
	}
	switch filterStatus {
	case "":
		resp.Count = nonReadyCount
	case "all":
		resp.Count = allStatusCount
	default:
		resp.Count = matchingStatusCount
	}
	for _, row := range rows {
		status := "missing"
		if row.PackageStatus != nil && strings.TrimSpace(*row.PackageStatus) != "" {
			status = *row.PackageStatus
		}
		item := mediaPackageCandidateEntry{
			MediaID:         row.MediaID,
			Title:           row.Title,
			Path:            row.Path,
			SchedulingGroup: row.SchedulingGroup,
			DurationMs:      row.DurationMs,
			PackageStatus:   status,
			PackageProfile:  row.RenditionProfile,
			Selectable:      !allProfiles && (status == "missing" || status == string(db.PackageStatusFailed)),
		}
		if row.PackageID != nil {
			item.PackageID = *row.PackageID
		}
		if row.PackageError != nil {
			item.PackageError = *row.PackageError
		}
		if row.PackagedDurationMs != nil {
			v := *row.PackagedDurationMs
			item.PackagedDurationMs = &v
		}
		if row.UpdatedAtMs != nil {
			v := *row.UpdatedAtMs
			item.UpdatedAtMs = &v
		}
		resp.Media = append(resp.Media, item)
	}
	writeJSON(w, resp)
}

// handleMediaPackage queues package work for arbitrary media IDs, independent
// of channel membership.
func (a *App) handleMediaPackage(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req mediaPackageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if len(req.MediaIDs) == 0 {
		writeError(w, http.StatusBadRequest, "missing_media_ids", "mediaIds is required")
		return
	}
	if len(req.MediaIDs) > 500 {
		writeError(w, http.StatusBadRequest, "too_many_media_ids", "mediaIds is capped at 500 per request")
		return
	}
	profile, ok := a.resolveMediaPackageProfile(r.Context(), w, req.Profile)
	if !ok {
		return
	}

	result, err := db.RequestMediaPackages(r.Context(), a.dbConn, req.MediaIDs, profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	resp := mediaPackageResponse{
		Profile:        result.Profile,
		Queued:         result.Queued,
		AlreadyPending: result.AlreadyPending,
		AlreadyReady:   result.AlreadyReady,
		Failed:         make([]mediaPackageFailureResponse, 0, len(result.Failed)),
	}
	for _, failure := range result.Failed {
		resp.Failed = append(resp.Failed, mediaPackageFailureResponse{
			MediaID: failure.MediaID,
			Code:    failure.Code,
			Message: failure.Message,
		})
	}
	writeJSON(w, resp)
}

// handleMediaPackageCancel cancels queued package work. Pending rows stop being
// claimable immediately; processing rows are marked failed and the worker's
// package-state monitor interrupts the active encode.
func (a *App) handleMediaPackageCancel(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req mediaPackageCancelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if !req.All && len(req.MediaIDs) == 0 {
		writeError(w, http.StatusBadRequest, "missing_target", "mediaIds or all:true is required")
		return
	}

	allProfiles := strings.EqualFold(strings.TrimSpace(req.Profile), allPackageProfiles)
	profile := allPackageProfiles
	var ok bool
	if !allProfiles {
		profile, ok = a.resolveMediaPackageProfile(r.Context(), w, req.Profile)
		if !ok {
			return
		}
	}

	nowMs := a.now().UTC().UnixMilli()
	reason := "cancelled by operator"
	var result db.MediaPackageCancelResult
	var err error
	if req.All {
		result, err = db.CancelAllMediaPackagesForProfile(r.Context(), a.dbConn, profile, nowMs, reason)
	} else {
		if allProfiles {
			writeError(w, http.StatusBadRequest, "invalid_profile", "profile=all requires all:true")
			return
		}
		result, err = db.CancelMediaPackages(r.Context(), a.dbConn, req.MediaIDs, profile, nowMs, reason)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, mediaPackageCancelResponse{
		Profile:            result.Profile,
		CanceledPending:    result.CanceledPending,
		CanceledProcessing: result.CanceledProcessing,
		SkippedReady:       result.SkippedReady,
		SkippedFailed:      result.SkippedFailed,
		SkippedMissing:     result.SkippedMissing,
		AffectedMediaIDs:   result.AffectedMediaIDs,
	})
}

func (a *App) resolveMediaPackageProfile(ctx context.Context, w http.ResponseWriter, raw string) (string, bool) {
	profile := strings.TrimSpace(raw)
	profiles, err := db.AllPackageProfileNames(ctx, a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return "", false
	}
	if profile == "" {
		profile = a.defaultMediaPackageProfile(ctx, profiles)
	}
	for _, allowed := range profiles {
		if profile == allowed {
			return profile, true
		}
	}
	writeError(w, http.StatusBadRequest, "invalid_profile", "profile is not available")
	return "", false
}

func (a *App) defaultMediaPackageProfile(ctx context.Context, profiles []string) string {
	configured, _ := db.GetDefaultPackagedProfile(ctx, a.dbConn)
	configured = strings.TrimSpace(configured)
	if configured != "" {
		for _, profile := range profiles {
			if profile == configured {
				return configured
			}
		}
	}
	for _, profile := range profiles {
		if profile == db.DefaultPackageProfile {
			return db.DefaultPackageProfile
		}
	}
	return db.DefaultPackageProfile
}

type defaultProfileRequest struct {
	Name string `json:"name"`
}

// handleDefaultPackagedProfileUpdate persists the default profile name used
// for new channels and the playback fallback. Validates the target profile
// exists and isn't disabled; updates take effect on next process start for
// linearcast + extender, and on next request for admin reads.
func (a *App) handleDefaultPackagedProfileUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req defaultProfileRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "name is required")
		return
	}
	record, err := db.PackageProfileByName(r.Context(), a.dbConn, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if record == nil {
		writeError(w, http.StatusNotFound, "not_found", "profile does not exist")
		return
	}
	if record.Disabled {
		writeError(w, http.StatusConflict, "disabled_profile", "disabled profiles cannot be the default")
		return
	}
	if err := db.SetDefaultPackagedProfile(r.Context(), a.dbConn, name); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "default": name})
}

func (a *App) handlePackageProfileUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "profile name is required")
		return
	}
	var p packageprofile.Profile
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name != name {
		writeError(w, http.StatusBadRequest, "name_mismatch", "profile name must match request path")
		return
	}

	record, err := db.PackageProfileByName(r.Context(), a.dbConn, p.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if record != nil && record.Disabled {
		writeError(w, http.StatusConflict, "disabled_profile", "disabled profiles are read-only")
		return
	}

	// Validate built-in profiles can't be overwritten with custom JSON.
	builtin, err := db.IsBuiltinProfile(r.Context(), a.dbConn, p.Name)
	if err != nil && errors.Is(err, db.ErrProfileNotFound) {
		// New custom profile - ok to create.
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	} else if builtin {
		writeError(w, http.StatusConflict, "builtin_profile", "built-in profiles cannot be modified")
		return
	}

	if err := db.UpsertPackageProfile(r.Context(), a.dbConn, p); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{"name": p.Name, "status": "ok"})
}

func (a *App) handlePackageProfileDelete(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "profile name is required")
		return
	}

	record, err := db.PackageProfileByName(r.Context(), a.dbConn, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if record == nil {
		writeError(w, http.StatusNotFound, "not_found", "profile not found")
		return
	}
	if record.Disabled {
		writeJSON(w, map[string]any{"name": name, "deleted": false, "disabled": true})
		return
	}

	refs, err := db.PackageProfileReferencesForName(r.Context(), a.dbConn, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if record.IsBuiltin || refs.HasAny() {
		if err := db.DisablePackageProfile(r.Context(), a.dbConn, name); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		writeJSON(w, map[string]any{"name": name, "deleted": false, "disabled": true, "references": refs})
		return
	}

	if err := db.DeletePackageProfile(r.Context(), a.dbConn, name); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{"name": name, "deleted": true, "disabled": false, "references": refs})
}

type profileMigrationResponse struct {
	ChannelID  string `json:"channelId"`
	Profile    string `json:"profile"`
	Total      int64  `json:"total"`
	Ready      int64  `json:"ready"`
	Pending    int64  `json:"pending"`
	Processing int64  `json:"processing"`
	Failed     int64  `json:"failed"`
	Missing    int64  `json:"missing"`
	Queued     int    `json:"queued,omitempty"`
}

func readinessToResponse(channelID string, r db.ProfileReadiness) profileMigrationResponse {
	return profileMigrationResponse{
		ChannelID:  channelID,
		Profile:    r.Profile,
		Total:      r.Total,
		Ready:      r.Ready,
		Pending:    r.Pending,
		Processing: r.Processing,
		Failed:     r.Failed,
		Missing:    r.Missing,
	}
}

// handleChannelProfileMigrationStatus returns packaging coverage counts for
// a channel's media at a given profile. Used by the UI to show migration
// progress before a profile cutover.
func (a *App) handleChannelProfileMigrationStatus(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	profile, ok := a.resolveMediaPackageProfile(r.Context(), w, r.URL.Query().Get("profile"))
	if !ok {
		return
	}
	readiness, err := db.ChannelProfileReadiness(r.Context(), a.dbConn, channelID, profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, readinessToResponse(channelID, readiness))
}

// handleChannelProfileMigrationQueue queues all codec-passing channel media
// for packaging at the target profile and returns updated readiness counts.
func (a *App) handleChannelProfileMigrationQueue(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	channelID := r.PathValue("channelID")

	var req struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	profile, ok := a.resolveMediaPackageProfile(r.Context(), w, req.Profile)
	if !ok {
		return
	}
	result, err := db.QueueChannelProfileMigration(r.Context(), a.dbConn, channelID, profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	readiness, err := db.ChannelProfileReadiness(r.Context(), a.dbConn, channelID, profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	resp := readinessToResponse(channelID, readiness)
	resp.Queued = len(result.Queued)
	writeJSON(w, resp)
}
