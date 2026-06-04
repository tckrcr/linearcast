package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/lcingest"
	"github.com/tckrcr/linearcast/internal/plex"
)

type plexStatusResponse struct {
	Connected  bool   `json:"connected"`
	Username   string `json:"username,omitempty"`
	ServerName string `json:"serverName,omitempty"`
	URL        string `json:"url,omitempty"`
	PathMap    string `json:"pathMap,omitempty"`
}

type plexConfigRequest struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	PathMap string `json:"pathMap,omitempty"`
}

func (a *App) handleAdminPlexStatus(w http.ResponseWriter, r *http.Request) {
	plexURL, err := a.effectivePlexURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex url")
		return
	}
	pathMap, err := a.effectivePlexPathMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex path map")
		return
	}
	token, err := db.GetPlexToken(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex token")
		return
	}
	if token == "" {
		writeJSON(w, plexStatusResponse{Connected: false, URL: plexURL, PathMap: pathMap})
		return
	}
	status, err := a.checkPlexConnection(plexURL, token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_status_error", a.plexConnectivityMessage(plexURL, err))
		return
	}
	status.URL = plexURL
	status.PathMap = pathMap
	writeJSON(w, status)
}

func (a *App) handleAdminPlexConfigSet(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req plexConfigRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	plexURL, err := normalizeAbsoluteHTTPURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plex_url", err.Error())
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, "empty_token", "token is required")
		return
	}
	pathMap := strings.TrimSpace(req.PathMap)

	status, err := a.checkPlexConnection(plexURL, token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_connectivity_error", a.plexConnectivityMessage(plexURL, err))
		return
	}
	if err := db.SetPlexURL(r.Context(), a.dbConn, plexURL); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store plex url")
		return
	}
	if err := db.SetPlexToken(r.Context(), a.dbConn, token); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store plex token")
		return
	}
	if err := db.SetPlexPathMap(r.Context(), a.dbConn, pathMap); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store plex path map")
		return
	}
	status.URL = plexURL
	status.PathMap = pathMap
	writeJSON(w, status)
}

func (a *App) handleAdminPlexConfigClear(w http.ResponseWriter, r *http.Request) {
	if err := db.ClearPlexToken(r.Context(), a.dbConn); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to clear plex token")
		return
	}
	plexURL, _ := a.effectivePlexURL()
	pathMap, _ := a.effectivePlexPathMap()
	writeJSON(w, plexStatusResponse{Connected: false, URL: plexURL, PathMap: pathMap})
}

func (a *App) checkPlexConnection(plexURL, token string) (plexStatusResponse, error) {
	client := plex.New(plexURL, token)
	client.HTTPClient = a.httpClient
	status, err := client.Status()
	if err != nil {
		return plexStatusResponse{}, err
	}
	return plexStatusResponse{
		Connected:  true,
		Username:   status.Username,
		ServerName: status.ServerName,
	}, nil
}

func (a *App) plexConnectivityMessage(plexURL string, err error) string {
	if strings.TrimSpace(plexURL) == "" {
		return "Plex URL is not configured. Enter the URL in Admin Tools."
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403") {
			return "Plex rejected the token. Replace it with a valid Plex token or sign out."
		}
	}
	return "Could not connect to Plex. Check the URL and that Plex Media Server is reachable, then try again."
}

type plexScanRequest struct {
	LibraryKey    string `json:"libraryKey"`
	MaxResolution string `json:"maxResolution,omitempty"` // "", "1080", "720"
}

func (a *App) handlePlexLibraries(w http.ResponseWriter, r *http.Request) {
	plexURL, err := a.effectivePlexURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex url")
		return
	}
	token, err := db.GetPlexToken(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex token")
		return
	}
	if plexURL == "" || token == "" {
		writeError(w, http.StatusBadRequest, "not_configured", "Plex is not connected")
		return
	}
	client := plex.New(plexURL, token)
	client.HTTPClient = a.httpClient
	sections, err := client.Sections()
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_error", err.Error())
		return
	}
	type libEntry struct {
		Key   string `json:"key"`
		Title string `json:"title"`
		Type  string `json:"type"`
	}
	out := make([]libEntry, 0, len(sections))
	for _, s := range sections {
		if s.Type != "movie" && s.Type != "show" {
			continue
		}
		out = append(out, libEntry{Key: s.Key, Title: s.Title, Type: s.Type})
	}
	writeJSON(w, out)
}

func (a *App) handlePlexScan(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req plexScanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.LibraryKey = strings.TrimSpace(req.LibraryKey)
	req.MaxResolution = strings.TrimSpace(req.MaxResolution)
	if req.LibraryKey == "" {
		writeError(w, http.StatusBadRequest, "missing_library", "libraryKey is required")
		return
	}

	plexURL, err := a.effectivePlexURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex url")
		return
	}
	token, err := db.GetPlexToken(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex token")
		return
	}
	pathMapStr, err := a.effectivePlexPathMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex path map")
		return
	}
	mapper, err := plex.ParsePathMap(pathMapStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "plex_path_map_error", err.Error())
		return
	}

	client := plex.New(plexURL, token)
	client.HTTPClient = a.httpClient
	sec, err := client.FindSection(req.LibraryKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_error", err.Error())
		return
	}
	if sec == nil {
		writeError(w, http.StatusBadRequest, "library_not_found", "library not found")
		return
	}
	items, err := client.Items(*sec, plex.ItemsOptions{})
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_error", err.Error())
		return
	}

	// Filter by max resolution.
	filtered := make([]plex.Item, 0, len(items))
	for _, it := range items {
		if plexResolutionExceeds(it.Resolution, req.MaxResolution) {
			continue
		}
		filtered = append(filtered, it)
	}

	jobID, job := a.ingestJobs.create()
	go a.runPlexScan(job, filtered, mapper)

	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

// mediaServerResolutionRank converts a resolution label to a comparable rank.
// Used by both Plex and Jellyfin scan handlers.
func mediaServerResolutionRank(r string) int {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "4k", "2160":
		return 4
	case "1080":
		return 3
	case "720":
		return 2
	default:
		return 1
	}
}

// plexResolutionExceeds reports whether a Plex video resolution string exceeds
// the given maxResolution cap. Empty maxResolution means no cap.
func plexResolutionExceeds(itemRes, maxRes string) bool {
	if maxRes == "" {
		return false
	}
	return mediaServerResolutionRank(itemRes) > mediaServerResolutionRank(maxRes)
}

func (a *App) runPlexScan(job *ingestJob, items []plex.Item, mapper *plex.PathMapper) {
	ctx := job.ctx
	acc := lcingest.Result{}
	job.setTotal(len(items))
	cancelled := false
	for _, it := range items {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		mapped := mapper.Map(it.Path)
		res, err := lcingest.IngestFile(ctx, a.dbConn, mapped, job)
		job.OnProgress()
		if err != nil {
			if ctx.Err() != nil {
				cancelled = true
				break
			}
			job.Printf("error ingesting %s: %v", mapped, err)
			acc.Total++
			acc.Failed++
			acc.Failures = append(acc.Failures, fmt.Sprintf("%s: %v", mapped, err))
			continue
		}
		acc.Total += res.Total
		acc.Passed += res.Passed
		acc.Failed += res.Failed
		acc.Failures = append(acc.Failures, res.Failures...)
	}
	if cancelled {
		job.finalize("cancelled", "scan cancelled", &acc)
		return
	}
	job.finalize("done", "", &acc)
}
