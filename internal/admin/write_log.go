package admin

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
)

// captureResponseWriter wraps http.ResponseWriter to record the status code
// written by the handler. Defaults to 200 if WriteHeader is never called.
//
// Intentionally minimal: this admin surface only writes JSON responses and
// does not use http.Flusher, http.Hijacker, http.Pusher, or io.ReaderFrom.
// If those interfaces are ever needed, add delegation methods here.
type captureResponseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// writeLogMiddleware records non-GET requests to admin_write_log after the
// handler responds. Logging errors are printed but never surfaced to clients.
//
// GET filtering keeps the read endpoint (GET /api/admin/write-log) silent.
// There are no non-GET routes registered under /api/admin/, so self-referential
// entries cannot be created through normal use.
//
// Path values logged via r.URL.Path contain only system-assigned identifiers
// (channel/media IDs, integer timestamps, profile names) and fixed route
// segments — no secrets or arbitrary user-controlled strings. http.ServeMux
// sets r.Pattern on a cloned inner request, so the normalised pattern is not
// visible here; the raw path is the practical choice.
// shouldSkipWriteLog reports whether a non-GET request should be excluded from
// the write log. Auth events are not operator actions; trigger-only endpoints
// (package, scan, cache flush) carry no useful context without their request
// bodies, so they produce noise rather than signal.
func shouldSkipWriteLog(path string) bool {
	switch path {
	case
		"/api/auth/login", "/api/auth/logout",
		"/api/media/package", "/api/media/package/cancel",
		"/api/cache/invalid-profiles",
		"/api/admin/plex/scan", "/api/admin/jellyfin/scan":
		return true
	}
	// Encoder heartbeat traffic — high-frequency internal polling, not operator actions.
	if strings.HasPrefix(path, "/api/encoder/") {
		return true
	}
	// POST /api/admin/local-sources/{id}/scan
	return strings.HasSuffix(path, "/scan") && strings.HasPrefix(path, "/api/admin/local-sources/")
}

func (a *App) writeLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || shouldSkipWriteLog(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		start := a.now()
		crw := &captureResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(crw, r)

		// Insert after ServeHTTP returns so the client response is never
		// delayed or altered by a logging failure.
		action, targetType, targetID := classifyRequest(r.Method, r.URL.Path)
		entry := db.AdminWriteLog{
			CreatedAtMs: start.UnixMilli(),
			Method:      r.Method,
			Path:        r.URL.Path,
			Action:      nonEmptyPtr(action),
			TargetType:  nonEmptyPtr(targetType),
			TargetID:    nonEmptyPtr(targetID),
			Status:      crw.status,
			DurationMs:  a.now().Sub(start).Milliseconds(),
		}
		if err := db.InsertAdminWriteLog(context.Background(), a.dbConn, entry); err != nil {
			a.logger.Printf("write log: %v", err)
		}
	})
}

func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// classifyRequest derives a human-readable action name and optional target
// type/ID from the HTTP method and URL path. Returns empty strings for routes
// it does not recognise.
func classifyRequest(method, path string) (action, targetType, targetID string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "api" {
		return "", "", ""
	}

	switch parts[1] {
	case "admin":
		if len(parts) == 4 && parts[2] == "plex" && parts[3] == "token" {
			switch method {
			case http.MethodPost:
				return "set_plex_token", "settings", "plex"
			case http.MethodDelete:
				return "clear_plex_token", "settings", "plex"
			}
		}
		if len(parts) >= 3 && parts[2] == "local-sources" {
			return classifyLocalSourceRequest(method, parts)
		}
	case "channels":
		return classifyChannelRequest(method, parts)
	case "schedule-builder":
		if len(parts) == 3 && parts[2] == "channels" && method == http.MethodPost {
			return "create_schedule_builder_channel", "channel", ""
		}
	case "subtitle-settings":
		if len(parts) == 2 && method == http.MethodPut {
			return "update_subtitle_settings", "settings", "subtitle"
		}
	case "package-profiles":
		if len(parts) == 3 {
			switch method {
			case http.MethodPut:
				return "update_package_profile", "profile", parts[2]
			case http.MethodDelete:
				return "delete_package_profile", "profile", parts[2]
			}
		}
	}
	return "", "", ""
}

func classifyLocalSourceRequest(method string, parts []string) (action, targetType, targetID string) {
	if len(parts) == 3 && method == http.MethodPost {
		return "create_local_source", "local_source", ""
	}
	if len(parts) == 4 {
		switch method {
		case http.MethodPut:
			return "update_local_source", "local_source", parts[3]
		case http.MethodDelete:
			return "delete_local_source", "local_source", parts[3]
		}
	}
	return "", "", ""
}

func classifyChannelRequest(method string, parts []string) (action, targetType, targetID string) {
	if len(parts) < 3 {
		return "", "", ""
	}
	channelID := parts[2]

	if len(parts) == 3 {
		if method == http.MethodDelete {
			return "delete_channel", "channel", channelID
		}
		return "", "", ""
	}

	switch parts[3] {
	case "clone":
		return "clone_channel", "channel", channelID
	case "restart-playback":
		return "restart_playback", "channel", channelID
	case "extend":
		return "extend_schedule", "channel", channelID
	case "disable":
		return "disable_channel", "channel", channelID
	case "enable":
		return "enable_channel", "channel", channelID
	case "hide-from-guide":
		return "hide_channel_from_guide", "channel", channelID
	case "show-in-guide":
		return "show_channel_in_guide", "channel", channelID
	case "policy":
		return "update_channel_policy", "channel", channelID
	case "media":
		return classifyChannelMediaRequest(method, parts, channelID)
	case "schedule":
		return classifyScheduleRequest(method, parts, channelID)
	}
	return "", "", ""
}

func classifyChannelMediaRequest(method string, parts []string, channelID string) (action, targetType, targetID string) {
	switch {
	case len(parts) == 4 && method == http.MethodPost:
		return "add_channel_media", "channel", channelID
	case len(parts) == 5 && method == http.MethodDelete:
		return "remove_channel_media", "channel", channelID
	case len(parts) == 5 && parts[4] == "order" && method == http.MethodPut:
		return "reorder_channel_media", "channel", channelID
	case len(parts) == 6 && parts[5] == "move" && method == http.MethodPost:
		return "move_channel_media", "channel", channelID
	}
	return "", "", ""
}

func classifyScheduleRequest(method string, parts []string, channelID string) (action, targetType, targetID string) {
	if len(parts) == 4 && method == http.MethodDelete {
		return "clear_schedule", "channel", channelID
	}
	if len(parts) == 5 {
		switch parts[4] {
		case "range":
			return "delete_schedule_range", "channel", channelID
		case "entries":
			return "upsert_schedule_entry", "channel", channelID
		}
	}
	if len(parts) == 6 && parts[4] == "window" && parts[5] == "order" && method == http.MethodPut {
		return "update_schedule_window_order", "channel", channelID
	}
	if len(parts) == 7 && parts[4] == "entries" && method == http.MethodPost {
		switch parts[6] {
		case "after":
			return "insert_schedule_entry_after", "channel", channelID
		case "before":
			return "insert_schedule_entry_before", "channel", channelID
		}
	}
	// DELETE /api/channels/{channelID}/schedule/entries/{startMs}
	if len(parts) == 6 && parts[4] == "entries" && method == http.MethodDelete {
		return "delete_schedule_entry", "channel", channelID
	}
	return "", "", ""
}

// handleAdminWriteLog returns recent write-action log entries, newest first.
// Accepts an optional ?limit= query param (default 100, max 500).
func (a *App) handleAdminWriteLog(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		limit = n
	}

	rows, err := db.RecentAdminWriteLogs(r.Context(), a.dbConn, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	type row struct {
		ID          int64   `json:"id"`
		CreatedAtMs int64   `json:"createdAtMs"`
		Method      string  `json:"method"`
		Path        string  `json:"path"`
		Action      *string `json:"action"`
		TargetType  *string `json:"targetType"`
		TargetID    *string `json:"targetId"`
		Status      int     `json:"status"`
		DurationMs  int64   `json:"durationMs"`
	}
	out := make([]row, 0, len(rows))
	for _, e := range rows {
		r := row{
			ID:          e.ID,
			CreatedAtMs: e.CreatedAtMs,
			Method:      e.Method,
			Path:        e.Path,
			Status:      e.Status,
			DurationMs:  e.DurationMs,
		}
		r.Action = e.Action
		r.TargetType = e.TargetType
		r.TargetID = e.TargetID
		out = append(out, r)
	}
	writeJSON(w, map[string]any{"entries": out})
}
