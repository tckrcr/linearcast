package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/schedcheck"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

type scheduleCheckResponse struct {
	GeneratedAt     string             `json:"generatedAt"`
	WindowFromMs    int64              `json:"windowFromMs"`
	WindowToMs      int64              `json:"windowToMs"`
	GapMs           int64              `json:"gapMs"`
	ChannelsChecked int                `json:"channelsChecked"`
	Issues          []schedcheck.Issue `json:"issues"`
}

// handleMaintenanceScheduleCheck runs the schedule integrity audit across all
// channels (or one, with ?channel=) and returns structured issue reports. It is
// read-only.
//
// Query params:
//
//	channel=<id>     restrict to one channel
//	hours=48         window length from the from time (default 48)
//	from=<iso8601>   window start (default: now, aligned to 6-second grid)
//	gap-ms=30000     minimum gap length to report (default 30000)
//	all=true         include disabled channels (default false)
func (a *App) handleMaintenanceScheduleCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	channelID := strings.TrimSpace(q.Get("channel"))
	includeAll := q.Get("all") == "true"

	hours := int64(48)
	if s := q.Get("hours"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "bad_param", "hours must be a positive integer")
			return
		}
		hours = v
	}

	gapMs := int64(30000)
	if s := q.Get("gap-ms"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "bad_param", "gap-ms must be a non-negative integer")
			return
		}
		gapMs = v
	}

	nowMs := a.now().UTC().UnixMilli()
	fromMs := scheduler.AlignToGrid(nowMs)
	if s := strings.TrimSpace(q.Get("from")); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_param", "from must be RFC3339")
			return
		}
		fromMs = t.UTC().UnixMilli()
	}
	toMs := fromMs + hours*3600*1000

	opts := schedcheck.Options{
		ChannelID:       channelID,
		IncludeDisabled: includeAll,
		FromMs:          fromMs,
		ToMs:            toMs,
		GapMs:           gapMs,
	}

	result, err := schedcheck.Check(r.Context(), a.dbConn, opts)
	if err != nil {
		status := http.StatusInternalServerError
		code := "check_error"
		if isNotFound(err) {
			status = http.StatusNotFound
			code = "not_found"
		}
		writeError(w, status, code, err.Error())
		return
	}

	issues := result.Issues
	if issues == nil {
		issues = []schedcheck.Issue{}
	}
	writeJSON(w, scheduleCheckResponse{
		GeneratedAt:     a.now().UTC().Format(time.RFC3339Nano),
		WindowFromMs:    fromMs,
		WindowToMs:      toMs,
		GapMs:           gapMs,
		ChannelsChecked: result.ChannelsChecked,
		Issues:          issues,
	})
}

// isNotFound reports whether err looks like a "not found" error from schedcheck
// (channel ID not found). We match on the error message since schedcheck does
// not define a sentinel type.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}
