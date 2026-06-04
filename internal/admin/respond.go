package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

var errNotFound = errors.New("not found")

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string, hint ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]string{
		"error":   code,
		"message": message,
	}
	if len(hint) > 0 && strings.TrimSpace(hint[0]) != "" {
		body["hint"] = strings.TrimSpace(hint[0])
	}
	_ = json.NewEncoder(w).Encode(body)
}

func parseQueryUnixMs(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		writeError(w, http.StatusBadRequest, "missing_"+name, name+" is required")
		return 0, false
	}
	var v int64
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_"+name, name+" must be a unix-ms integer")
		return 0, false
	}
	return v, true
}

func packageSummaryKey(channelID, profile, status string) string {
	return channelID + "\x00" + profile + "\x00" + status
}

func requiredPackageProfile(ch db.Channel) string {
	if ch.RequiredPackageProfile != "" {
		return ch.RequiredPackageProfile
	}
	return db.DefaultPackageProfile
}
