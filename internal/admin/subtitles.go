package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
)

// --- Subtitle track listing ---

type subtitleTrackResponse struct {
	Language string `json:"language"`
	Source   string `json:"source"`
	Codec    string `json:"codec"`
	HasFile  bool   `json:"hasFile"`
}

func (a *App) handleMediaSubtitlesList(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaID")
	if mediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media_id", "mediaID is required")
		return
	}

	tracks, err := db.MediaTracksByMediaID(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	var out []subtitleTrackResponse
	for _, t := range tracks {
		if t.Kind != "subtitle" {
			continue
		}
		out = append(out, subtitleTrackResponse{
			Language: t.Language,
			Source:   string(t.Source),
			Codec:    t.Codec,
			HasFile:  t.Path != nil && *t.Path != "",
		})
	}
	writeJSON(w, out)
}

func (a *App) handleMediaSubtitlesDelete(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaID")
	lang := r.PathValue("language")
	if mediaID == "" || lang == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "mediaID and language are required")
		return
	}

	if err := db.DeleteMediaTrack(r.Context(), a.dbConn, mediaID, lang); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

// --- Single-media subtitle extraction ---

func (a *App) handleMediaSubtitlesExtract(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaID")
	if mediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media_id", "mediaID is required")
		return
	}

	defer r.Body.Close()
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&struct{}{})

	media, err := db.MediaByID(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if media == nil {
		writeError(w, http.StatusNotFound, "not_found", "media not found")
		return
	}

	prefs, err := db.GetSubtitleLanguagePreference(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if len(prefs) == 0 {
		writeError(w, http.StatusBadRequest, "no_preferences", "no subtitle language preferences configured")
		return
	}

	packageRoot := a.packageRoot
	if packageRoot == "" {
		if cacheDir := os.Getenv("CACHE_DIR"); cacheDir != "" {
			packageRoot = cacheDir + "/packages"
		}
	}
	if packageRoot == "" {
		writeError(w, http.StatusInternalServerError, "no_package_root", "package root is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	result, err := packager.FetchSubtitlesForMedia(ctx, a.dbConn, mediaID, media.Path, packageRoot, prefs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "extract_failed", err.Error())
		return
	}

	writeJSON(w, map[string]any{
		"embeddedExtracted": result.EmbeddedExtracted > 0,
		"skipped":           result.Skipped,
	})
}
