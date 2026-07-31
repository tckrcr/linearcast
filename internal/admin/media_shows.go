package admin

import (
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

type mediaShowSeasonSummary struct {
	SeasonNumber *int64 `json:"seasonNumber"`
	EpisodeCount int64  `json:"episodeCount"`
	DurationMs   int64  `json:"durationMs"`
}

type mediaShowSummary struct {
	CollectionID string                   `json:"collectionId"`
	Name         string                   `json:"name"`
	EpisodeCount int64                    `json:"episodeCount"`
	DurationMs   int64                    `json:"durationMs"`
	SeasonCount  int                      `json:"seasonCount"`
	Seasons      []mediaShowSeasonSummary `json:"seasons"`
}

func (a *App) handleMediaShows(w http.ResponseWriter, r *http.Request) {
	rows, err := db.ShowCollectionSeasonRollup(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	shows := make([]mediaShowSummary, 0)
	showIndexes := make(map[string]int)
	for _, row := range rows {
		index, found := showIndexes[row.CollectionID]
		if !found {
			index = len(shows)
			showIndexes[row.CollectionID] = index
			shows = append(shows, mediaShowSummary{
				CollectionID: row.CollectionID,
				Name:         row.CollectionName,
				Seasons:      make([]mediaShowSeasonSummary, 0),
			})
		}
		show := &shows[index]
		show.EpisodeCount += row.EpisodeCount
		show.DurationMs += row.DurationMs
		show.Seasons = append(show.Seasons, mediaShowSeasonSummary{
			SeasonNumber: row.SeasonNumber,
			EpisodeCount: row.EpisodeCount,
			DurationMs:   row.DurationMs,
		})
		show.SeasonCount = len(show.Seasons)
	}

	writeJSON(w, map[string]any{"shows": shows})
}
