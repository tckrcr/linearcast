package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"

	"github.com/tckrcr/linearcast/internal/db"
)

type externalNowPlaying struct {
	Title   string `json:"title,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Album   string `json:"album,omitempty"`
	ArtURL  string `json:"artUrl,omitempty"`
	Playing bool   `json:"playing"`
}

func (a *App) fetchExternalNowPlaying(ctx context.Context, ch db.Channel) (*externalNowPlaying, error) {
	if ch.UpstreamHLSURL == nil {
		return nil, nil
	}
	u, err := url.Parse(*ch.UpstreamHLSURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, nil
	}
	u.Path = "/now-playing"
	u.RawQuery = ""
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var now externalNowPlaying
	if err := json.NewDecoder(resp.Body).Decode(&now); err != nil {
		return nil, err
	}
	return &now, nil
}

func artworkForExternalChannel(ch db.Channel, now *externalNowPlaying) string {
	if ch.ArtworkURL != "" {
		return ch.ArtworkURL
	}
	if now != nil {
		return now.ArtURL
	}
	return ""
}

func externalNowPlayingURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = path.Clean("/now-playing")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
