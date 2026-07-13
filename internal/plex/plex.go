// Package plex is a minimal client for the Plex Media Server HTTP API,
// scoped to the needs of the admin Plex library-walk/import flow:
// list library sections, list items in a section filtered by resolution,
// and translate Plex-side file paths to server-side mount paths.
//
// Auth uses the X-Plex-Token query parameter. Responses are requested as
// JSON via the Accept header. Only the fields we use are decoded.
package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/mediasource"
)

// Client is a Plex HTTP client.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// New returns a Client with a 30s timeout HTTP client.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Section is one Plex library section.
type Section struct {
	Key   string // numeric, e.g. "1"
	Title string // e.g. "TV Shows"
	Type  string // "show", "movie"
}

type rootResponse struct {
	MediaContainer struct {
		FriendlyName   string `json:"friendlyName"`
		ServerName     string `json:"serverName"`
		MyPlexUsername string `json:"myPlexUsername"`
		Username       string `json:"username"`
	} `json:"MediaContainer"`
}

type sectionsResponse struct {
	MediaContainer struct {
		Directory []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
			Type  string `json:"type"`
		} `json:"Directory"`
	} `json:"MediaContainer"`
}

type itemsResponse struct {
	MediaContainer struct {
		Metadata []rawMeta `json:"Metadata"`
	} `json:"MediaContainer"`
}

type rawMeta struct {
	RatingKey        string     `json:"ratingKey"`
	Title            string     `json:"title"`
	Type             string     `json:"type"`
	GrandparentTitle string     `json:"grandparentTitle"`
	ParentIndex      int        `json:"parentIndex"`
	Index            int        `json:"index"`
	Year             int        `json:"year"`
	Summary          string     `json:"summary"`
	Thumb            string     `json:"thumb"`
	ContentRating    string     `json:"contentRating"`
	Genre            []rawTag   `json:"Genre"`
	Media            []rawMedia `json:"Media"`
}

type rawTag struct {
	Tag string `json:"tag"`
}

type rawMedia struct {
	VideoResolution string    `json:"videoResolution"`
	Part            []rawPart `json:"Part"`
}

type rawPart struct {
	File string `json:"file"`
}

// Sections returns all library sections.
func (c *Client) Sections() ([]Section, error) {
	var resp sectionsResponse
	if err := c.getJSON(context.Background(), "/library/sections", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Section, 0, len(resp.MediaContainer.Directory))
	for _, d := range resp.MediaContainer.Directory {
		out = append(out, Section{Key: d.Key, Title: d.Title, Type: d.Type})
	}
	return out, nil
}

// FindSection resolves a section by exact title match (case-insensitive)
// or by numeric key. Returns (nil, nil) if not found.
func (c *Client) FindSection(nameOrKey string) (*Section, error) {
	all, err := c.Sections()
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(nameOrKey))
	for _, s := range all {
		if s.Key == nameOrKey || strings.ToLower(s.Title) == want {
			s := s
			return &s, nil
		}
	}
	return nil, nil
}

// String renders a Section for human output.
func (s Section) String() string {
	return fmt.Sprintf("%s\t%s\t%s", s.Key, s.Type, s.Title)
}

// Name returns the source kind identifier.
func (c *Client) Name() string {
	return "plex"
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	if c.BaseURL == "" {
		return fmt.Errorf("plex: BaseURL is empty (set PLEX_URL)")
	}
	if c.Token == "" {
		return fmt.Errorf("plex: Token is empty (configure Plex in the admin UI/API)")
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("X-Plex-Token", c.Token)

	u := c.BaseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex GET %s: %w", path, sanitizeRequestError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("plex GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
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

// PathMapper rewrites Plex-side paths into server-side paths.
//
// Configured via a string of `plex=server` pairs separated by `,` or `;`,
// e.g. `/data/tv=/data/media/tv,/data/movies=/data/media/movies`.
// Longest plex-prefix wins. Paths that match no prefix are returned
// unchanged.
type PathMapper struct {
	pairs []pair
}

type pair struct {
	plex   string
	server string
}

// ParsePathMap parses a `;`-separated list of `plex=server` mappings.
// Empty input yields a passthrough mapper.
func ParsePathMap(spec string) (*PathMapper, error) {
	pm := &PathMapper{}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return pm, nil
	}
	splitter := func(r rune) bool { return r == ',' || r == ';' }
	for _, raw := range strings.FieldsFunc(spec, splitter) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		eq := strings.Index(raw, "=")
		if eq <= 0 || eq == len(raw)-1 {
			return nil, fmt.Errorf("path-map entry %q: expected plex=server", raw)
		}
		pm.pairs = append(pm.pairs, pair{
			plex:   strings.TrimRight(strings.TrimSpace(raw[:eq]), "/"),
			server: strings.TrimRight(strings.TrimSpace(raw[eq+1:]), "/"),
		})
	}
	sort.SliceStable(pm.pairs, func(i, j int) bool {
		return len(pm.pairs[i].plex) > len(pm.pairs[j].plex)
	})
	return pm, nil
}

// Map returns p rewritten using the longest matching plex prefix, or p
// unchanged if no prefix matches.
func (m *PathMapper) Map(p string) string {
	if m == nil {
		return p
	}
	// Pairs are sorted longest-first at parse time so a narrow Plex mount wins
	// over a broader fallback mount.
	for _, pr := range m.pairs {
		if p == pr.plex {
			return pr.server
		}
		if strings.HasPrefix(p, pr.plex+"/") {
			return pr.server + p[len(pr.plex):]
		}
	}
	return p
}

// Status checks connectivity and returns server info.
func (c *Client) Status(ctx context.Context) (mediasource.Status, error) {
	var resp rootResponse
	if err := c.getJSON(ctx, "/", nil, &resp); err != nil {
		return mediasource.Status{}, err
	}
	status := mediasource.Status{
		Connected:  true,
		ServerName: resp.MediaContainer.FriendlyName,
		Username:   resp.MediaContainer.MyPlexUsername,
		Version:    "",
		ID:         "",
		URL:        c.BaseURL,
		PathMap:    "",
	}
	if status.ServerName == "" {
		status.ServerName = resp.MediaContainer.ServerName
	}
	if status.Username == "" {
		status.Username = resp.MediaContainer.Username
	}
	return status, nil
}

// Libraries returns all available libraries/sections.
func (c *Client) Libraries(ctx context.Context) ([]mediasource.Library, error) {
	var resp sectionsResponse
	if err := c.getJSON(ctx, "/library/sections", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]mediasource.Library, 0, len(resp.MediaContainer.Directory))
	for _, d := range resp.MediaContainer.Directory {
		libType := d.Type
		if libType == "show" {
			libType = "shows"
		} else if libType == "movie" {
			libType = "movies"
		}
		out = append(out, mediasource.Library{
			ID:   d.Key,
			Name: d.Title,
			Type: libType,
		})
	}
	return out, nil
}

// Items returns items in a library, filtered by options.
func (c *Client) Items(ctx context.Context, libraryID string, opts mediasource.ScanOptions) ([]mediasource.Item, error) {
	sections, err := c.Sections()
	if err != nil {
		return nil, err
	}
	var section *Section
	for i := range sections {
		if sections[i].Key == libraryID {
			section = &sections[i]
			break
		}
	}
	if section == nil {
		return nil, fmt.Errorf("section not found: %s", libraryID)
	}

	q := url.Values{}
	switch section.Type {
	case "show":
		q.Set("type", "4") // episode
	case "movie":
		q.Set("type", "1")
	default:
		return nil, fmt.Errorf("unsupported section type %q (only show/movie)", section.Type)
	}

	var resp itemsResponse
	if err := c.getJSON(ctx, "/library/sections/"+section.Key+"/all", q, &resp); err != nil {
		return nil, err
	}

	out := make([]mediasource.Item, 0, len(resp.MediaContainer.Metadata))
	for _, m := range resp.MediaContainer.Metadata {
		path := ""
		res := ""
		height := 0
		if len(m.Media) > 0 {
			res = strings.ToLower(m.Media[0].VideoResolution)
			if len(m.Media[0].Part) > 0 {
				path = m.Media[0].Part[0].File
			}
		}
		if path == "" {
			continue
		}
		if plexResolutionExceeds(res, opts.MaxResolution) {
			continue
		}
		itemType := "movie"
		if m.Type == "episode" {
			itemType = "episode"
		}
		sourceRef := ""
		if m.RatingKey != "" {
			sourceRef = "plex://" + m.RatingKey
		}
		out = append(out, mediasource.Item{
			ID:            m.RatingKey,
			Title:         m.Title,
			Type:          itemType,
			SeriesName:    m.GrandparentTitle,
			SeasonNumber:  m.ParentIndex,
			EpisodeNumber: m.Index,
			Year:          m.Year,
			Description:   m.Summary,
			ThumbnailPath: m.Thumb,
			ContentRating: m.ContentRating,
			Genres:        plexTags(m.Genre),
			Path:          path,
			Resolution:    res,
			Height:        height,
			SourceRef:     sourceRef,
		})
	}
	return out, nil
}

func plexTags(tags []rawTag) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if s := strings.TrimSpace(tag.Tag); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func plexResolutionExceeds(itemRes, maxRes string) bool {
	if maxRes == "" {
		return false
	}
	return plexResolutionRank(itemRes) > plexResolutionRank(maxRes)
}

func plexResolutionRank(r string) int {
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

// Close releases any resources held by the client.
func (c *Client) Close() error {
	return nil
}

// compile-time interface check
var _ mediasource.MediaSourceClient = (*Client)(nil)
