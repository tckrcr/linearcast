package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var plexTVBaseURL = "https://plex.tv"

const plexOAuthClientID = "linearcast-admin"

type plexPinStartResponse struct {
	ID      int    `json:"id"`
	Code    string `json:"code"`
	AuthURL string `json:"authUrl"`
}

type plexPinPollResponse struct {
	Authorized bool                   `json:"authorized"`
	Username   string                 `json:"username,omitempty"`
	Servers    []plexServerConnection `json:"servers,omitempty"`
}

type plexServerConnection struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
	Local bool   `json:"local"`
}

type plexPinCreateResponse struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
}

type plexPinCheckResponse struct {
	AuthToken string `json:"authToken"`
}

type plexResourcesResponse []struct {
	Name        string `json:"name"`
	Product     string `json:"product"`
	Provides    string `json:"provides"`
	AccessToken string `json:"accessToken"`
	Connections []struct {
		URI   string `json:"uri"`
		Local bool   `json:"local"`
		Relay bool   `json:"relay"`
	} `json:"connections"`
}

func (a *App) handlePlexPinStart(w http.ResponseWriter, r *http.Request) {
	pin, err := a.createPlexPIN(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_pin_error", "failed to start Plex sign-in")
		return
	}
	authURL := "https://app.plex.tv/auth#?" + url.Values{
		"clientID":                 {plexOAuthClientID},
		"code":                     {pin.Code},
		"context[device][product]": {"linearcast"},
	}.Encode()
	writeJSON(w, plexPinStartResponse{ID: pin.ID, Code: pin.Code, AuthURL: authURL})
}

func (a *App) handlePlexPinPoll(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if id == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing_pin", "Plex PIN id and code are required")
		return
	}
	token, err := a.checkPlexPIN(r.Context(), id, code)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_pin_error", "failed to check Plex sign-in")
		return
	}
	if token == "" {
		writeJSON(w, plexPinPollResponse{Authorized: false})
		return
	}
	servers, username, err := a.fetchPlexServerConnections(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_resources_error", "Plex sign-in succeeded, but servers could not be loaded")
		return
	}
	writeJSON(w, plexPinPollResponse{Authorized: true, Username: username, Servers: servers})
}

func (a *App) createPlexPIN(ctx context.Context) (plexPinCreateResponse, error) {
	var out plexPinCreateResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(plexTVBaseURL, "/")+"/api/v2/pins?strong=true", nil)
	if err != nil {
		return out, err
	}
	setPlexTVHeaders(req)
	if err := a.doPlexTVJSON(req, &out); err != nil {
		return out, err
	}
	if out.ID == 0 || out.Code == "" {
		return out, fmt.Errorf("plex pin response missing id/code")
	}
	return out, nil
}

func (a *App) checkPlexPIN(ctx context.Context, id, code string) (string, error) {
	u := strings.TrimRight(plexTVBaseURL, "/") + "/api/v2/pins/" + url.PathEscape(id) + "?" + url.Values{"code": {code}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	setPlexTVHeaders(req)
	var out plexPinCheckResponse
	if err := a.doPlexTVJSON(req, &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.AuthToken), nil
}

func (a *App) fetchPlexServerConnections(ctx context.Context, accountToken string) ([]plexServerConnection, string, error) {
	u := strings.TrimRight(plexTVBaseURL, "/") + "/api/v2/resources?" + url.Values{
		"includeHttps": {"1"},
		"includeRelay": {"0"},
		"X-Plex-Token": {accountToken},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	setPlexTVHeaders(req)
	var resources plexResourcesResponse
	if err := a.doPlexTVJSON(req, &resources); err != nil {
		return nil, "", err
	}
	var out []plexServerConnection
	username := ""
	seen := map[string]bool{}
	for _, res := range resources {
		if !strings.Contains(res.Provides, "server") {
			continue
		}
		serverToken := strings.TrimSpace(res.AccessToken)
		if serverToken == "" {
			serverToken = accountToken
		}
		for _, conn := range res.Connections {
			serverURL := strings.TrimRight(strings.TrimSpace(conn.URI), "/")
			if serverURL == "" || conn.Relay || seen[serverURL] {
				continue
			}
			seen[serverURL] = true
			out = append(out, plexServerConnection{Name: res.Name, URL: serverURL, Token: serverToken, Local: conn.Local})
		}
	}
	return out, username, nil
}

func setPlexTVHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", "linearcast")
	req.Header.Set("X-Plex-Client-Identifier", plexOAuthClientID)
}

func (a *App) doPlexTVJSON(req *http.Request, out any) error {
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("plex.tv %s: status %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
