package admin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

type subtitleSettingsResponse struct {
	AutoEnable         bool     `json:"subtitleAutoEnable"`
	LanguagePreference []string `json:"subtitleLanguagePreference"`
}

type subtitleSettingsUpdateRequest struct {
	AutoEnable         bool     `json:"subtitleAutoEnable"`
	LanguagePreference []string `json:"subtitleLanguagePreference"`
}

var subtitleLangCodeRE = regexp.MustCompile(`^[a-z]{3}$`)

func (a *App) handleSubtitleSettings(w http.ResponseWriter, r *http.Request) {
	autoEnable, err := db.GetSubtitleAutoEnable(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read settings")
		return
	}
	langs, err := db.GetSubtitleLanguagePreference(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read settings")
		return
	}
	writeJSON(w, subtitleSettingsResponse{
		AutoEnable:         autoEnable,
		LanguagePreference: langs,
	})
}

func (a *App) handleSubtitleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req subtitleSettingsUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	langs, err := normalizeSubtitleLanguagePreference(req.LanguagePreference)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_language_preference", err.Error())
		return
	}
	if err := db.SetSubtitleLanguagePreference(r.Context(), a.dbConn, langs); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to write settings")
		return
	}
	if err := db.SetSubtitleAutoEnable(r.Context(), a.dbConn, req.AutoEnable); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to write settings")
		return
	}
	writeJSON(w, subtitleSettingsResponse{
		AutoEnable:         req.AutoEnable,
		LanguagePreference: langs,
	})
}

func (a *App) handleSchedulerTunables(w http.ResponseWriter, r *http.Request) {
	t, err := db.GetSchedulerTunables(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read scheduler tunables")
		return
	}
	writeJSON(w, t)
}

func (a *App) handleSchedulerTunablesUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req db.SchedulerTunables
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if err := db.SetSchedulerTunables(r.Context(), a.dbConn, req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tunables", err.Error())
		return
	}
	writeJSON(w, req)
}

func (a *App) handleEncoderSweeperSettings(w http.ResponseWriter, r *http.Request) {
	s, err := db.GetEncoderSweeperSettings(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read encoder sweeper settings")
		return
	}
	writeJSON(w, s)
}

func (a *App) handleEncoderSweeperSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req db.EncoderSweeperSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if err := db.SetEncoderSweeperSettings(r.Context(), a.dbConn, req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	writeJSON(w, req)
}

func normalizeSubtitleLanguagePreference(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, errSubtitleLangsRequired{}
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, lang := range raw {
		lang = strings.ToLower(strings.TrimSpace(lang))
		if !subtitleLangCodeRE.MatchString(lang) {
			return nil, errInvalidSubtitleLang{lang: lang}
		}
		if seen[lang] {
			continue
		}
		seen[lang] = true
		out = append(out, lang)
	}
	if len(out) == 0 {
		return nil, errSubtitleLangsRequired{}
	}
	return out, nil
}

type errSubtitleLangsRequired struct{}

func (errSubtitleLangsRequired) Error() string {
	return "at least one 3-letter ISO 639-2 language code is required"
}

type errInvalidSubtitleLang struct {
	lang string
}

func (e errInvalidSubtitleLang) Error() string {
	if e.lang == "" {
		return "language codes cannot be empty"
	}
	return "language code " + e.lang + " must be a 3-letter ISO 639-2 code"
}
