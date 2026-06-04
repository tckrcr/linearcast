// Package plex is a minimal client for the Plex Media Server HTTP API,
// scoped to the needs of the admin Plex library-walk/import flow:
// list library sections, list items in a section filtered by resolution,
// and translate Plex-side file paths to server-side mount paths.
//
// Auth uses the X-Plex-Token query parameter. Responses are requested as
// JSON via the Accept header. Only the fields we use are decoded.
package plex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
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

// Item is one Plex media row (an episode for TV sections, a movie for movie
// sections). Path is taken from the first Media.Part. Empty if Plex returned
// no parts.
type Item struct {
	RatingKey        string // stable Plex ID
	Title            string
	Type             string // "episode", "movie"
	GrandparentTitle string // show name, for episodes
	ParentIndex      int    // season number, for episodes
	Index            int    // episode number, for episodes
	Year             int    // for movies
	Resolution       string // "1080", "720", "4k", ...
	Path             string // Plex-side path, before any path-map rewrite
}

// Status is the minimal authenticated server/account metadata used by admin UI.
type Status struct {
	ServerName string
	Username   string
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
	Media            []rawMedia `json:"Media"`
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
	if err := c.getJSON("/library/sections", nil, &resp); err != nil {
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

// ItemsOptions controls Items().
type ItemsOptions struct {
	// Resolution filter, e.g. "1080". Empty means no filter.
	Resolution string
}

// Items returns the items in a section. For "show" sections it returns
// episodes (type=4). For "movie" sections it returns movies (type=1).
// Items missing a file path or a non-matching resolution are dropped.
func (c *Client) Items(section Section, opts ItemsOptions) ([]Item, error) {
	q := url.Values{}
	switch section.Type {
	case "show":
		q.Set("type", "4") // episode
	case "movie":
		q.Set("type", "1")
	default:
		return nil, fmt.Errorf("unsupported section type %q (only show/movie)", section.Type)
	}
	if opts.Resolution != "" {
		q.Set("videoResolution", opts.Resolution)
	}

	var resp itemsResponse
	if err := c.getJSON("/library/sections/"+section.Key+"/all", q, &resp); err != nil {
		return nil, err
	}

	out := make([]Item, 0, len(resp.MediaContainer.Metadata))
	for _, m := range resp.MediaContainer.Metadata {
		path := ""
		res := ""
		if len(m.Media) > 0 {
			res = strings.ToLower(m.Media[0].VideoResolution)
			if len(m.Media[0].Part) > 0 {
				path = m.Media[0].Part[0].File
			}
		}
		if path == "" {
			continue
		}
		if opts.Resolution != "" && res != strings.ToLower(opts.Resolution) {
			continue
		}
		out = append(out, Item{
			RatingKey:        m.RatingKey,
			Title:            m.Title,
			Type:             m.Type,
			GrandparentTitle: m.GrandparentTitle,
			ParentIndex:      m.ParentIndex,
			Index:            m.Index,
			Year:             m.Year,
			Resolution:       res,
			Path:             path,
		})
	}
	return out, nil
}

// Status verifies the token against the Plex server root endpoint and returns
// the small amount of metadata Plex exposes there.
func (c *Client) Status() (Status, error) {
	var resp rootResponse
	if err := c.getJSON("/", nil, &resp); err != nil {
		return Status{}, err
	}
	status := Status{
		ServerName: resp.MediaContainer.FriendlyName,
		Username:   resp.MediaContainer.MyPlexUsername,
	}
	if status.ServerName == "" {
		status.ServerName = resp.MediaContainer.ServerName
	}
	if status.Username == "" {
		status.Username = resp.MediaContainer.Username
	}
	return status, nil
}

func (c *Client) getJSON(path string, q url.Values, out any) error {
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
	req, err := http.NewRequest(http.MethodGet, u, nil)
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

// String renders a Section for human output.
func (s Section) String() string {
	return fmt.Sprintf("%s\t%s\t%s", s.Key, s.Type, s.Title)
}

// EpisodeSlug returns "S01E02" style or empty if not an episode.
func (i Item) EpisodeSlug() string {
	if i.Type != "episode" || i.ParentIndex == 0 || i.Index == 0 {
		return ""
	}
	return "S" + pad2(i.ParentIndex) + "E" + pad2(i.Index)
}

func pad2(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}
