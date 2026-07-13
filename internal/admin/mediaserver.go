package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/jellyfin"
	"github.com/tckrcr/linearcast/internal/lcingest"
	"github.com/tckrcr/linearcast/internal/mediasource"
	"github.com/tckrcr/linearcast/internal/plex"
)

type mediaServerStatusResponse struct {
	Connected  bool   `json:"connected"`
	Configured bool   `json:"configured,omitempty"`
	Username   string `json:"username,omitempty"`
	ServerName string `json:"serverName,omitempty"`
	Version    string `json:"version,omitempty"`
	URL        string `json:"url,omitempty"`
	PathMap    string `json:"pathMap,omitempty"`
}

type mediaServerConfigRequest struct {
	URL     string `json:"url"`
	Token   string `json:"token,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
	PathMap string `json:"pathMap,omitempty"`
}

type mediaServerScanRequest struct {
	LibraryKey    string `json:"libraryKey,omitempty"`
	LibraryID     string `json:"libraryId,omitempty"`
	MaxResolution string `json:"maxResolution,omitempty"`
}

type mediaServerLibraryEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type serverType struct {
	name            string
	credentialField string

	effectiveURL     func(*App) (string, error)
	effectivePathMap func(*App) (string, error)
	newClient        func(url, credential string) mediasource.MediaSourceClient

	credentialKey   string
	setCredential   func(context.Context, *sql.DB, string) error
	clearCredential func(context.Context, *sql.DB) error

	setURL     func(context.Context, *sql.DB, string) error
	setPathMap func(context.Context, *sql.DB, string) error

	persistIdentity func(ctx context.Context, conn *sql.DB, status mediasource.Status) error
	loadIdentity    func(ctx context.Context, conn *sql.DB, resp *mediaServerStatusResponse)
	deleteIdentity  func(context.Context, *sql.DB) error

	connectivityMessage func(url string, err error) string
	stampsSourceRef     bool
}

var plexServer = &serverType{
	name:            "plex",
	credentialField: "token",
	effectiveURL: func(a *App) (string, error) {
		s, err := db.GetPlexURL(context.Background(), a.dbConn)
		if err != nil {
			return "", err
		}
		if s != "" {
			return s, nil
		}
		return strings.TrimRight(strings.TrimSpace(a.plexURL), "/"), nil
	},
	effectivePathMap: func(a *App) (string, error) {
		s, err := db.GetPlexPathMap(context.Background(), a.dbConn)
		if err != nil {
			return "", err
		}
		if s != "" {
			return s, nil
		}
		return strings.TrimSpace(a.plexPathMap), nil
	},
	newClient: func(u, c string) mediasource.MediaSourceClient {
		return plex.New(u, c)
	},

	credentialKey: "plex_token",
	setCredential: func(ctx context.Context, conn *sql.DB, v string) error {
		return db.SetPlexToken(ctx, conn, v)
	},
	clearCredential: func(ctx context.Context, conn *sql.DB) error {
		return db.ClearPlexToken(ctx, conn)
	},
	setURL:     func(ctx context.Context, conn *sql.DB, v string) error { return db.SetPlexURL(ctx, conn, v) },
	setPathMap: func(ctx context.Context, conn *sql.DB, v string) error { return db.SetPlexPathMap(ctx, conn, v) },

	persistIdentity: func(ctx context.Context, conn *sql.DB, s mediasource.Status) error {
		if err := db.SetPlexServerName(ctx, conn, s.ServerName); err != nil {
			return err
		}
		return db.SetPlexUsername(ctx, conn, s.Username)
	},
	loadIdentity: func(ctx context.Context, conn *sql.DB, resp *mediaServerStatusResponse) {
		resp.Username, _ = db.GetPlexUsername(ctx, conn)
		resp.ServerName, _ = db.GetPlexServerName(ctx, conn)
	},
	deleteIdentity: func(ctx context.Context, conn *sql.DB) error {
		return db.DeletePlexIdentity(ctx, conn)
	},

	connectivityMessage: func(u string, err error) string {
		if strings.TrimSpace(u) == "" {
			return "Plex URL is not configured. Enter the URL in Admin Tools."
		}
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403") {
				return "Plex rejected the token. Replace it with a valid Plex token or sign out."
			}
		}
		return "Could not connect to Plex. Check the URL and that Plex Media Server is reachable, then try again."
	},
	stampsSourceRef: true,
}

var jellyfinServer = &serverType{
	name:            "jellyfin",
	credentialField: "apiKey",
	effectiveURL: func(a *App) (string, error) {
		s, err := db.GetJellyfinURL(context.Background(), a.dbConn)
		if err != nil {
			return "", err
		}
		if s != "" {
			return s, nil
		}
		return strings.TrimRight(strings.TrimSpace(a.jellyfinURL), "/"), nil
	},
	effectivePathMap: func(a *App) (string, error) {
		s, err := db.GetJellyfinPathMap(context.Background(), a.dbConn)
		if err != nil {
			return "", err
		}
		if s != "" {
			return s, nil
		}
		return strings.TrimSpace(a.jellyfinPathMap), nil
	},
	newClient: func(u, c string) mediasource.MediaSourceClient {
		return jellyfin.New(u, c)
	},

	credentialKey: "jellyfin_api_key",
	setCredential: func(ctx context.Context, conn *sql.DB, v string) error {
		return db.SetJellyfinAPIKey(ctx, conn, v)
	},
	clearCredential: func(ctx context.Context, conn *sql.DB) error {
		return db.ClearJellyfinAPIKey(ctx, conn)
	},
	setURL:     func(ctx context.Context, conn *sql.DB, v string) error { return db.SetJellyfinURL(ctx, conn, v) },
	setPathMap: func(ctx context.Context, conn *sql.DB, v string) error { return db.SetJellyfinPathMap(ctx, conn, v) },

	persistIdentity: func(ctx context.Context, conn *sql.DB, s mediasource.Status) error {
		if err := db.SetJellyfinServerName(ctx, conn, s.ServerName); err != nil {
			return err
		}
		return db.SetJellyfinVersion(ctx, conn, s.Version)
	},
	loadIdentity: func(ctx context.Context, conn *sql.DB, resp *mediaServerStatusResponse) {
		resp.ServerName, _ = db.GetJellyfinServerName(ctx, conn)
		resp.Version, _ = db.GetJellyfinVersion(ctx, conn)
		resp.Configured = true
	},
	deleteIdentity: func(ctx context.Context, conn *sql.DB) error {
		return db.DeleteJellyfinIdentity(ctx, conn)
	},

	connectivityMessage: func(u string, err error) string {
		if strings.TrimSpace(u) == "" {
			return "Jellyfin URL is not configured. Set JELLYFIN_URL or save a URL in Admin Tools."
		}
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403") {
				return "Jellyfin rejected the API key. Replace it with a valid API key or sign out."
			}
		}
		return "Could not connect to Jellyfin. Check the Jellyfin URL and that the server is reachable, then try again."
	},
	stampsSourceRef: false,
}

func (a *App) getServerCredential(ctx context.Context, st *serverType) (string, error) {
	switch st.credentialKey {
	case "plex_token":
		return db.GetPlexToken(ctx, a.dbConn)
	case "jellyfin_api_key":
		return db.GetJellyfinAPIKey(ctx, a.dbConn)
	}
	return "", nil
}

// --- Shared handler implementations ---

func (a *App) handleMediaServerStatus(w http.ResponseWriter, r *http.Request, st *serverType) {
	url, err := st.effectiveURL(a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" url")
		return
	}
	pathMap, err := st.effectivePathMap(a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" path map")
		return
	}
	cred, err := a.getServerCredential(r.Context(), st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" credential")
		return
	}

	resp := mediaServerStatusResponse{Connected: cred != "", URL: url, PathMap: pathMap}
	if resp.Connected {
		st.loadIdentity(r.Context(), a.dbConn, &resp)
	}
	writeJSON(w, resp)
}

func (a *App) handleMediaServerConfigSet(w http.ResponseWriter, r *http.Request, st *serverType) {
	defer r.Body.Close()
	var req mediaServerConfigRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	serverURL, err := normalizeAbsoluteHTTPURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_"+st.name+"_url", err.Error())
		return
	}

	var credential string
	switch st.credentialField {
	case "token":
		credential = strings.TrimSpace(req.Token)
	case "apiKey":
		credential = strings.TrimSpace(req.APIKey)
	}
	if credential == "" {
		friendly := st.credentialField
		if friendly == "apiKey" {
			friendly = "api key"
		}
		writeError(w, http.StatusBadRequest, "empty_"+strings.ReplaceAll(st.credentialField, "Key", "_key"), friendly+" is required")
		return
	}

	pathMap := strings.TrimSpace(req.PathMap)

	client := st.newClient(serverURL, credential)
	if c, ok := client.(*plex.Client); ok {
		c.HTTPClient = a.httpClient
	} else if c, ok := client.(*jellyfin.Client); ok {
		c.HTTPClient = a.httpClient
	}

	status, err := client.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, st.name+"_connectivity_error", st.connectivityMessage(serverURL, err))
		return
	}

	if err := st.setURL(r.Context(), a.dbConn, serverURL); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store "+st.name+" url")
		return
	}
	if err := st.setCredential(r.Context(), a.dbConn, credential); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store "+st.name+" credential")
		return
	}
	if err := st.setPathMap(r.Context(), a.dbConn, pathMap); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store "+st.name+" path map")
		return
	}
	if err := st.persistIdentity(r.Context(), a.dbConn, status); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to store "+st.name+" identity")
		return
	}

	resp := mediaServerStatusResponse{
		Connected:  true,
		URL:        serverURL,
		PathMap:    pathMap,
		ServerName: status.ServerName,
	}
	if st.name == "jellyfin" {
		resp.Configured = true
		resp.Version = status.Version
	} else {
		resp.Username = status.Username
	}

	writeJSON(w, resp)
}

func (a *App) handleMediaServerConfigClear(w http.ResponseWriter, r *http.Request, st *serverType) {
	if err := st.clearCredential(r.Context(), a.dbConn); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to clear "+st.name+" credential")
		return
	}
	if err := st.deleteIdentity(r.Context(), a.dbConn); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to clear "+st.name+" identity")
		return
	}
	url, _ := st.effectiveURL(a)
	pathMap, _ := st.effectivePathMap(a)
	writeJSON(w, mediaServerStatusResponse{Connected: false, URL: url, PathMap: pathMap})
}

func (a *App) handleMediaServerLibraries(w http.ResponseWriter, r *http.Request, st *serverType) {
	serverURL, err := st.effectiveURL(a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" url")
		return
	}
	cred, err := a.getServerCredential(r.Context(), st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" credential")
		return
	}
	if serverURL == "" || cred == "" {
		writeError(w, http.StatusBadRequest, "not_configured", st.name+" is not connected")
		return
	}

	client := st.newClient(serverURL, cred)
	if c, ok := client.(*plex.Client); ok {
		c.HTTPClient = a.httpClient
	} else if c, ok := client.(*jellyfin.Client); ok {
		c.HTTPClient = a.httpClient
	}

	libs, err := client.Libraries(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, st.name+"_error", err.Error())
		return
	}

	out := make([]mediaServerLibraryEntry, 0, len(libs))
	for _, l := range libs {
		if l.Type != "movies" && l.Type != "shows" {
			continue
		}
		out = append(out, mediaServerLibraryEntry{ID: l.ID, Name: l.Name, Type: l.Type})
	}
	writeJSON(w, out)
}

func (a *App) handleMediaServerScan(w http.ResponseWriter, r *http.Request, st *serverType) {
	defer r.Body.Close()
	var req mediaServerScanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	libraryID := strings.TrimSpace(req.LibraryID)
	libraryKey := strings.TrimSpace(req.LibraryKey)
	maxRes := strings.TrimSpace(req.MaxResolution)

	// Accept either field; plex sends libraryKey, jellyfin sends libraryId.
	id := libraryID
	if id == "" {
		id = libraryKey
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_library", "library ID is required")
		return
	}

	serverURL, err := st.effectiveURL(a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" url")
		return
	}
	cred, err := a.getServerCredential(r.Context(), st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" credential")
		return
	}
	pathMapStr, err := st.effectivePathMap(a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read "+st.name+" path map")
		return
	}

	mapper, err := plex.ParsePathMap(pathMapStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, st.name+"_path_map_error", err.Error())
		return
	}

	client := st.newClient(serverURL, cred)
	if c, ok := client.(*plex.Client); ok {
		c.HTTPClient = a.httpClient
	} else if c, ok := client.(*jellyfin.Client); ok {
		c.HTTPClient = a.httpClient
	}

	items, err := client.Items(r.Context(), id, mediasource.ScanOptions{MaxResolution: maxRes})
	if err != nil {
		writeError(w, http.StatusBadGateway, st.name+"_error", err.Error())
		return
	}

	jobID, job := a.ingestJobs.create()
	go a.runMediaServerScan(job, items, mapper, st)

	w.Header().Set("Content-Type", "application/json")
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

func (a *App) runMediaServerScan(job *ingestJob, items []mediasource.Item, mapper *plex.PathMapper, st *serverType) {
	ctx := job.ctx
	job.setTotal(len(items))
	acc := lcingest.ScanPool(ctx, items, job, func(it mediasource.Item, res *lcingest.Result) {
		mapped := mapper.Map(it.Path)
		r, err := lcingest.IngestFileWithHints(ctx, a.dbConn, mapped, it.SeasonNumber, it.EpisodeNumber, job)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			job.Printf("error ingesting %s: %v", mapped, err)
			res.Add(lcingest.Result{
				Total:          1,
				Failed:         1,
				FailureReasons: map[string]int{err.Error(): 1},
			})
			return
		}
		if st.stampsSourceRef && it.SourceRef != "" {
			if sErr := db.SetMediaSourceRef(ctx, a.dbConn, mapped, it.SourceRef); sErr != nil {
				job.Printf("warning: failed to set source_ref for %s: %v", mapped, sErr)
			}
		}
		if st.name == "plex" || st.name == "jellyfin" {
			if sErr := db.UpdateMediaSourceMetadata(ctx, a.dbConn, mediaSourceMetadataForItem(st.name, mapped, it)); sErr != nil {
				job.Printf("warning: failed to update metadata for %s: %v", mapped, sErr)
			}
		}
		res.Add(r)
	})
	if ctx.Err() != nil {
		job.finalize("cancelled", "scan cancelled", &acc)
		return
	}
	job.finalize("done", "", &acc)
}

func mediaSourceMetadataForItem(source, path string, it mediasource.Item) db.MediaSourceMetadata {
	collectionName := strings.TrimSpace(it.SeriesName)
	collectionKind := "show"
	if it.Type == "movie" {
		collectionName = strings.TrimSpace(it.Title)
		collectionKind = "movie"
	}
	return db.MediaSourceMetadata{
		Path:           path,
		Source:         source,
		SourceRef:      it.SourceRef,
		Title:          it.Title,
		CollectionName: collectionName,
		CollectionKind: collectionKind,
		Description:    it.Description,
		ThumbPath:      it.ThumbnailPath,
		ContentRating:  it.ContentRating,
		Genres:         it.Genres,
	}
}

// --- Thin wrapper handlers ---

func (a *App) handleAdminPlexStatus(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerStatus(w, r, plexServer)
}

func (a *App) handleAdminPlexConfigSet(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerConfigSet(w, r, plexServer)
}

func (a *App) handleAdminPlexConfigClear(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerConfigClear(w, r, plexServer)
}

func (a *App) handlePlexLibraries(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerLibraries(w, r, plexServer)
}

func (a *App) handlePlexScan(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerScan(w, r, plexServer)
}

func (a *App) handleAdminJellyfinStatus(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerStatus(w, r, jellyfinServer)
}

func (a *App) handleAdminJellyfinConfigSet(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerConfigSet(w, r, jellyfinServer)
}

func (a *App) handleAdminJellyfinConfigClear(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerConfigClear(w, r, jellyfinServer)
}

func (a *App) handleJellyfinLibraries(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerLibraries(w, r, jellyfinServer)
}

func (a *App) handleJellyfinScan(w http.ResponseWriter, r *http.Request) {
	a.handleMediaServerScan(w, r, jellyfinServer)
}

// --- Shared helpers ---

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
