package db

import (
	"context"
	"database/sql"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// EstimateCandidateSize estimates the finished package size for media candidate
// c encoded with profile p. It is a thin mapping of the candidate's stored
// probe fields onto packageprofile.SizeInputs; the rate-control logic lives in
// packageprofile.EstimateSize.
//
// expectedVideoBps, when > 0, supplies a measured average video bitrate for
// quality-based (CRF) profiles whose output size the profile alone cannot
// predict. Pass 0 until the empirical per-profile model exists, in which case
// CRF profiles report only a ceiling (MaxBytes) and ExpectedKnown is false.
func EstimateCandidateSize(c MediaPackageCandidate, p packageprofile.Profile, expectedVideoBps int64) packageprofile.SizeEstimate {
	return packageprofile.EstimateSize(p, packageprofile.SizeInputs{
		DurationMs:              c.DurationMs,
		SourceVideoBitrateBps:   c.VideoBitrateBps,
		ExpectedVideoBitrateBps: expectedVideoBps,
	})
}

// ProfileRealizedTotalBitrate returns the mean realized total (video+audio)
// output bitrate in bps across a profile's ready packages that have a recorded
// on-disk size, and the number of packages averaged. It reads media_packages
// only; the size is recorded at finalize, so no filesystem access is needed.
// Returns 0/0 when no ready package has a recorded size yet (e.g. before the
// size backfill has run).
func ProfileRealizedTotalBitrate(ctx context.Context, conn *sql.DB, profile string) (int64, int, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT CAST(COALESCE(AVG(package_bytes * 8000.0 / packaged_duration_ms), 0) AS INTEGER),
		       COUNT(*)
		FROM media_packages
		WHERE rendition_profile = ? AND status = ?
		  AND package_bytes IS NOT NULL AND package_bytes > 0
		  AND packaged_duration_ms IS NOT NULL AND packaged_duration_ms > 0`,
		profile, string(PackageStatusReady))
	var bps int64
	var n int
	if err := row.Scan(&bps, &n); err != nil {
		return 0, 0, err
	}
	return bps, n, nil
}
