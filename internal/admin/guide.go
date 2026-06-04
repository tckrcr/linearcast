package admin

import (
	"fmt"
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

// guideMaxHours caps the window a viewer may request. The guide is a public
// (viewer-tier) endpoint, so the horizon is bounded to keep the query cheap.
const guideMaxHours = 48

type guideEntry struct {
	EntryID    string `json:"entryId"`
	MediaID    string `json:"mediaId"`
	Title      string `json:"title,omitempty"`
	StartMs    int64  `json:"startMs"`
	EndMs      int64  `json:"endMs"`
	DurationMs int64  `json:"durationMs"`
}

type guideChannel struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"displayName"`
	ArtworkURL  string              `json:"artworkUrl,omitempty"`
	Status      string              `json:"status"`
	IsExternal  bool                `json:"isExternal,omitempty"`
	NowPlaying  *externalNowPlaying `json:"nowPlaying,omitempty"`
	// ScheduleEndMs is the end of the channel's last scheduled entry. The guide
	// uses it to stop paging past where a schedule has actually been built (so
	// "next" doesn't advance into expected, not-yet-generated gaps).
	ScheduleEndMs *int64       `json:"scheduleEndMs,omitempty"`
	Entries       []guideEntry `json:"entries"`
}

type guideResponse struct {
	NowMs    int64          `json:"nowMs"`
	FromMs   int64          `json:"fromMs"`
	ToMs     int64          `json:"toMs"`
	Channels []guideChannel `json:"channels"`
}

// handleGuide returns a viewer-safe EPG for all enabled guide channels in a
// single response: each channel's status plus its schedule entries within the
// requested window. It deliberately omits filesystem paths and scheduling-group
// detail (which the owner-only /api/channels/{id}/schedule exposes) so nothing
// sensitive leaks to the public tier. Query params mirror handleChannelSchedule:
// ?from=<unix-ms> (default now) and ?hours=N (default 24, clamped to guideMaxHours).
func (a *App) handleGuide(w http.ResponseWriter, r *http.Request) {
	nowMs := a.now().UTC().UnixMilli()
	fromMs := nowMs
	horizonHours := 24

	if f := r.URL.Query().Get("from"); f != "" {
		if _, err := fmt.Sscanf(f, "%d", &fromMs); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_from", "from must be a unix-ms integer")
			return
		}
	}
	if h := r.URL.Query().Get("hours"); h != "" {
		if _, err := fmt.Sscanf(h, "%d", &horizonHours); err != nil || horizonHours <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_hours", "hours must be a positive integer")
			return
		}
	}
	if horizonHours > guideMaxHours {
		horizonHours = guideMaxHours
	}
	toMs := fromMs + int64(horizonHours)*3600*1000

	channels, err := db.EnabledGuideChannels(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	out := make([]guideChannel, 0, len(channels))
	for _, ch := range channels {
		if ch.UpstreamHLSURL != nil {
			nowPlaying, err := a.fetchExternalNowPlaying(r.Context(), ch)
			if err != nil {
				nowPlaying = nil
			}
			out = append(out, guideChannel{
				ID:          ch.ID,
				DisplayName: ch.DisplayName,
				ArtworkURL:  artworkForExternalChannel(ch, nowPlaying),
				Status:      "live",
				IsExternal:  true,
				NowPlaying:  nowPlaying,
				Entries:     []guideEntry{},
			})
			continue
		}

		now, err := a.channelNowForRow(r.Context(), nowMs, ch, cacheStatus{})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}

		raw, err := db.ScheduleWindowEnriched(r.Context(), a.dbConn, ch.ID, fromMs, toMs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		entries := make([]guideEntry, 0, len(raw))
		for _, e := range raw {
			item := guideEntry{
				EntryID:    e.ID,
				MediaID:    e.MediaID,
				StartMs:    e.StartMs,
				EndMs:      e.StartMs + e.DurationMs,
				DurationMs: e.DurationMs,
			}
			item.Title = e.Title
			entries = append(entries, item)
		}

		out = append(out, guideChannel{
			ID:            now.ID,
			DisplayName:   now.DisplayName,
			ArtworkURL:    now.ArtworkURL,
			Status:        now.Status,
			ScheduleEndMs: now.ScheduleEndMs,
			Entries:       entries,
		})
	}

	writeJSON(w, guideResponse{
		NowMs:    nowMs,
		FromMs:   fromMs,
		ToMs:     toMs,
		Channels: out,
	})
}
