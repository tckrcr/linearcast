// Package jellyfin is a minimal Jellyfin API client for admin connection
// checks and future media-library ingest.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/mediasource"
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

type systemInfoResponse struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
	ID         string `json:"Id"`
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

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	if c.BaseURL == "" {
		return fmt.Errorf("jellyfin: BaseURL is empty (set JELLYFIN_URL)")
	}
	if c.APIKey == "" {
		return fmt.Errorf("jellyfin: APIKey is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
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

// Name returns the source kind identifier.
func (c *Client) Name() string {
	return "jellyfin"
}

// Status checks connectivity and returns server info.
func (c *Client) Status(ctx context.Context) (mediasource.Status, error) {
	var resp systemInfoResponse
	if err := c.getJSON(ctx, "/System/Info", &resp); err != nil {
		return mediasource.Status{}, err
	}
	return mediasource.Status{
		Connected:  true,
		ServerName: resp.ServerName,
		Version:    resp.Version,
		ID:         resp.ID,
		URL:        c.BaseURL,
		PathMap:    "",
	}, nil
}

// Libraries returns all available libraries/sections.
func (c *Client) Libraries(ctx context.Context) ([]mediasource.Library, error) {
	var resp mediaFoldersResponse
	if err := c.getJSON(ctx, "/Library/MediaFolders", &resp); err != nil {
		return nil, err
	}
	out := make([]mediasource.Library, 0, len(resp.Items))
	for _, item := range resp.Items {
		libType := item.CollectionType
		if libType == "tvshows" {
			libType = "shows"
		}
		out = append(out, mediasource.Library{
			ID:   item.ID,
			Name: item.Name,
			Type: libType,
		})
	}
	return out, nil
}

// Items returns items in a library, filtered by options.
func (c *Client) Items(ctx context.Context, libraryID string, opts mediasource.ScanOptions) ([]mediasource.Item, error) {
	maxHeight := jellyfinMaxHeight(opts.MaxResolution)

	q := "ParentId=" + libraryID +
		"&Recursive=true" +
		"&IncludeItemTypes=Movie%2CEpisode" +
		"&Fields=Path%2CMediaSources%2CParentIndexNumber%2CIndexNumber%2CProductionYear"
	var resp itemsResponse
	if err := c.getJSON(ctx, "/Items?"+q, &resp); err != nil {
		return nil, err
	}
	out := make([]mediasource.Item, 0, len(resp.Items))
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
		if maxHeight > 0 && height > maxHeight {
			continue
		}
		itemType := "movie"
		if r.Type == "Episode" {
			itemType = "episode"
		}
		sourceRef := ""
		if r.ID != "" {
			sourceRef = "jellyfin://" + r.ID
		}
		out = append(out, mediasource.Item{
			ID:             r.ID,
			Title:          r.Name,
			Type:           itemType,
			SeriesName:     r.SeriesName,
			SeasonNumber:   r.ParentIndexNumber,
			EpisodeNumber:  r.IndexNumber,
			Year:           r.ProductionYear,
			Path:           path,
			Resolution:     "",
			Height:         height,
			SourceRef:      sourceRef,
		})
	}
	return out, nil
}

// Close releases any resources held by the client.
func (c *Client) Close() error {
	return nil
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

// compile-time interface check
var _ mediasource.MediaSourceClient = (*Client)(nil)
