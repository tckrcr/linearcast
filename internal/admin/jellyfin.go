package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/jellyfin"
	"github.com/tckrcr/linearcast/internal/lcingest"
	"github.com/tckrcr/linearcast/internal/plex"
)

type jellyfinStatusResponse struct {
	Connected  bool   `json:"connected"`
	Configured bool   `json:"configured"`
	URL        string `json:"url,omitempty"`
	ServerName string `json:"serverName,omitempty"`
	Version    string `json:"version,omitempty"`
	PathMap    string `json:"pathMap,omitempty"`
}

type jellyfinConfigRequest struct {
	URL     string `json:"url"`
	APIKey  string `json:"apiKey"`
	PathMap string `json:"pathMap,omitempty"`
}

func (a *App) handleAdminJellyfinStatus(w http.ResponseWriter, r *http.Request) {
	baseURL, err := a.effectiveJellyfinURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin url")
		return
	}
	apiKey, err := db.GetJellyfinAPIKey(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin api key")
		return
	}
	pathMap, err := a.effectiveJellyfinPathMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin path map")
		return
	}
	if baseURL == "" || apiKey == "" {
		writeJSON(w, jellyfinStatusResponse{Connected: false, Configured: apiKey != "", URL: baseURL, PathMap: pathMap})
		return
	}
	status, err := a.checkJellyfin(baseURL, apiKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "jellyfin_status_error", a.jellyfinConnectivityMessage(baseURL, err))
		return
	}
	status.PathMap = pathMap
	writeJSON(w, status)
}

func (a *App) handleAdminJellyfinConfigSet(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req jellyfinConfigRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	baseURL, err := normalizeAbsoluteHTTPURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_jellyfin_url", err.Error())
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		writeError(w, http.StatusBadRequest, "empty_api_key", "api key is required")
		return
	}
	pathMap := strings.TrimSpace(req.PathMap)

	status, err := a.checkJellyfin(baseURL, apiKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "jellyfin_connectivity_error", a.jellyfinConnectivityMessage(baseURL, err))
		return
	}
	if err := db.SetJellyfinURL(r.Context(), a.dbConn, baseURL); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store jellyfin url")
		return
	}
	if err := db.SetJellyfinAPIKey(r.Context(), a.dbConn, apiKey); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store jellyfin config")
		return
	}
	if err := db.SetJellyfinPathMap(r.Context(), a.dbConn, pathMap); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store jellyfin path map")
		return
	}
	status.PathMap = pathMap
	writeJSON(w, status)
}

func (a *App) handleAdminJellyfinConfigClear(w http.ResponseWriter, r *http.Request) {
	baseURL, err := a.effectiveJellyfinURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin url")
		return
	}
	if err := db.ClearJellyfinAPIKey(r.Context(), a.dbConn); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to clear jellyfin api key")
		return
	}
	pathMap, _ := a.effectiveJellyfinPathMap()
	writeJSON(w, jellyfinStatusResponse{Connected: false, Configured: false, URL: baseURL, PathMap: pathMap})
}

func (a *App) effectiveJellyfinURL() (string, error) {
	stored, err := db.GetJellyfinURL(context.Background(), a.dbConn)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	return strings.TrimRight(strings.TrimSpace(a.jellyfinURL), "/"), nil
}

func (a *App) checkJellyfin(baseURL, apiKey string) (jellyfinStatusResponse, error) {
	client := jellyfin.New(baseURL, apiKey)
	client.HTTPClient = a.httpClient
	status, err := client.Status()
	if err != nil {
		return jellyfinStatusResponse{}, err
	}
	return jellyfinStatusResponse{
		Connected:  true,
		Configured: true,
		URL:        baseURL,
		ServerName: status.ServerName,
		Version:    status.Version,
	}, nil
}

func (a *App) jellyfinConnectivityMessage(baseURL string, err error) string {
	if strings.TrimSpace(baseURL) == "" {
		return "Jellyfin URL is not configured. Set JELLYFIN_URL or save a URL in Admin Tools."
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403") {
			return "Jellyfin rejected the API key. Replace it with a valid API key or sign out."
		}
	}
	return "Could not connect to Jellyfin. Check the Jellyfin URL and that the server is reachable, then try again."
}

type jellyfinScanRequest struct {
	LibraryID     string `json:"libraryId"`
	MaxResolution string `json:"maxResolution,omitempty"` // "", "1080", "720"
}

func (a *App) handleJellyfinLibraries(w http.ResponseWriter, r *http.Request) {
	baseURL, err := a.effectiveJellyfinURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin url")
		return
	}
	apiKey, err := db.GetJellyfinAPIKey(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin api key")
		return
	}
	if baseURL == "" || apiKey == "" {
		writeError(w, http.StatusBadRequest, "not_configured", "Jellyfin is not connected")
		return
	}
	client := jellyfin.New(baseURL, apiKey)
	client.HTTPClient = a.httpClient
	libs, err := client.Libraries()
	if err != nil {
		writeError(w, http.StatusBadGateway, "jellyfin_error", err.Error())
		return
	}
	type libEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	out := make([]libEntry, 0, len(libs))
	for _, l := range libs {
		t := l.Type
		if t != "movies" && t != "tvshows" {
			continue
		}
		out = append(out, libEntry{ID: l.ID, Name: l.Name, Type: t})
	}
	writeJSON(w, out)
}

func (a *App) handleJellyfinScan(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req jellyfinScanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.LibraryID = strings.TrimSpace(req.LibraryID)
	req.MaxResolution = strings.TrimSpace(req.MaxResolution)
	if req.LibraryID == "" {
		writeError(w, http.StatusBadRequest, "missing_library", "libraryId is required")
		return
	}

	baseURL, err := a.effectiveJellyfinURL()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin url")
		return
	}
	apiKey, err := db.GetJellyfinAPIKey(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin api key")
		return
	}
	pathMapStr, err := a.effectiveJellyfinPathMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read jellyfin path map")
		return
	}
	mapper, err := plex.ParsePathMap(pathMapStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "jellyfin_path_map_error", err.Error())
		return
	}

	client := jellyfin.New(baseURL, apiKey)
	client.HTTPClient = a.httpClient
	items, err := client.Items(req.LibraryID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "jellyfin_error", err.Error())
		return
	}

	// Filter by max resolution using item height.
	maxHeight := jellyfinMaxHeight(req.MaxResolution)
	filtered := make([]jellyfin.Item, 0, len(items))
	for _, it := range items {
		if maxHeight > 0 && it.Height > maxHeight {
			continue
		}
		filtered = append(filtered, it)
	}

	jobID, job := a.ingestJobs.create()
	go a.runJellyfinScan(job, filtered, mapper)

	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

// jellyfinMaxHeight returns the max allowed video height for a given
// maxResolution label. 0 means no cap.
func jellyfinMaxHeight(maxRes string) int {
	switch strings.ToLower(strings.TrimSpace(maxRes)) {
	case "1080":
		return 1440 // skip true 4K (2160) but allow 1080p
	case "720":
		return 900 // skip 1080 and above
	default:
		return 0
	}
}

func (a *App) runJellyfinScan(job *ingestJob, items []jellyfin.Item, mapper *plex.PathMapper) {
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

func normalizeAbsoluteHTTPURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", errRequiredURL{}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errInvalidURL{}
	}
	return raw, nil
}

type errRequiredURL struct{}

func (errRequiredURL) Error() string {
	return "url is required"
}

type errInvalidURL struct{}

func (errInvalidURL) Error() string {
	return "url must be an absolute http or https URL"
}
