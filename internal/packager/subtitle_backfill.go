package packager

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/tckrcr/linearcast/internal/db"
)

// SubtitleBackfillResult reports what a single-media backfill accomplished.
type SubtitleBackfillResult struct {
	EmbeddedExtracted int
	Skipped           bool // true when all preferred languages are already covered
}

// FetchSubtitlesForMedia extracts embedded text subtitle tracks for a single
// media item. Safe to call repeatedly: already-extracted tracks are skipped.
func FetchSubtitlesForMedia(ctx context.Context, conn *sql.DB, mediaID, mediaPath, outputRoot string, prefs []string) (SubtitleBackfillResult, error) {
	var result SubtitleBackfillResult

	media, err := db.MediaByID(ctx, conn, mediaID)
	if err != nil {
		return result, fmt.Errorf("lookup media: %w", err)
	}
	if media == nil {
		return result, fmt.Errorf("media not found: %s", mediaID)
	}
	if mediaPath == "" {
		mediaPath = media.Path
	}

	if len(prefs) > 0 {
		allCovered := true
		for _, lang := range prefs {
			has, err := db.HasSubtitleTrackForLang(ctx, conn, mediaID, lang)
			if err != nil {
				return result, fmt.Errorf("check lang %s: %w", lang, err)
			}
			if !has {
				allCovered = false
				break
			}
		}
		if allCovered {
			result.Skipped = true
			return result, nil
		}
	}

	if err := BackfillSubtitleTracks(ctx, conn, mediaID, mediaPath, outputRoot, prefs); err != nil {
		return result, fmt.Errorf("backfill embedded: %w", err)
	}
	result.EmbeddedExtracted = 1
	return result, nil
}
