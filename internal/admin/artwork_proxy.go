package admin

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func (a *App) handleMediaArtwork(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaID")
	m, err := db.MediaByID(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if m == nil || m.ThumbPath == "" {
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(m.SourceRef, "plex://") {
		http.NotFound(w, r)
		return
	}

	base, err := plexServer.effectiveURL(a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex url")
		return
	}
	token, err := db.GetPlexToken(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read plex token")
		return
	}
	artURL, err := plexArtworkURL(base, m.ThumbPath, token)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_artwork_error", err.Error())
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, artURL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_artwork_error", err.Error())
		return
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "plex_artwork_error", sanitizeProxyError(err).Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		http.NotFound(w, r)
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Expires", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

func plexArtworkURL(base, thumbPath, token string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	thumbPath = strings.TrimSpace(thumbPath)
	if base == "" || thumbPath == "" || token == "" {
		return "", errors.New("plex artwork is not configured")
	}
	if !strings.HasPrefix(thumbPath, "/") {
		return "", errors.New("plex thumbnail path must be absolute")
	}
	u, err := url.Parse(base + thumbPath)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("X-Plex-Token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func sanitizeProxyError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
