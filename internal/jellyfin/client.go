// Package jellyfin is a minimal Jellyfin API client for admin connection
// checks and future media-library ingest.
package jellyfin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type Status struct {
	ServerName string
	Version    string
	ID         string
}

type systemInfoResponse struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
	ID         string `json:"Id"`
}

func (c *Client) Status() (Status, error) {
	var resp systemInfoResponse
	if err := c.getJSON("/System/Info", &resp); err != nil {
		return Status{}, err
	}
	return Status{
		ServerName: resp.ServerName,
		Version:    resp.Version,
		ID:         resp.ID,
	}, nil
}

// Library is a top-level Jellyfin media folder.
type Library struct {
	ID   string
	Name string
	Type string // "movies", "tvshows", "homevideos", etc.
}

// Item is one Jellyfin media item (a movie or episode).
// Path is taken from the first MediaSource. Height is from the first
// MediaSource's video dimensions, used for resolution filtering.
type Item struct {
	ID            string
	Name          string
	Type          string // "Movie" or "Episode"
	SeriesName    string // show title, for episodes
	SeasonNumber  int    // for episodes
	EpisodeNumber int    // for episodes
	Year          int    // for movies
	Path          string
	Height        int // video height in pixels, 0 if unknown
}

type mediaFoldersResponse struct {
	Items []struct {
		ID             string `json:"Id"`
		Name           string `json:"Name"`
		CollectionType string `json:"CollectionType"`
	} `json:"Items"`
}

type itemsResponse struct {
	Items []rawItem `json:"Items"`
}

type rawItem struct {
	ID                string        `json:"Id"`
	Name              string        `json:"Name"`
	Type              string        `json:"Type"`
	SeriesName        string        `json:"SeriesName"`
	ParentIndexNumber int           `json:"ParentIndexNumber"`
	IndexNumber       int           `json:"IndexNumber"`
	ProductionYear    int           `json:"ProductionYear"`
	MediaSources      []mediaSource `json:"MediaSources"`
}

type mediaSource struct {
	Path   string `json:"Path"`
	Width  int    `json:"Width"`
	Height int    `json:"Height"`
}

// Libraries returns all top-level media folders.
func (c *Client) Libraries() ([]Library, error) {
	var resp mediaFoldersResponse
	if err := c.getJSON("/Library/MediaFolders", &resp); err != nil {
		return nil, err
	}
	out := make([]Library, 0, len(resp.Items))
	for _, item := range resp.Items {
		out = append(out, Library{
			ID:   item.ID,
			Name: item.Name,
			Type: item.CollectionType,
		})
	}
	return out, nil
}

// Items returns all Movie and Episode items under libraryID, recursively.
// Path is taken from the first MediaSource; items with no path are dropped.
func (c *Client) Items(libraryID string) ([]Item, error) {
	q := "ParentId=" + libraryID +
		"&Recursive=true" +
		"&IncludeItemTypes=Movie%2CEpisode" +
		"&Fields=Path%2CMediaSources%2CParentIndexNumber%2CIndexNumber%2CProductionYear"
	var resp itemsResponse
	if err := c.getJSON("/Items?"+q, &resp); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(resp.Items))
	for _, r := range resp.Items {
		path := ""
		height := 0
		if len(r.MediaSources) > 0 {
			path = r.MediaSources[0].Path
			height = r.MediaSources[0].Height
		}
		if path == "" {
			continue
		}
		out = append(out, Item{
			ID:            r.ID,
			Name:          r.Name,
			Type:          r.Type,
			SeriesName:    r.SeriesName,
			SeasonNumber:  r.ParentIndexNumber,
			EpisodeNumber: r.IndexNumber,
			Year:          r.ProductionYear,
			Path:          path,
			Height:        height,
		})
	}
	return out, nil
}

func (c *Client) getJSON(path string, out any) error {
	if c.BaseURL == "" {
		return fmt.Errorf("jellyfin: BaseURL is empty (set JELLYFIN_URL)")
	}
	if c.APIKey == "" {
		return fmt.Errorf("jellyfin: APIKey is empty")
	}
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Emby-Token", c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin GET %s: %w", path, sanitizeRequestError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("jellyfin GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func sanitizeRequestError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
