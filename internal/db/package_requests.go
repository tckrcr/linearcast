package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/packageid"
)

// validPackageTransition enforces the media_packages state machine. The
// processing -> pending edge (added in schema v5) represents a transient
// attempt failure: the work didn't complete but the package itself isn't
// proven broken, so it should be reclaimable by another encoder. Terminal
// failures (source missing, profile-incompatible, attempts cap exceeded)
// still go to 'failed' and require an operator retry to leave that state.
func validPackageTransition(from, to PackageStatus, inserting bool) bool {
	if inserting {
		return to == PackageStatusPending || to == PackageStatusProcessing || to == PackageStatusFailed || to == PackageStatusReady
	}
	switch from {
	case PackageStatusPending:
		return to == PackageStatusProcessing
	case PackageStatusProcessing:
		return to == PackageStatusReady || to == PackageStatusFailed || to == PackageStatusPending || to == PackageStatusProcessing
	case PackageStatusFailed:
		return to == PackageStatusProcessing
	case PackageStatusReady:
		return false
	default:
		return false
	}
}

// RequestMediaPackages records operator intent to package arbitrary media
// rows for a profile. It is transactional and classifies each unique input ID.
// Failed package rows are treated as explicit retries: they are moved back to
// pending, their error is cleared, and they are returned in Queued.
func RequestMediaPackages(ctx context.Context, conn *sql.DB, mediaIDs []string, profile string) (MediaPackageRequestResult, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultPackageProfile
	}
	result := MediaPackageRequestResult{
		Profile:        profile,
		Queued:         []string{},
		AlreadyPending: []string{},
		AlreadyReady:   []string{},
		Failed:         []MediaPackageFailure{},
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	nowMs := time.Now().UTC().UnixMilli()
	seen := make(map[string]bool, len(mediaIDs))
	for _, raw := range mediaIDs {
		mediaID := strings.TrimSpace(raw)
		if mediaID == "" || seen[mediaID] {
			continue
		}
		seen[mediaID] = true

		var codecPassed int64
		var codecReason sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT codec_check_passed, codec_check_reason
			FROM media
			WHERE id = ?`, mediaID).Scan(&codecPassed, &codecReason)
		if errors.Is(err, sql.ErrNoRows) {
			result.Failed = append(result.Failed, MediaPackageFailure{
				MediaID: mediaID,
				Code:    "not_found",
				Message: "media row not found",
			})
			continue
		}
		if err != nil {
			return result, err
		}
		if codecPassed != 1 {
			message := "media failed codec check"
			if codecReason.Valid && strings.TrimSpace(codecReason.String) != "" {
				message += ": " + codecReason.String
			}
			result.Failed = append(result.Failed, MediaPackageFailure{
				MediaID: mediaID,
				Code:    "codec_check_failed",
				Message: message,
			})
			continue
		}

		var status string
		err = tx.QueryRowContext(ctx, `
			SELECT status
			FROM media_packages
			WHERE media_id = ? AND rendition_profile = ?`, mediaID, profile).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
				VALUES (?, ?, ?, ?, ?, ?)`,
				packageid.For(mediaID, profile), mediaID, profile, string(PackageStatusPending), nowMs, nowMs); err != nil {
				return result, err
			}
			metrics.PackageStateTransitionsTotal.WithLabelValues(profile, "missing", string(PackageStatusPending)).Inc()
			result.Queued = append(result.Queued, mediaID)
			continue
		}
		if err != nil {
			return result, err
		}

		switch PackageStatus(status) {
		case PackageStatusReady:
			result.AlreadyReady = append(result.AlreadyReady, mediaID)
		case PackageStatusPending, PackageStatusProcessing:
			result.AlreadyPending = append(result.AlreadyPending, mediaID)
		case PackageStatusFailed:
			// Operator retry resets the attempts counter so the new claim
			// starts fresh against the configured max_attempts cap. Without
			// this, a row that previously hit the cap would terminal-fail
			// again on the first transient failure of its retry attempt.
			if _, err := tx.ExecContext(ctx, `
				UPDATE media_packages
				SET status = ?, attempts = 0, error = NULL, last_attempt_error = NULL, updated_at_ms = ?
				WHERE media_id = ? AND rendition_profile = ? AND status = ?`,
				string(PackageStatusPending), nowMs, mediaID, profile, string(PackageStatusFailed)); err != nil {
				return result, err
			}
			metrics.PackageStateTransitionsTotal.WithLabelValues(profile, string(PackageStatusFailed), string(PackageStatusPending)).Inc()
			result.Queued = append(result.Queued, mediaID)
		default:
			result.Failed = append(result.Failed, MediaPackageFailure{
				MediaID: mediaID,
				Code:    "invalid_package_status",
				Message: "package row has unsupported status: " + status,
			})
		}
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

// CancelMediaPackages marks pending and processing package rows as failed with
// an operator-cancel reason. Pending rows stop being claimable immediately.
// Processing rows rely on the packager worker's status monitor to interrupt
// the active ffmpeg process and preserve this failed state.
func CancelMediaPackages(ctx context.Context, conn *sql.DB, mediaIDs []string, profile string, nowMs int64, reason string) (MediaPackageCancelResult, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultPackageProfile
	}
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
	}
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled by operator"
	}
	result := MediaPackageCancelResult{
		Profile:          profile,
		AffectedMediaIDs: []string{},
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	seen := make(map[string]bool, len(mediaIDs))
	for _, raw := range mediaIDs {
		mediaID := strings.TrimSpace(raw)
		if mediaID == "" || seen[mediaID] {
			continue
		}
		seen[mediaID] = true

		var status string
		err := tx.QueryRowContext(ctx, `
			SELECT status
			FROM media_packages
			WHERE media_id = ? AND rendition_profile = ?`, mediaID, profile).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			result.SkippedMissing++
			continue
		}
		if err != nil {
			return result, err
		}

		switch PackageStatus(status) {
		case PackageStatusPending, PackageStatusProcessing:
			res, err := tx.ExecContext(ctx, `
				UPDATE media_packages
				SET status = ?, error = ?, updated_at_ms = ?
				WHERE media_id = ? AND rendition_profile = ? AND status = ?`,
				string(PackageStatusFailed), reason, nowMs, mediaID, profile, status)
			if err != nil {
				return result, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return result, err
			}
			if n == 0 {
				continue
			}
			result.AffectedMediaIDs = append(result.AffectedMediaIDs, mediaID)
			if PackageStatus(status) == PackageStatusProcessing {
				result.CanceledProcessing += n
			} else {
				result.CanceledPending += n
			}
			metrics.PackageStateTransitionsTotal.WithLabelValues(profile, status, string(PackageStatusFailed)).Add(float64(n))
		case PackageStatusReady:
			result.SkippedReady++
		case PackageStatusFailed:
			result.SkippedFailed++
		default:
			result.SkippedUnsupported++
		}
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

// CancelAllMediaPackagesForProfile cancels every pending/processing package row
// for a profile. Passing "all" as the profile spans every profile.
func CancelAllMediaPackagesForProfile(ctx context.Context, conn *sql.DB, profile string, nowMs int64, reason string) (MediaPackageCancelResult, error) {
	profile = strings.TrimSpace(profile)
	allProfiles := strings.EqualFold(profile, "all")
	if profile == "" {
		profile = DefaultPackageProfile
	}
	if nowMs == 0 {
		nowMs = time.Now().UTC().UnixMilli()
	}
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled by operator"
	}
	result := MediaPackageCancelResult{
		Profile:          profile,
		AffectedMediaIDs: []string{},
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	query := `
		SELECT media_id, rendition_profile, status
		FROM media_packages
		WHERE status IN (?, ?)`
	args := []any{string(PackageStatusPending), string(PackageStatusProcessing)}
	if !allProfiles {
		query += ` AND rendition_profile = ?`
		args = append(args, profile)
	}
	targets, err := queryRows(ctx, tx, scanPackageCancelTarget, query, args...)
	if err != nil {
		return result, err
	}

	for _, target := range targets {
		res, err := tx.ExecContext(ctx, `
			UPDATE media_packages
			SET status = ?, error = ?, updated_at_ms = ?
			WHERE media_id = ? AND rendition_profile = ? AND status = ?`,
			string(PackageStatusFailed), reason, nowMs, target.mediaID, target.profile, target.status)
		if err != nil {
			return result, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return result, err
		}
		if n == 0 {
			continue
		}
		result.AffectedMediaIDs = append(result.AffectedMediaIDs, target.mediaID)
		if PackageStatus(target.status) == PackageStatusProcessing {
			result.CanceledProcessing += n
		} else {
			result.CanceledPending += n
		}
		metrics.PackageStateTransitionsTotal.WithLabelValues(target.profile, target.status, string(PackageStatusFailed)).Add(float64(n))
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

type packageCancelTarget struct {
	mediaID string
	profile string
	status  string
}

func scanPackageCancelTarget(row scanner) (packageCancelTarget, error) {
	var target packageCancelTarget
	err := row.Scan(&target.mediaID, &target.profile, &target.status)
	return target, err
}

// MediaPackageCandidates returns codec-passing media without a ready package
// for the supplied profile. Pending/processing rows are included so operators

// ClaimRequest is the input to ClaimPackage. EncoderID is "" for local
// claims and a registered encoder ID for remote claims. LeaseTTL is required
// when EncoderID is non-empty; it sets the initial lease_expires_ms on the
// encoder_jobs row that ClaimPackage will insert in the same transaction.
//
// The concurrency cap is no longer a caller input: ClaimPackage reads the
// limit from the encoder row (remote) or the local_worker_concurrency setting
// (local) and applies it inside the same transaction.
type ClaimRequest struct {
	MediaID   string
	Profile   string
	PackageID string
	EncoderID string
	LeaseTTL  time.Duration
	NowMs     int64
}

// ClaimPackage atomically claims a media_packages row for work. It is the
// single entry point for both local workers (EncoderID="") and remote
// encoders (EncoderID set). Steps, in one transaction:
//
//  1. Validate the encoder is registered and not revoked (remote only).
//  2. Resolve channel encoder_policy across every channel referencing the
//     media; reject the claim if any channel forbids this claim type.
//  3. Move the package row pending/failed→processing (or insert a fresh row
//     at processing if missing). Increment attempts, clear error and
//     last_attempt_error.
//  4. Insert an encoder_jobs lease row (remote only).
//
// Returns true when the caller wins the claim. Returns false (with nil err)
// when the row is already in a state that can't be claimed — e.g. ready,
// already-processing, or rejected by channel encoder policy — so workers can
// move on. Errors are reserved for malformed requests, missing/revoked
// encoders, and DB faults.
func ClaimPackage(ctx context.Context, conn *sql.DB, req ClaimRequest) (bool, error) {
	if req.MediaID == "" || req.Profile == "" || req.PackageID == "" {
		return false, errors.New("media id, profile, and package id are required")
	}
	isRemote := req.EncoderID != ""
	if isRemote && req.LeaseTTL <= 0 {
		return false, errors.New("LeaseTTL is required for remote claims")
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if isRemote {
		if err := requireActiveEncoder(ctx, tx, req.EncoderID); err != nil {
			return false, err
		}
	}
	// Policy rejection is "couldn't claim, try another row," not "input was
	// wrong." Returning (false, nil) keeps discovery loops simple: they don't
	// have to special-case policy errors versus already-ready/already-processing
	// rows. The reason is dropped here; callers who care can fetch the channel
	// policy directly.
	if allow, _, err := policyAllowsClaim(ctx, tx, req.MediaID, isRemote); err != nil {
		return false, err
	} else if !allow {
		return false, tx.Commit()
	}

	// Per-encoder (remote) or per-machine (local) concurrency cap.
	maxCon, err := concurrencyCapForClaim(ctx, tx, isRemote, req.EncoderID)
	if err != nil {
		return false, err
	}
	if maxCon > 0 {
		var active int
		var countErr error
		if isRemote {
			countErr = tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM encoder_jobs WHERE encoder_id = ?`,
				req.EncoderID).Scan(&active)
		} else {
			countErr = tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM media_packages mp
				WHERE mp.status = ?
				  AND NOT EXISTS (SELECT 1 FROM encoder_jobs ej WHERE ej.package_id = mp.id)`,
				string(PackageStatusProcessing)).Scan(&active)
		}
		if countErr != nil {
			return false, countErr
		}
		if active >= maxCon {
			return false, tx.Commit()
		}
	} else if !isRemote {
		// local_worker_concurrency=0 means "local worker disabled."
		return false, tx.Commit()
	}

	var status string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM media_packages WHERE media_id = ? AND rendition_profile = ?`,
		req.MediaID, req.Profile).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Fresh row: attempts=1, status=processing.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO media_packages
			  (id, media_id, rendition_profile, status, attempts, created_at_ms, updated_at_ms)
			VALUES (?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT(media_id, rendition_profile) DO NOTHING`,
			req.PackageID, req.MediaID, req.Profile, string(PackageStatusProcessing),
			req.NowMs, req.NowMs)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, tx.Commit()
		}
		metrics.PackageStateTransitionsTotal.WithLabelValues(req.Profile, "missing", string(PackageStatusProcessing)).Inc()
	case err != nil:
		return false, err
	default:
		if !validPackageTransition(PackageStatus(status), PackageStatusProcessing, false) {
			return false, tx.Commit()
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE media_packages
			   SET status = ?, attempts = attempts + 1,
			       error = NULL, last_attempt_error = NULL, updated_at_ms = ?
			 WHERE media_id = ? AND rendition_profile = ? AND status IN (?, ?)`,
			string(PackageStatusProcessing), req.NowMs,
			req.MediaID, req.Profile,
			string(PackageStatusPending), string(PackageStatusFailed))
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, tx.Commit()
		}
		metrics.PackageStateTransitionsTotal.WithLabelValues(req.Profile, metrics.PackageStatusLabel(status), string(PackageStatusProcessing)).Inc()
	}

	if isRemote {
		leaseExpires := req.NowMs + req.LeaseTTL.Milliseconds()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO encoder_jobs (package_id, encoder_id, claimed_at_ms, lease_expires_ms, last_heartbeat_ms)
			VALUES (?, ?, ?, ?, ?)`,
			req.PackageID, req.EncoderID, req.NowMs, leaseExpires, req.NowMs); err != nil {
			return false, fmt.Errorf("insert lease: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE encoders SET last_seen_ms = ? WHERE id = ?`, req.NowMs, req.EncoderID); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// MarkPackageProcessing records the package filesystem identity and metadata
// before ffmpeg starts writing output. PackageOne calls this after a worker
// claim, or directly for one-shot CLI packaging.
func MarkPackageProcessing(ctx context.Context, conn *sql.DB, p MediaPackage) error {
	from := metrics.PackageStatusLabel(string(p.Status))
	p.Status = PackageStatusProcessing
	if p.CreatedAtMs == 0 {
		p.CreatedAtMs = p.UpdatedAtMs
	}
	if err := UpsertMediaPackage(ctx, conn, p); err != nil {
		return err
	}
	if from != string(PackageStatusProcessing) {
		metrics.PackageStateTransitionsTotal.WithLabelValues(p.RenditionProfile, from, string(PackageStatusProcessing)).Inc()
	}
	return nil
}

// ApplyFinalizedPackageTransition writes the canonical ready-column list for a
// media_packages row. It clears error / last_attempt_error, sets all finalized
// metadata fields, and verifies the row was still processing. The number of
// rows affected is returned so callers can decide whether zero is an error.
func ApplyFinalizedPackageTransition(ctx context.Context, tx *sql.Tx, packageID string, finalized FinalizedPackage, nowMs int64) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE media_packages
		   SET status = ?, error = NULL, last_attempt_error = NULL,
		       package_root = ?, init_segment_path = ?, segment_base_path = ?,
		       container = ?, video_codec = ?, video_profile = ?,
		       video_width = ?, video_height = ?, audio_codec = ?, audio_profile = ?,
		       timescale = ?, packaged_duration_ms = ?, updated_at_ms = ?
		 WHERE id = ? AND status = ?`,
		string(PackageStatusReady),
		finalized.PackageRoot, finalized.InitSegmentPath, finalized.SegmentBasePath,
		finalized.Container, finalized.VideoCodec, finalized.VideoProfile,
		finalized.VideoWidth, finalized.VideoHeight, finalized.AudioCodec, finalized.AudioProfile,
		finalized.Timescale, finalized.PackagedDurationMs, nowMs,
		packageID, string(PackageStatusProcessing))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func MarkPackageReady(ctx context.Context, conn *sql.DB, p MediaPackage) error {
	p.Status = PackageStatusReady
	p.Error = nil
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	finalized := FinalizedPackage{
		PackageRoot:        p.PackageRoot,
		InitSegmentPath:    p.InitSegmentPath,
		SegmentBasePath:    nonEmptyStr(p.SegmentBasePath),
		Container:          nonEmptyStr(p.Container),
		VideoCodec:         nonEmptyStr(p.VideoCodec),
		VideoProfile:       nonEmptyStr(p.VideoProfile),
		VideoWidth:         p.VideoWidth,
		VideoHeight:        p.VideoHeight,
		AudioCodec:         nonEmptyStr(p.AudioCodec),
		AudioProfile:       nonEmptyStr(p.AudioProfile),
		Timescale:          p.Timescale,
		PackagedDurationMs: p.PackagedDurationMs,
	}
	n, err := ApplyFinalizedPackageTransition(ctx, tx, p.ID, finalized, p.UpdatedAtMs)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("package is no longer processing")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	metrics.PackageStateTransitionsTotal.WithLabelValues(p.RenditionProfile, string(PackageStatusProcessing), string(PackageStatusReady)).Inc()
	return nil
}

func MarkPackageFailed(ctx context.Context, conn *sql.DB, p MediaPackage, cause error, nowMs int64) error {
	from := metrics.PackageStatusLabel(string(p.Status))
	p.Status = PackageStatusFailed
	if cause != nil {
		errStr := cause.Error()
		p.Error = &errStr
	}
	p.UpdatedAtMs = nowMs
	if err := UpsertMediaPackage(ctx, conn, p); err != nil {
		return err
	}
	if from != string(PackageStatusFailed) {
		metrics.PackageStateTransitionsTotal.WithLabelValues(p.RenditionProfile, from, string(PackageStatusFailed)).Inc()
	}
	return nil
}

func MarkPackageFailedByMediaProfile(ctx context.Context, conn *sql.DB, mediaID, profile string, cause error, nowMs int64) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	res, err := conn.ExecContext(ctx, `
		UPDATE media_packages
		SET status = ?, error = ?, updated_at_ms = ?
		WHERE media_id = ? AND rendition_profile = ? AND status = ?`,
		string(PackageStatusFailed), message, nowMs, mediaID, profile, string(PackageStatusProcessing))
	if err == nil {
		if n, rowsErr := res.RowsAffected(); rowsErr == nil && n > 0 {
			metrics.PackageStateTransitionsTotal.WithLabelValues(profile, string(PackageStatusProcessing), string(PackageStatusFailed)).Add(float64(n))
		}
	}
	return err
}

// MarkPackageFailedWithKind applies the same transient/terminal failure
// semantics used by remote encoders to a local worker package. It does not
// touch encoder_jobs or encoders — those are remote-only concerns.
func MarkPackageFailedWithKind(ctx context.Context, conn *sql.DB, packageID, kind, reason string, maxAttempts int, nowMs int64) (PackageStatus, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "transient" && kind != "terminal" {
		return "", fmt.Errorf("kind must be 'transient' or 'terminal', got %q", kind)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "worker reported failure"
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	newStatus, _, err := resolveAndApplyFailure(ctx, tx, packageID, kind, reason, maxAttempts, nowMs)
	if err != nil {
		return "", err
	}
	return newStatus, tx.Commit()
}

// ChannelProfileReadiness returns packaging coverage counts for all
// codec-check-passing media in a channel's playlist at the given profile.
func ChannelProfileReadiness(ctx context.Context, conn Execer, channelID, profile string) (ProfileReadiness, error) {
	r := ProfileReadiness{Profile: profile}
	err := conn.QueryRowContext(ctx, `
		SELECT
		  COUNT(*),
		  COALESCE(SUM(CASE WHEN p.status = 'ready'      THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN p.status = 'pending'    THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN p.status = 'processing' THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN p.status = 'failed'     THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN p.id IS NULL            THEN 1 ELSE 0 END), 0)
		FROM channel_media cm
		JOIN media m ON m.id = cm.media_id
		LEFT JOIN media_packages p
		       ON p.media_id = cm.media_id
		      AND p.rendition_profile = ?
		WHERE cm.channel_id = ?
		  AND m.codec_check_passed = 1`,
		profile, channelID,
	).Scan(&r.Total, &r.Ready, &r.Pending, &r.Processing, &r.Failed, &r.Missing)
	return r, err
}

// ScheduleUnreadyCount returns the number of schedule entries in [nowMs,
// nowMs+horizonMs) whose media does not have a ready package at profile.
// Used to gate profile cutover: a non-zero result means playback will break.
func ScheduleUnreadyCount(ctx context.Context, conn *sql.DB, channelID, profile string, nowMs, horizonMs int64) (int64, error) {
	var n int64
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schedule_entries se
		LEFT JOIN media_packages p
		       ON p.media_id = se.media_id
		      AND p.rendition_profile = ?
		WHERE se.channel_id = ?
		  AND se.start_ms >= ?
		  AND se.start_ms < ?
		  AND (p.status IS NULL OR p.status != 'ready')`,
		profile, channelID, nowMs, nowMs+horizonMs,
	).Scan(&n)
	return n, err
}

// QueueChannelProfileMigration queues package work for every codec-passing
// media item in channelID's playlist at the target profile. It delegates to
// RequestMediaPackages so the same ready/pending/failed classification logic
// applies.
func QueueChannelProfileMigration(ctx context.Context, conn *sql.DB, channelID, profile string) (MediaPackageRequestResult, error) {
	order, err := ChannelMediaOrdered(ctx, conn, channelID)
	if err != nil {
		return MediaPackageRequestResult{}, err
	}
	eligible := map[string]bool{}
	eligibleIDs, err := queryRows(ctx, conn, scanString, `
		SELECT cm.media_id
		FROM channel_media cm
		JOIN media m ON m.id = cm.media_id
		WHERE cm.channel_id = ?
		  AND m.codec_check_passed = 1`, channelID)
	if err != nil {
		return MediaPackageRequestResult{}, err
	}
	for _, id := range eligibleIDs {
		eligible[id] = true
	}
	var ids []string
	for _, id := range order {
		if eligible[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return MediaPackageRequestResult{Profile: profile, Queued: []string{}, AlreadyPending: []string{}, AlreadyReady: []string{}, Failed: []MediaPackageFailure{}}, nil
	}
	return RequestMediaPackages(ctx, conn, ids, profile)
}

func FailStaleProcessingPackages(ctx context.Context, conn *sql.DB, cutoffMs, nowMs int64, maxAttempts int, reason string) (int64, error) {
	ids, err := queryRows(ctx, conn, scanString, `
		SELECT id FROM media_packages
		WHERE status = ? AND updated_at_ms < ?`,
		string(PackageStatusProcessing), cutoffMs)
	if err != nil {
		return 0, err
	}

	var count int64
	for _, id := range ids {
		_, err := MarkPackageFailedWithKind(ctx, conn, id, "transient", reason, maxAttempts, nowMs)
		if err == nil {
			count++
		}
	}
	if count > 0 {
		metrics.PackageRepairRequeuesTotal.WithLabelValues("stale_processing").Add(float64(count))
	}
	return count, nil
}

func MarkReadyPackagePendingForReencode(ctx context.Context, conn *sql.DB, packageID string, nowMs int64, reason string) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE media_packages
		SET status = ?, error = ?, packaged_duration_ms = NULL, updated_at_ms = ?
		WHERE id = ? AND status = ?`,
		string(PackageStatusPending), reason, nowMs, packageID, string(PackageStatusReady))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, tx.Commit()
	}
	var profile string
	_ = tx.QueryRowContext(ctx, `SELECT rendition_profile FROM media_packages WHERE id = ?`, packageID).Scan(&profile)
	if _, err := tx.ExecContext(ctx, `DELETE FROM packaged_segments WHERE package_id = ?`, packageID); err != nil {
		return false, err
	}
	metrics.PackageStateTransitionsTotal.WithLabelValues(profile, string(PackageStatusReady), string(PackageStatusPending)).Inc()
	return true, tx.Commit()
}
