package admin

import (
	"context"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// expectedVideoBpsForProfile returns the empirical expected video bitrate for a
// quality-based (CRF / capped-CRF) profile, measured from finalized package
// sizes recorded in SQLite, together with the number of packages behind it.
// Both are 0 for non-quality modes and when no finished package is measurable,
// in which case capped-CRF estimates fall back to a ceiling.
func (a *App) expectedVideoBpsForProfile(ctx context.Context, p packageprofile.Profile) (int64, int) {
	if rc := p.Video.RateControl(); rc != packageprofile.RateControlCRF && rc != packageprofile.RateControlCappedCRF {
		return 0, 0
	}
	totalBps, n, err := db.ProfileRealizedTotalBitrate(ctx, a.dbConn, p.Name)
	if err != nil || totalBps <= 0 || n == 0 {
		return 0, 0
	}
	audioBps := int64(0)
	if p.Audio.Mode == packageprofile.AudioModeTranscode {
		audioBps = packageprofile.ParseBitrate(p.Audio.Bitrate)
	}
	videoBps := totalBps - audioBps
	if videoBps <= 0 {
		return 0, 0
	}
	return videoBps, n
}
