package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/lcingest"
)

type mediaInventoryResponse struct {
	Count  int64                     `json:"count"`
	Limit  int                       `json:"limit"`
	Offset int                       `json:"offset"`
	Media  []mediaInventoryListEntry `json:"media"`
}

type mediaInventoryListEntry struct {
	MediaID            string `json:"mediaId"`
	Title              string `json:"title"`
	Path               string `json:"path"`
	PathRoot           string `json:"pathRoot"`
	ReleaseGroup       string `json:"releaseGroup,omitempty"`
	EpisodeCode        string `json:"episodeCode,omitempty"`
	SeasonNumber       *int64 `json:"seasonNumber,omitempty"`
	EpisodeNumber      *int64 `json:"episodeNumber,omitempty"`
	Collection         string `json:"collection"`
	SourceRef          string `json:"sourceRef,omitempty"`
	Source             string `json:"source"`
	MediaKind          string `json:"mediaKind"`
	DurationMs         int64  `json:"durationMs"`
	Container          string `json:"container"`
	VideoCodec         string `json:"videoCodec"`
	VideoWidth         int64  `json:"videoWidth,omitempty"`
	VideoHeight        int64  `json:"videoHeight,omitempty"`
	AudioCodec         string `json:"audioCodec"`
	CodecCheckPassed   bool   `json:"codecCheckPassed"`
	CodecCheckReason   string `json:"codecCheckReason,omitempty"`
	ReadyPackages      int64  `json:"readyPackages"`
	PendingPackages    int64  `json:"pendingPackages"`
	ProcessingPackages int64  `json:"processingPackages"`
	FailedPackages     int64  `json:"failedPackages"`
	PackageProfiles    string `json:"packageProfiles,omitempty"`
}

func (a *App) handleMediaInventory(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryInt(r, "limit", 100)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be an integer")
		return
	}
	offset, ok := queryInt(r, "offset", 0)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be an integer")
		return
	}
	if limit < 1 || limit > 200 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200")
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be non-negative")
		return
	}

	rows, count, err := db.MediaInventory(r.Context(), a.dbConn, db.MediaInventoryFilter{
		Search:        r.URL.Query().Get("q"),
		Title:         r.URL.Query().Get("title"),
		Episode:       r.URL.Query().Get("episode"),
		PathRoot:      r.URL.Query().Get("pathRoot"),
		ReleaseGroup:  r.URL.Query().Get("releaseGroup"),
		Media:         r.URL.Query().Get("media"),
		Source:        r.URL.Query().Get("source"),
		MediaKind:     r.URL.Query().Get("kind"),
		Collection:    r.URL.Query().Get("collection"),
		PackageStatus: r.URL.Query().Get("packageStatus"),
		CodecStatus:   r.URL.Query().Get("codecStatus"),
		SortBy:        r.URL.Query().Get("sortBy"),
		SortDir:       r.URL.Query().Get("sortDir"),
		Limit:         limit,
		Offset:        offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	resp := mediaInventoryResponse{
		Count:  count,
		Limit:  limit,
		Offset: offset,
		Media:  make([]mediaInventoryListEntry, 0, len(rows)),
	}
	for _, row := range rows {
		kind := string(db.NormalizeMediaKind(row.MediaKind))
		collection := row.CollectionName
		if lcingest.IsMovieGroup(collection) {
			collection = ""
		}
		resp.Media = append(resp.Media, mediaInventoryListEntry{
			MediaID:            row.ID,
			Title:              row.Title,
			Path:               row.Path,
			PathRoot:           mediaPathRoot(row.Path),
			ReleaseGroup:       releaseGroupFromPath(row.Path),
			EpisodeCode:        episodeCodeFromPath(row.Path),
			SeasonNumber:       row.SeasonNumber,
			EpisodeNumber:      row.EpisodeNumber,
			Collection:         collection,
			SourceRef:          row.SourceRef,
			Source:             sourceLabel(row.SourceRef),
			MediaKind:          kind,
			DurationMs:         row.DurationMs,
			Container:          row.Container,
			VideoCodec:         row.VideoCodec,
			VideoWidth:         row.VideoWidth,
			VideoHeight:        row.VideoHeight,
			AudioCodec:         row.AudioCodec,
			CodecCheckPassed:   row.CodecCheckPassed,
			CodecCheckReason:   row.CodecCheckReason,
			ReadyPackages:      row.ReadyPackages,
			PendingPackages:    row.PendingPackages,
			ProcessingPackages: row.ProcessingPackages,
			FailedPackages:     row.FailedPackages,
			PackageProfiles:    row.PackageProfiles,
		})
	}
	writeJSON(w, resp)
}

func queryInt(r *http.Request, key string, fallback int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

func sourceLabel(sourceRef string) string {
	switch {
	case strings.HasPrefix(sourceRef, "plex://"):
		return "plex"
	case strings.HasPrefix(sourceRef, "jellyfin://"):
		return "jellyfin"
	case strings.TrimSpace(sourceRef) != "":
		return "external"
	default:
		return "local"
	}
}

func mediaPathRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	out := []string{}
	for _, part := range parts {
		if part == "" {
			continue
		}
		out = append(out, part)
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		return path
	}
	return "/" + strings.Join(out, "/")
}

func releaseGroupFromPath(path string) string {
	base := path
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}
	dash := strings.LastIndex(base, "-")
	if dash < 0 || dash == len(base)-1 {
		return ""
	}
	group := base[dash+1:]
	for _, r := range group {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return group
}

func episodeCodeFromPath(path string) string {
	base := strings.ToLower(path)
	for i := 0; i+6 <= len(base); i++ {
		if base[i] != 's' || base[i+3] != 'e' {
			continue
		}
		if isDigit(base[i+1]) && isDigit(base[i+2]) && isDigit(base[i+4]) && isDigit(base[i+5]) {
			return strings.ToUpper(base[i : i+6])
		}
	}
	return ""
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
