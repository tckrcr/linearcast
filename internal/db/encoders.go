package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/metrics"
)

// Sentinel errors for encoder job-op preconditions. They are returned wrapped
// via %w so the human-readable message is preserved verbatim (these strings are
// surfaced to encoder HTTP clients) while callers match with errors.Is instead
// of switching on error text. The admin layer maps them to HTTP status codes;
// see classifyJobOpError.
var (
	ErrEncoderNotRegistered = errors.New("not registered")
	ErrEncoderRevoked       = errors.New("revoked")
	ErrNoActiveLease        = errors.New("no active lease")
	ErrPackageLeasedByOther = errors.New("leased by encoder")
	ErrPackageNotProcessing = errors.New("not processing")
	ErrPackageNotFound      = errors.New("not found")
)

// EncoderStatus values track encoder reachability, not authorization.
// Revocation is signalled separately via Encoder.RevokedAtMs so that an
// encoder which has been forcibly disabled cannot be re-enabled merely by
// reporting in. Draining is reserved for a future "finish in-flight jobs but
// take no new ones" mode; not used in v1.
type EncoderStatus string

const (
	EncoderStatusPending  EncoderStatus = "pending"
	EncoderStatusOnline   EncoderStatus = "online"
	EncoderStatusDraining EncoderStatus = "draining"
	EncoderStatusOffline  EncoderStatus = "offline"
)

// EncoderPolicy is the per-channel encoder-selection rule. NULL on a channel
// row is equivalent to EncoderPolicyAny.
type EncoderPolicy string

const (
	EncoderPolicyAny             EncoderPolicy = "any"
	EncoderPolicyRemoteOnly      EncoderPolicy = "remote_only"
	EncoderPolicyRemotePreferred EncoderPolicy = "remote_preferred"
	EncoderPolicyLocalOnly       EncoderPolicy = "local_only"
)

// apiKeyPrefix tags raw keys so they are recognizable in logs, configs, and
// HTTP authorization headers. The hash stored in the DB is over the full
// prefixed key, so trimming the prefix anywhere produces an unverifiable token.
const apiKeyPrefix = "lcenc_"

// Encoder is the record of a registered remote encoder. Rows are ephemeral:
// RevokeEncoder flips Status to offline so the key stops working but the row
// stays, and DeleteEncoder removes the row outright (releasing any active
// leases back to pending).
type Encoder struct {
	ID           string
	Name         string
	APIKeyHash   string
	Capabilities string
	LastSeenMs   int64
	Status       EncoderStatus
	CreatedAtMs  int64
	RevokedAtMs  *int64
	Concurrency  int
}

// IsRevoked reports whether this encoder has been administratively disabled.
// Job-op helpers (Heartbeat/Complete/Fail/Claim) all reject revoked encoders
// in v1 — there are no draining semantics yet.
func (e *Encoder) IsRevoked() bool {
	return e.RevokedAtMs != nil
}

// EncoderJob is the per-claim lease row. 1:1 with a media_packages row while
// that row is in 'processing'. Heartbeats touch this table (not media_packages)
// so the playback-read path on media_packages stays free of write contention.
type EncoderJob struct {
	PackageID       string
	EncoderID       string
	ClaimedAtMs     int64
	LeaseExpiresMs  int64
	LastHeartbeatMs int64
	ProgressPct     *int64
}

// EncoderJobSummary is the read-side view of an active encoder job, enriched
// with media metadata for display in the admin UI.
type EncoderJobSummary struct {
	EncoderID      string
	PackageID      string
	MediaID        string
	MediaTitle     string
	Profile        string
	ProgressPct    *int64
	LeaseExpiresMs int64
	ClaimedAtMs    int64
}

// RegisterEncoder creates a new encoder row and returns the raw API key.
// The raw key is shown to the operator exactly once: only the SHA-256 hash is
// persisted, so a lost key requires re-registering. capabilities is opaque
// JSON; the DB does not parse it.
func RegisterEncoder(ctx context.Context, conn *sql.DB, name, capabilities string, nowMs int64) (encoderID, rawAPIKey string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", errors.New("encoder name is required")
	}
	capabilities = strings.TrimSpace(capabilities)
	if capabilities == "" {
		capabilities = "{}"
	}

	id, err := newEncoderID()
	if err != nil {
		return "", "", err
	}
	raw, hash, err := newAPIKey()
	if err != nil {
		return "", "", err
	}

	// New encoders start as pending with last_seen_ms=0. The first successful
	// /api/encoder/ping (via UpdateEncoderCapabilities) flips status to online
	// and records host metadata. This keeps a freshly registered row from
	// looking live before the encoder has actually connected.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO encoders (id, name, api_key_hash, capabilities, last_seen_ms, status, created_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, hash, capabilities, 0, string(EncoderStatusPending), nowMs); err != nil {
		return "", "", fmt.Errorf("insert encoder: %w", err)
	}
	return id, raw, nil
}

// RevokeEncoder marks an encoder revoked. The row stays for audit; subsequent
// job operations (heartbeat/complete/fail/claim) will reject this encoder
// even if it still holds the raw key. Idempotent: re-revoking is a no-op and
// the original revoked_at_ms is preserved.
func RevokeEncoder(ctx context.Context, conn *sql.DB, encoderID string, nowMs int64) error {
	res, err := conn.ExecContext(ctx, `
		UPDATE encoders
		   SET revoked_at_ms = ?, status = ?
		 WHERE id = ? AND revoked_at_ms IS NULL`,
		nowMs, string(EncoderStatusOffline), encoderID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Could be already-revoked OR missing; distinguish for clearer errors.
		var exists int
		if err := conn.QueryRowContext(ctx,
			`SELECT 1 FROM encoders WHERE id = ?`, encoderID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("encoder %s %w", encoderID, ErrEncoderNotRegistered)
			}
			return err
		}
		// already revoked: idempotent success
	}
	return nil
}

// DeleteEncoder removes an encoder row. Any media_packages currently leased
// to this encoder transition back to pending (with last_attempt_error =
// "encoder deleted") so they're picked up by another claim instead of stuck
// in processing forever; the encoder_jobs rows are cleared in the same
// transaction. Returns an error only if the encoder is not registered.
func DeleteEncoder(ctx context.Context, conn *sql.DB, encoderID string, nowMs int64) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM encoders WHERE id = ?`, encoderID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("encoder %s %w", encoderID, ErrEncoderNotRegistered)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE media_packages
		   SET status = ?, last_attempt_error = ?, error = NULL, updated_at_ms = ?
		 WHERE status = ?
		   AND id IN (SELECT package_id FROM encoder_jobs WHERE encoder_id = ?)`,
		string(PackageStatusPending), "encoder deleted", nowMs,
		string(PackageStatusProcessing), encoderID); err != nil {
		return fmt.Errorf("release active leases: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encoder_jobs WHERE encoder_id = ?`, encoderID); err != nil {
		return fmt.Errorf("delete leases: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encoders WHERE id = ?`, encoderID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetEncoderByAPIKey looks up an encoder by raw API key. Returns nil when the
// key does not match any registered encoder. Revoked encoders are returned
// (with RevokedAtMs populated) so callers can log 'revoked key used' rather
// than 'unknown key'. Job-op helpers enforce the revoke check themselves.
func GetEncoderByAPIKey(ctx context.Context, conn *sql.DB, rawAPIKey string) (*Encoder, error) {
	hash := hashAPIKey(rawAPIKey)
	return getEncoderWhere(ctx, conn, "api_key_hash = ?", hash)
}

// GetEncoderByID returns the encoder row by id, or nil if no such encoder.
func GetEncoderByID(ctx context.Context, conn *sql.DB, encoderID string) (*Encoder, error) {
	return getEncoderWhere(ctx, conn, "id = ?", encoderID)
}

// ListEncoders returns every registered encoder ordered by created_at_ms.
// Revoked encoders are included so the admin UI can show them as audit
// history; callers filter on RevokedAtMs if they want only active rows.
// The api_key_hash field is populated but should not be exposed beyond the
// admin server — it has no operational use outside of GetEncoderByAPIKey.
func ListEncoders(ctx context.Context, conn *sql.DB) ([]Encoder, error) {
	return queryRows(ctx, conn, scanEncoder, `
		SELECT id, name, api_key_hash, capabilities, last_seen_ms, status, created_at_ms, revoked_at_ms, concurrency
		  FROM encoders
		 ORDER BY created_at_ms, id`)
}

// ListEncoderJobSummaries returns every active encoder job joined with its
// media title and profile so the admin UI can show what each encoder is
// currently working on. Encoders with no active lease are omitted.
func ListEncoderJobSummaries(ctx context.Context, conn *sql.DB) ([]EncoderJobSummary, error) {
	return queryRows(ctx, conn, scanEncoderJobSummary, `
		SELECT j.encoder_id, j.package_id, p.media_id, m.title, p.rendition_profile,
		       j.progress_pct, j.lease_expires_ms, j.claimed_at_ms
		  FROM encoder_jobs j
		  JOIN media_packages p ON p.id = j.package_id
		  LEFT JOIN media m ON m.id = p.media_id
		 ORDER BY j.claimed_at_ms`)
}

// LocalWorkerJob is the read-side view of a job currently being processed by
// the local packager-worker. Local jobs have no encoder_jobs row (leases are
// remote-only in v1), so this query identifies them as processing packages
// without a matching lease.
type LocalWorkerJob struct {
	PackageID   string
	MediaID     string
	MediaTitle  string
	Profile     string
	ClaimedAtMs int64
}

// ListLocalWorkerJobs returns every media_packages row in 'processing' that
// has no matching encoder_jobs lease. These are jobs being handled by the
// local packager-worker goroutines. Stale rows (left behind by a crashed
// worker) are included; callers should filter by updated_at_ms if they want
// to exclude them.
func ListLocalWorkerJobs(ctx context.Context, conn *sql.DB) ([]LocalWorkerJob, error) {
	return queryRows(ctx, conn, scanLocalWorkerJob, `
		SELECT p.id, p.media_id, m.title, p.rendition_profile, p.updated_at_ms
		  FROM media_packages p
		  LEFT JOIN media m ON m.id = p.media_id
		 WHERE p.status = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM encoder_jobs j WHERE j.package_id = p.id
		   )
		 ORDER BY p.updated_at_ms DESC`, string(PackageStatusProcessing))
}

// UpdateEncoderCapabilities replaces the opaque capabilities JSON and marks
// the encoder online. The admin server uses this when an encoder reports host
// metadata via the bearer-auth ping path.
func UpdateEncoderCapabilities(ctx context.Context, conn *sql.DB, encoderID, capabilities string, nowMs int64) error {
	capabilities = strings.TrimSpace(capabilities)
	if capabilities == "" {
		capabilities = "{}"
	}
	res, err := conn.ExecContext(ctx, `
		UPDATE encoders
		   SET capabilities = ?, last_seen_ms = ?, status = ?
		 WHERE id = ? AND revoked_at_ms IS NULL`,
		capabilities, nowMs, string(EncoderStatusOnline), encoderID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("encoder %s %w or revoked", encoderID, ErrEncoderNotRegistered)
	}
	return nil
}

func getEncoderWhere(ctx context.Context, conn *sql.DB, whereClause string, arg any) (*Encoder, error) {
	e, err := scanEncoder(conn.QueryRowContext(ctx, `
		SELECT id, name, api_key_hash, capabilities, last_seen_ms, status, created_at_ms, revoked_at_ms, concurrency
		  FROM encoders
		 WHERE `+whereClause, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func scanEncoder(row scanner) (Encoder, error) {
	var e Encoder
	var status string
	var revokedAt sql.NullInt64
	if err := row.Scan(&e.ID, &e.Name, &e.APIKeyHash, &e.Capabilities, &e.LastSeenMs, &status, &e.CreatedAtMs, &revokedAt, &e.Concurrency); err != nil {
		return Encoder{}, err
	}
	e.Status = EncoderStatus(status)
	if revokedAt.Valid {
		v := revokedAt.Int64
		e.RevokedAtMs = &v
	}
	return e, nil
}

func scanEncoderJobSummary(row scanner) (EncoderJobSummary, error) {
	var j EncoderJobSummary
	var title sql.NullString
	var progressPct sql.NullInt64
	if err := row.Scan(&j.EncoderID, &j.PackageID, &j.MediaID, &title, &j.Profile, &progressPct, &j.LeaseExpiresMs, &j.ClaimedAtMs); err != nil {
		return EncoderJobSummary{}, err
	}
	if title.Valid {
		j.MediaTitle = title.String
	}
	if progressPct.Valid {
		v := progressPct.Int64
		j.ProgressPct = &v
	}
	return j, nil
}

func scanLocalWorkerJob(row scanner) (LocalWorkerJob, error) {
	var j LocalWorkerJob
	var title sql.NullString
	if err := row.Scan(&j.PackageID, &j.MediaID, &title, &j.Profile, &j.ClaimedAtMs); err != nil {
		return LocalWorkerJob{}, err
	}
	if title.Valid {
		j.MediaTitle = title.String
	}
	return j, nil
}

type channelEncoderPolicy struct {
	channelID string
	policy    sql.NullString
}

func scanChannelEncoderPolicy(row scanner) (channelEncoderPolicy, error) {
	var p channelEncoderPolicy
	err := row.Scan(&p.channelID, &p.policy)
	return p, err
}

type leaseExpiryCandidate struct {
	packageID string
	encoderID string
}

func scanLeaseExpiryCandidate(row scanner) (leaseExpiryCandidate, error) {
	var c leaseExpiryCandidate
	err := row.Scan(&c.packageID, &c.encoderID)
	return c, err
}

// newEncoderID produces enc_<16 hex chars> — 64 bits of entropy. Collisions
// are practically impossible at any realistic encoder count.
func newEncoderID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "enc_" + hex.EncodeToString(buf[:]), nil
}

// newAPIKey returns (raw, hash) where raw is what the operator copies into
// the encoder config, and hash is what gets persisted. Caller is responsible
// for treating raw as sensitive — log/print it exactly once at registration.
func newAPIKey() (raw, hash string, err error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", err
	}
	raw = apiKeyPrefix + hex.EncodeToString(buf[:])
	return raw, hashAPIKey(raw), nil
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// concurrencyCapForClaim returns the active concurrency cap for a claim being
// processed inside tx. For both remote and local claims the cap lives in the
// encoder row's concurrency column. For local claims (EncoderID==""), the
// local encoder row is found via the local_encoder_id settings key; if no
// local encoder has registered yet, returns 0 (no claims allowed).
func concurrencyCapForClaim(ctx context.Context, tx *sql.Tx, isRemote bool, encoderID string) (int, error) {
	if isRemote {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT concurrency FROM encoders WHERE id = ?`, encoderID).Scan(&n); err != nil {
			return 0, fmt.Errorf("read encoder concurrency: %w", err)
		}
		return n, nil
	}
	// Local claim: find the local encoder row via the persisted ID.
	var idRaw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, localEncoderIDSettingKey).Scan(&idRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // no local encoder registered yet
	}
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", localEncoderIDSettingKey, err)
	}
	var localID string
	if jsonErr := json.Unmarshal([]byte(idRaw), &localID); jsonErr != nil || localID == "" {
		return 0, nil
	}
	var n int
	err = tx.QueryRowContext(ctx,
		`SELECT concurrency FROM encoders WHERE id = ? AND revoked_at_ms IS NULL`, localID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // local encoder deleted or revoked
	}
	if err != nil {
		return 0, fmt.Errorf("read local encoder concurrency: %w", err)
	}
	return n, nil
}

// UpdateEncoderConcurrency sets how many simultaneous jobs this encoder may
// hold. A value of 0 disables the encoder (no new claims will be accepted).
// Values < 0 are rejected. Returns an error if the encoder is not registered.
func UpdateEncoderConcurrency(ctx context.Context, conn *sql.DB, encoderID string, concurrency int) error {
	if concurrency < 0 {
		return fmt.Errorf("concurrency must be >= 0, got %d", concurrency)
	}
	res, err := conn.ExecContext(ctx,
		`UPDATE encoders SET concurrency = ? WHERE id = ?`,
		concurrency, encoderID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("encoder %s %w", encoderID, ErrEncoderNotRegistered)
	}
	return nil
}

// policyAllowsClaim resolves the strictest encoder_policy across every channel
// containing this media. A media item not present in any channel (operator
// orphan request) accepts either claim type. The result is intentionally
// black-and-white: 'remote_preferred' is a hint to the discovery layer, not a
// hard block on the claim itself, so it returns allow here.
func policyAllowsClaim(ctx context.Context, tx *sql.Tx, mediaID string, isRemote bool) (bool, string, error) {
	policies, err := queryRows(ctx, tx, scanChannelEncoderPolicy, `
		SELECT DISTINCT c.id, c.encoder_policy
		  FROM channels c
		  JOIN channel_media cm ON cm.channel_id = c.id
		 WHERE cm.media_id = ?`, mediaID)
	if err != nil {
		return false, "", err
	}
	for _, row := range policies {
		if !row.policy.Valid || row.policy.String == "" || row.policy.String == string(EncoderPolicyAny) || row.policy.String == string(EncoderPolicyRemotePreferred) {
			continue
		}
		switch EncoderPolicy(row.policy.String) {
		case EncoderPolicyRemoteOnly:
			if !isRemote {
				return false, fmt.Sprintf("channel %s has encoder_policy=remote_only", row.channelID), nil
			}
		case EncoderPolicyLocalOnly:
			if isRemote {
				return false, fmt.Sprintf("channel %s has encoder_policy=local_only", row.channelID), nil
			}
		}
	}
	return true, "", nil
}

// HeartbeatEncoderJob extends the lease for an in-flight job. All four
// invariants must hold inside one transaction:
//  1. The encoder is registered and not revoked.
//  2. The encoder_jobs row exists and is owned by this encoder.
//  3. The corresponding media_packages row is still 'processing'.
//  4. The new lease_expires_ms is strictly greater than the previous.
//
// progressPct may be nil to leave the previous value untouched. Returns the
// renewed lease_expires_ms, or an error if any invariant fails. A failed
// heartbeat is the encoder's signal to drop the job and let the sweeper
// reclaim it as a transient attempt failure.
func HeartbeatEncoderJob(ctx context.Context, conn *sql.DB, packageID, encoderID string, leaseTTL time.Duration, progressPct *int, nowMs int64) (newLeaseExpiresMs int64, err error) {
	if leaseTTL <= 0 {
		return 0, errors.New("leaseTTL must be positive")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := requireActiveEncoder(ctx, tx, encoderID); err != nil {
		return 0, err
	}
	if err := requireJobOwnership(ctx, tx, packageID, encoderID); err != nil {
		return 0, err
	}
	if err := requirePackageProcessing(ctx, tx, packageID); err != nil {
		return 0, err
	}

	newLease := nowMs + leaseTTL.Milliseconds()
	args := []any{newLease, nowMs}
	setProgress := ""
	if progressPct != nil {
		setProgress = ", progress_pct = ?"
		args = append(args, *progressPct)
	}
	args = append(args, packageID, encoderID)

	if _, err := tx.ExecContext(ctx,
		`UPDATE encoder_jobs
		    SET lease_expires_ms = ?, last_heartbeat_ms = ?`+setProgress+`
		  WHERE package_id = ? AND encoder_id = ?`, args...); err != nil {
		return 0, fmt.Errorf("update lease: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE encoders SET last_seen_ms = ?, status = CASE WHEN status = 'pending' THEN 'online' ELSE status END WHERE id = ?`, nowMs, encoderID); err != nil {
		return 0, err
	}
	return newLease, tx.Commit()
}

// ActiveEncoderLeaseForMedia returns the package id for an active processing
// lease held by encoderID on mediaID. It is intentionally media-scoped for the
// source download endpoint: a claimed package authorizes downloading exactly
// that source media, regardless of rendition profile.
func ActiveEncoderLeaseForMedia(ctx context.Context, conn *sql.DB, mediaID, encoderID string, nowMs int64) (string, bool, error) {
	var packageID string
	err := conn.QueryRowContext(ctx, `
		SELECT p.id
		  FROM media_packages p
		  JOIN encoder_jobs j ON j.package_id = p.id
		 WHERE p.media_id = ?
		   AND p.status = ?
		   AND j.encoder_id = ?
		   AND j.lease_expires_ms >= ?
		 ORDER BY j.lease_expires_ms DESC
		 LIMIT 1`,
		mediaID, string(PackageStatusProcessing), encoderID, nowMs).Scan(&packageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return packageID, true, nil
}

// CompleteEncoderJob is the DB-side completion primitive. It is the caller's
// responsibility (typically the /complete HTTP handler after finalizing
// segments and writing packaged_segments rows) to ensure the filesystem and
// dependent tables are consistent before invoking this. This helper only:
//   - verifies the encoder is registered and not revoked
//   - verifies the encoder still owns the lease (lease may have expired but
//     the sweeper hasn't run yet — we accept that race here; the sweeper's
//     UPDATE will no-op once the row is ready)
//   - verifies the package is still 'processing'
//   - transitions media_packages → 'ready' with cleared errors
//   - deletes the encoder_jobs row
func CompleteEncoderJob(ctx context.Context, conn *sql.DB, packageID, encoderID string, finalized FinalizedPackage, nowMs int64) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := requireActiveEncoder(ctx, tx, encoderID); err != nil {
		return err
	}
	if err := requireJobOwnership(ctx, tx, packageID, encoderID); err != nil {
		return err
	}
	if err := requirePackageProcessing(ctx, tx, packageID); err != nil {
		return err
	}

	if _, err := ApplyFinalizedPackageTransition(ctx, tx, packageID, finalized, nowMs); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM encoder_jobs WHERE package_id = ?`, packageID); err != nil {
		return fmt.Errorf("delete lease: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE encoders SET last_seen_ms = ?, status = CASE WHEN status = 'pending' THEN 'online' ELSE status END WHERE id = ?`, nowMs, encoderID); err != nil {
		return err
	}
	return tx.Commit()
}

// FinalizedPackage carries the post-encode metadata that CompleteEncoderJob
// writes onto the media_packages row. Mirrors MediaPackage's metadata fields
// but is decoupled from the schema struct so handlers can populate it without
// constructing a full MediaPackage. All fields are nullable to allow probe
// failures or missing tracks to surface as NULL.
type FinalizedPackage struct {
	PackageRoot        *string
	InitSegmentPath    *string
	SegmentBasePath    *string
	Container          *string
	VideoCodec         *string
	VideoProfile       *string
	VideoWidth         *int64
	VideoHeight        *int64
	AudioCodec         *string
	AudioProfile       *string
	Timescale          *int64
	PackagedDurationMs *int64
}

// resolveAndApplyFailure reads the current attempts for a processing package,
// resolves the target status via resolveFailureStatus, applies the transition,
// and emits the state-change metric. It returns the new status, the profile
// (for any additional caller-side metrics), and an error if the package is
// not processing or the transition fails.
func resolveAndApplyFailure(ctx context.Context, tx *sql.Tx, packageID, kind, reason string, maxAttempts int, nowMs int64) (PackageStatus, string, error) {
	var attempts int
	var profile string
	if err := tx.QueryRowContext(ctx,
		`SELECT attempts, rendition_profile FROM media_packages WHERE id = ? AND status = ?`,
		packageID, string(PackageStatusProcessing)).Scan(&attempts, &profile); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("package %s is %w", packageID, ErrPackageNotProcessing)
		}
		return "", "", err
	}

	newStatus := resolveFailureStatus(kind, attempts, maxAttempts)
	if err := applyFailureTransition(ctx, tx, packageID, newStatus, reason, nowMs); err != nil {
		return "", "", err
	}
	metrics.PackageStateTransitionsTotal.WithLabelValues(profile, string(PackageStatusProcessing), string(newStatus)).Inc()
	return newStatus, profile, nil
}

// FailEncoderJob is invoked by the encoder when it cannot finish its claim.
// kind=transient means the work didn't complete but the package itself may
// still be encodable — sends the package back to pending unless the attempts
// cap has been reached. kind=terminal means the encoder has determined the
// package cannot be made (source missing, codec-incompatible, etc.) and
// short-circuits to failed regardless of attempts.
//
// maxAttempts is the cap-from-transient policy threshold; pass 0 to disable
// auto-promotion (only explicit terminal kind will fail terminally). The
// cap is exclusive: attempts >= maxAttempts promotes a transient fail to
// terminal. Since ClaimPackage increments attempts at claim time, the cap
// counts actual claims, not failures.
func FailEncoderJob(ctx context.Context, conn *sql.DB, packageID, encoderID, kind, reason string, maxAttempts int, nowMs int64) (PackageStatus, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "transient" && kind != "terminal" {
		return "", fmt.Errorf("kind must be 'transient' or 'terminal', got %q", kind)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "encoder reported failure"
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if err := requireActiveEncoder(ctx, tx, encoderID); err != nil {
		return "", err
	}
	if err := requireJobOwnership(ctx, tx, packageID, encoderID); err != nil {
		return "", err
	}
	newStatus, _, err := resolveAndApplyFailure(ctx, tx, packageID, kind, reason, maxAttempts, nowMs)
	if err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM encoder_jobs WHERE package_id = ?`, packageID); err != nil {
		return "", fmt.Errorf("delete lease: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE encoders SET last_seen_ms = ?, status = CASE WHEN status = 'pending' THEN 'online' ELSE status END WHERE id = ?`, nowMs, encoderID); err != nil {
		return "", err
	}
	return newStatus, tx.Commit()
}

// LeaseExpiryResult describes a single sweeper action so the caller can log
// or surface per-package outcomes. The sweeper loop in the admin server is a
// thin wrapper around LeaseExpiredJobs that emits metrics from this slice.
type LeaseExpiryResult struct {
	PackageID string
	EncoderID string
	NewStatus PackageStatus
	Attempts  int
}

// LeaseExpiredJobs finds every encoder_jobs row whose lease has passed and
// transitions the corresponding media_packages row back to pending (transient
// attempt failure) or to failed (attempts cap reached). The expired lease row
// is deleted in the same transaction so two sweeper passes can't double-count
// the same failure.
//
// Returns one result per processed lease so callers can log a count of pending
// vs terminal transitions. Errors abort the sweep; partial progress is
// committed (each lease is its own transaction).
func LeaseExpiredJobs(ctx context.Context, conn *sql.DB, nowMs int64, maxAttempts int) ([]LeaseExpiryResult, error) {
	candidates, err := queryRows(ctx, conn, scanLeaseExpiryCandidate, `
		SELECT package_id, encoder_id
		  FROM encoder_jobs
		 WHERE lease_expires_ms < ?
		 ORDER BY lease_expires_ms`, nowMs)
	if err != nil {
		return nil, err
	}

	var results []LeaseExpiryResult
	for _, c := range candidates {
		newStatus, attempts, err := expireOneLease(ctx, conn, c.packageID, maxAttempts, nowMs)
		if err != nil {
			return results, fmt.Errorf("expire lease %s: %w", c.packageID, err)
		}
		if newStatus == "" {
			// Race: row already gone or already not processing. Skip silently.
			continue
		}
		results = append(results, LeaseExpiryResult{
			PackageID: c.packageID,
			EncoderID: c.encoderID,
			NewStatus: newStatus,
			Attempts:  attempts,
		})
	}
	return results, nil
}

func expireOneLease(ctx context.Context, conn *sql.DB, packageID string, maxAttempts int, nowMs int64) (PackageStatus, int, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()

	// Re-check that the package is still processing inside the tx; another
	// caller (CompleteEncoderJob, FailEncoderJob) may have raced us.
	var attempts int
	var profile string
	err = tx.QueryRowContext(ctx,
		`SELECT attempts, rendition_profile FROM media_packages WHERE id = ? AND status = ?`,
		packageID, string(PackageStatusProcessing)).Scan(&attempts, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		// Clean up the orphan lease row regardless.
		_, _ = tx.ExecContext(ctx, `DELETE FROM encoder_jobs WHERE package_id = ?`, packageID)
		return "", 0, tx.Commit()
	}
	if err != nil {
		return "", 0, err
	}

	newStatus := resolveFailureStatus("transient", attempts, maxAttempts)
	if err := applyFailureTransition(ctx, tx, packageID, newStatus, "encoder lease expired", nowMs); err != nil {
		return "", 0, err
	}
	metrics.PackageStateTransitionsTotal.WithLabelValues(profile, string(PackageStatusProcessing), string(newStatus)).Inc()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM encoder_jobs WHERE package_id = ?`, packageID); err != nil {
		return "", 0, err
	}
	return newStatus, attempts, tx.Commit()
}

// resolveFailureStatus encodes the attempts-cap policy in one place. kind is
// 'transient' or 'terminal'. maxAttempts=0 disables auto-promotion: a
// transient fail always returns pending even past 1000 attempts (intended for
// tests or operators who want explicit terminal kind to be the only failure
// route).
func resolveFailureStatus(kind string, attempts, maxAttempts int) PackageStatus {
	if kind == "terminal" {
		return PackageStatusFailed
	}
	if maxAttempts > 0 && attempts >= maxAttempts {
		return PackageStatusFailed
	}
	return PackageStatusPending
}

// applyFailureTransition runs the UPDATE that takes a processing row to either
// pending (transient with attempts remaining) or failed (terminal or cap hit).
// Pending sets last_attempt_error and clears error. Failed sets error to the
// reason (preserving the transient context if any) and clears last_attempt_error
// since terminal failures are visible to the operator via error.
func applyFailureTransition(ctx context.Context, tx *sql.Tx, packageID string, newStatus PackageStatus, reason string, nowMs int64) error {
	switch newStatus {
	case PackageStatusPending:
		if _, err := tx.ExecContext(ctx, `
			UPDATE media_packages
			   SET status = ?, last_attempt_error = ?, error = NULL, updated_at_ms = ?
			 WHERE id = ? AND status = ?`,
			string(PackageStatusPending), reason, nowMs, packageID, string(PackageStatusProcessing)); err != nil {
			return fmt.Errorf("transition to pending: %w", err)
		}
	case PackageStatusFailed:
		if _, err := tx.ExecContext(ctx, `
			UPDATE media_packages
			   SET status = ?, error = ?, last_attempt_error = NULL, updated_at_ms = ?
			 WHERE id = ? AND status = ?`,
			string(PackageStatusFailed), reason, nowMs, packageID, string(PackageStatusProcessing)); err != nil {
			return fmt.Errorf("transition to failed: %w", err)
		}
	default:
		return fmt.Errorf("unsupported failure target status %q", newStatus)
	}
	return nil
}

func requireActiveEncoder(ctx context.Context, tx *sql.Tx, encoderID string) error {
	var revokedAtMs sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT revoked_at_ms FROM encoders WHERE id = ?`, encoderID).Scan(&revokedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("encoder %s %w", encoderID, ErrEncoderNotRegistered)
	}
	if err != nil {
		return err
	}
	if revokedAtMs.Valid {
		return fmt.Errorf("encoder %s is %w", encoderID, ErrEncoderRevoked)
	}
	return nil
}

func requireJobOwnership(ctx context.Context, tx *sql.Tx, packageID, encoderID string) error {
	var owner string
	err := tx.QueryRowContext(ctx,
		`SELECT encoder_id FROM encoder_jobs WHERE package_id = ?`, packageID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w for package %s", ErrNoActiveLease, packageID)
	}
	if err != nil {
		return err
	}
	if owner != encoderID {
		return fmt.Errorf("package %s is %w %s, not %s", packageID, ErrPackageLeasedByOther, owner, encoderID)
	}
	return nil
}

// localEncoderIDSettingKey is the settings key that stores the encoder row ID
// auto-registered by the packager-worker on first run.
const localEncoderIDSettingKey = "local_encoder_id"

// EnsureLocalEncoder returns the encoder row ID for the in-process packager-worker,
// registering a new row the first time it is called. The ID is persisted in the
// settings table so it survives process restarts. If the stored ID points to a
// deleted or revoked row, a fresh registration replaces it.
func EnsureLocalEncoder(ctx context.Context, conn *sql.DB, name string, nowMs int64) (string, error) {
	raw, ok, err := getSetting(ctx, conn, localEncoderIDSettingKey)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", localEncoderIDSettingKey, err)
	}
	if ok {
		var id string
		if jsonErr := json.Unmarshal([]byte(raw), &id); jsonErr == nil && id != "" {
			enc, lookupErr := GetEncoderByID(ctx, conn, id)
			if lookupErr != nil {
				return "", fmt.Errorf("verify local encoder: %w", lookupErr)
			}
			if enc != nil && !enc.IsRevoked() {
				return id, nil
			}
		}
	}
	id, _, err := RegisterEncoder(ctx, conn, name, `{"type":"local"}`, nowMs)
	if err != nil {
		return "", fmt.Errorf("register local encoder: %w", err)
	}
	val, _ := json.Marshal(id)
	if err := setSetting(ctx, conn, localEncoderIDSettingKey, string(val)); err != nil {
		return "", fmt.Errorf("store %s: %w", localEncoderIDSettingKey, err)
	}
	return id, nil
}

// GetLocalEncoderID returns the encoder row ID stored by EnsureLocalEncoder,
// or "" if the packager-worker has never registered on this DB.
func GetLocalEncoderID(ctx context.Context, conn *sql.DB) string {
	raw, ok, _ := getSetting(ctx, conn, localEncoderIDSettingKey)
	if !ok {
		return ""
	}
	var id string
	if err := json.Unmarshal([]byte(raw), &id); err != nil {
		return ""
	}
	return id
}

func requirePackageProcessing(ctx context.Context, tx *sql.Tx, packageID string) error {
	var status string
	err := tx.QueryRowContext(ctx,
		`SELECT status FROM media_packages WHERE id = ?`, packageID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("package %s %w", packageID, ErrPackageNotFound)
	}
	if err != nil {
		return err
	}
	if PackageStatus(status) != PackageStatusProcessing {
		return fmt.Errorf("package %s is %s, %w", packageID, status, ErrPackageNotProcessing)
	}
	return nil
}
