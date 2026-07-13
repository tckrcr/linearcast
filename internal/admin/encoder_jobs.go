package admin

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/packager"
)

// Default lease TTL used when the encoder doesn't request a specific one.
// 60s gives ample headroom over the recommended 10–20s heartbeat cadence
// while keeping stale-encoder recovery (sweeper) reasonably responsive.
const defaultEncoderLeaseTTL = 60 * time.Second

// maxClaimAttempts caps how many candidates the /claim handler walks before
// giving up and returning 204. Prevents a pathological backlog of unclaimable
// rows (e.g. all blocked by policy) from holding the connection forever.
const maxClaimAttempts = 50

type encoderClaimRequest struct {
	// LeaseTTLSeconds requests a specific lease duration. The server caps it
	// to keep stuck encoders from holding work for an hour. Zero = default.
	LeaseTTLSeconds int `json:"leaseTtlSeconds,omitempty"`
}

type encoderClaimResponse struct {
	PackageID        string                 `json:"packageId"`
	MediaID          string                 `json:"mediaId"`
	MediaPath        string                 `json:"mediaPath"`
	RenditionProfile string                 `json:"renditionProfile"`
	Profile          packageprofile.Profile `json:"profile"`
	LeaseExpiresMs   int64                  `json:"leaseExpiresMs"`
	ClaimedAtMs      int64                  `json:"claimedAtMs"`
}

// handleEncoderClaim picks the next claimable package for this encoder and
// returns the resolved profile config inline so the encoder can build ffmpeg
// args without DB access. 204 No Content when no candidates are claimable.
//
// Walking is bounded by maxClaimAttempts; a pathological all-rejected backlog
// (e.g. every pending row is on a local_only channel) returns 204 instead of
// looping until timeout.
func (a *App) handleEncoderClaim(w http.ResponseWriter, r *http.Request) {
	enc := encoderFromContext(r.Context())
	if enc == nil {
		writeError(w, http.StatusUnauthorized, "missing_encoder", "encoder context not present")
		return
	}

	defer r.Body.Close()
	var req encoderClaimRequest
	if !decodeOptionalJSON(w, r, 1<<14, &req) {
		return
	}
	leaseTTL := defaultEncoderLeaseTTL
	if req.LeaseTTLSeconds > 0 {
		requested := time.Duration(req.LeaseTTLSeconds) * time.Second
		if requested > 10*time.Minute {
			requested = 10 * time.Minute
		}
		leaseTTL = requested
	}

	cands, err := packager.DiscoverCandidates(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "discover_failed", err.Error())
		return
	}
	if len(cands) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(cands) > maxClaimAttempts {
		cands = cands[:maxClaimAttempts]
	}

	nowMs := a.now().UTC().UnixMilli()
	for _, c := range cands {
		packageID := layout.ID(c.MediaID, c.Profile)
		ok, err := db.ClaimPackage(r.Context(), a.dbConn, db.ClaimRequest{
			MediaID:   c.MediaID,
			Profile:   c.Profile,
			PackageID: packageID,
			EncoderID: enc.ID,
			LeaseTTL:  leaseTTL,
			NowMs:     nowMs,
		})
		if err != nil {
			a.logger.Warn("claim failed",
				"media", c.MediaID,
				"profile", c.Profile,
				"encoder", enc.ID,
				"err", err,
			)
			continue
		}
		if !ok {
			continue
		}

		// Claimed. Load media + profile config for the response.
		media, err := db.MediaByID(r.Context(), a.dbConn, c.MediaID)
		if err != nil || media == nil {
			a.logger.Warn("claimed but media gone",
				"media", c.MediaID,
				"profile", c.Profile,
				"encoder", enc.ID,
				"err", err,
			)
			// Mark the claim failed; the encoder can't do anything with a vanished file.
			_, _ = db.FailEncoderJob(r.Context(), a.dbConn, packageID, enc.ID,
				"terminal", "source media row vanished after claim", 0, a.now().UTC().UnixMilli())
			continue
		}
		profile, err := db.GetPackageProfile(r.Context(), a.dbConn, c.Profile)
		if err != nil || profile == nil {
			a.logger.Warn("profile lookup failed",
				"profile", c.Profile,
				"err", err,
			)
			_, _ = db.FailEncoderJob(r.Context(), a.dbConn, packageID, enc.ID,
				"terminal", "profile config not found", 0, a.now().UTC().UnixMilli())
			continue
		}

		writeJSON(w, encoderClaimResponse{
			PackageID:        packageID,
			MediaID:          c.MediaID,
			MediaPath:        media.Path,
			RenditionProfile: c.Profile,
			Profile:          *profile,
			LeaseExpiresMs:   nowMs + leaseTTL.Milliseconds(),
			ClaimedAtMs:      nowMs,
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleEncoderMedia(w http.ResponseWriter, r *http.Request) {
	enc := encoderFromContext(r.Context())
	if enc == nil {
		writeError(w, http.StatusUnauthorized, "missing_encoder", "encoder context not present")
		return
	}
	mediaID := strings.TrimSpace(r.PathValue("mediaID"))
	if mediaID == "" {
		writeError(w, http.StatusBadRequest, "missing_media", "media id is required")
		return
	}
	nowMs := a.now().UTC().UnixMilli()
	if _, ok, err := db.ActiveEncoderLeaseForMedia(r.Context(), a.dbConn, mediaID, enc.ID, nowMs); err != nil {
		writeError(w, http.StatusInternalServerError, "lease_lookup_failed", err.Error())
		return
	} else if !ok {
		writeError(w, http.StatusConflict, "no_active_lease", "encoder does not hold an active lease for this media")
		return
	}

	media, err := db.MediaByID(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "media_lookup_failed", err.Error())
		return
	}
	if media == nil {
		writeError(w, http.StatusNotFound, "media_not_found", "media row not found")
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": filepath.Base(media.Path),
	}))
	http.ServeFile(w, r, media.Path)
}

type encoderHeartbeatRequest struct {
	LeaseTTLSeconds int  `json:"leaseTtlSeconds,omitempty"`
	ProgressPct     *int `json:"progressPct,omitempty"`
}

type encoderHeartbeatResponse struct {
	LeaseExpiresMs int64 `json:"leaseExpiresMs"`
}

func (a *App) handleEncoderHeartbeat(w http.ResponseWriter, r *http.Request) {
	enc := encoderFromContext(r.Context())
	if enc == nil {
		writeError(w, http.StatusUnauthorized, "missing_encoder", "encoder context not present")
		return
	}
	packageID := strings.TrimSpace(r.PathValue("packageID"))
	if packageID == "" {
		writeError(w, http.StatusBadRequest, "missing_package", "package id is required")
		return
	}
	defer r.Body.Close()
	var req encoderHeartbeatRequest
	if !decodeOptionalJSON(w, r, 1<<14, &req) {
		return
	}
	if req.ProgressPct != nil && (*req.ProgressPct < 0 || *req.ProgressPct > 100) {
		writeError(w, http.StatusBadRequest, "invalid_progress", "progressPct must be between 0 and 100")
		return
	}
	leaseTTL := defaultEncoderLeaseTTL
	if req.LeaseTTLSeconds > 0 {
		requested := time.Duration(req.LeaseTTLSeconds) * time.Second
		if requested > 10*time.Minute {
			requested = 10 * time.Minute
		}
		leaseTTL = requested
	}
	nowMs := a.now().UTC().UnixMilli()
	newLease, err := db.HeartbeatEncoderJob(r.Context(), a.dbConn, packageID, enc.ID, leaseTTL, req.ProgressPct, nowMs)
	if err != nil {
		status := classifyJobOpError(err)
		writeError(w, status, "heartbeat_failed", err.Error())
		return
	}
	if req.ProgressPct != nil {
		a.encoderBroadcaster.publish(encoderProgressEvent{
			PackageID:      packageID,
			ProgressPct:    req.ProgressPct,
			LeaseExpiresMs: newLease,
		})
	}
	writeJSON(w, encoderHeartbeatResponse{LeaseExpiresMs: newLease})
}

type encoderFailRequest struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

type encoderFailResponse struct {
	NewStatus string `json:"newStatus"`
}

type encoderCompleteResponse struct {
	OK               bool   `json:"ok"`
	PackageID        string `json:"packageId"`
	MediaID          string `json:"mediaId"`
	RenditionProfile string `json:"renditionProfile"`
	SegmentCount     int    `json:"segmentCount"`
	DurationMs       int64  `json:"durationMs"`
	PackageRoot      string `json:"packageRoot"`
	InitSegmentPath  string `json:"initSegmentPath"`
}

func (a *App) handleEncoderComplete(w http.ResponseWriter, r *http.Request) {
	enc := encoderFromContext(r.Context())
	if enc == nil {
		writeError(w, http.StatusUnauthorized, "missing_encoder", "encoder context not present")
		return
	}
	packageID := strings.TrimSpace(r.PathValue("packageID"))
	if packageID == "" {
		writeError(w, http.StatusBadRequest, "missing_package", "package id is required")
		return
	}
	defer r.Body.Close()

	pkg, err := db.MediaPackageByID(r.Context(), a.dbConn, packageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "package_lookup_failed", err.Error())
		return
	}
	if pkg == nil {
		writeError(w, http.StatusConflict, "package_not_found", "package not found")
		return
	}
	if err := a.requireEncoderLease(packageID, enc.ID); err != nil {
		writeError(w, classifyJobOpError(err), "complete_failed", err.Error())
		return
	}
	media, err := db.MediaByID(r.Context(), a.dbConn, pkg.MediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "media_lookup_failed", err.Error())
		return
	}
	if media == nil {
		writeError(w, http.StatusConflict, "media_not_found", "media row not found")
		return
	}
	if a.cache.Root() == "" {
		writeError(w, http.StatusInternalServerError, "package_root_missing", "CACHE_DIR is required")
		return
	}
	packageRoot := a.cache.PackageRoot(pkg.MediaID, pkg.RenditionProfile)
	if err := receivePackageTar(r.Body, packageRoot); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_package_tar", err.Error())
		return
	}
	res, finalized, err := packager.FinalizePackage(r.Context(), a.dbConn, packager.FinalizeOptions{
		MediaPath:        media.Path,
		MediaID:          pkg.MediaID,
		Profile:          pkg.RenditionProfile,
		OutputRoot:       a.cache.PackagesDir(),
		PackageID:        packageID,
		NowMs:            a.now().UTC().UnixMilli(),
		SourceDurationMs: media.DurationMs,
	})
	if err != nil {
		a.cleanupUnpromotedPackageUpload(packageID, packageRoot, "finalize_failed")
		writeError(w, http.StatusBadRequest, "finalize_failed", err.Error())
		return
	}
	if err := db.CompleteEncoderJob(r.Context(), a.dbConn, packageID, enc.ID, finalized, a.now().UTC().UnixMilli()); err != nil {
		a.cleanupUnpromotedPackageUpload(packageID, packageRoot, "complete_failed")
		writeError(w, classifyJobOpError(err), "complete_failed", err.Error())
		return
	}
	writeJSON(w, encoderCompleteResponse{
		OK:               true,
		PackageID:        res.PackageID,
		MediaID:          res.MediaID,
		RenditionProfile: res.RenditionProfile,
		SegmentCount:     res.SegmentCount,
		DurationMs:       res.DurationMs,
		PackageRoot:      res.PackageRoot,
		InitSegmentPath:  res.InitSegmentPath,
	})
}

func (a *App) cleanupUnpromotedPackageUpload(packageID, packageRoot, stage string) {
	if err := os.RemoveAll(packageRoot); err != nil {
		a.logger.Warn("encoder complete cleanup",
			"stage", stage,
			"package_id", packageID,
			"path", packageRoot,
			"err", err,
		)
	}
	if err := db.DeletePackagedSegments(context.Background(), a.dbConn, packageID); err != nil {
		a.logger.Warn("encoder complete segment cleanup",
			"stage", stage,
			"package_id", packageID,
			"err", err,
		)
	}
	if err := a.requeueLeaselessCompletionFailure(packageID, stage); err != nil {
		a.logger.Warn("encoder complete requeue cleanup",
			"stage", stage,
			"package_id", packageID,
			"err", err,
		)
	}
}

func (a *App) requeueLeaselessCompletionFailure(packageID, stage string) error {
	ctx := context.Background()
	var status string
	var leases int
	err := a.dbConn.QueryRowContext(ctx, `
		SELECT p.status, COUNT(j.package_id)
		  FROM media_packages p
		  LEFT JOIN encoder_jobs j ON j.package_id = p.id
		 WHERE p.id = ?
		 GROUP BY p.id, p.status`, packageID).Scan(&status, &leases)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != string(db.PackageStatusProcessing) || leases > 0 {
		return nil
	}
	settings, err := db.GetEncoderSweeperSettings(ctx, a.dbConn)
	if err != nil {
		return fmt.Errorf("read sweeper settings: %w", err)
	}
	_, err = db.MarkPackageFailedWithKind(ctx, a.dbConn, packageID, "transient",
		"encoder complete failed before ready: "+stage, settings.MaxAttempts, a.now().UTC().UnixMilli())
	return err
}

func (a *App) requireEncoderLease(packageID, encoderID string) error {
	var status, owner string
	err := a.dbConn.QueryRow(`
		SELECT p.status, j.encoder_id
		  FROM media_packages p
		  JOIN encoder_jobs j ON j.package_id = p.id
		 WHERE p.id = ?`, packageID).Scan(&status, &owner)
	if err != nil {
		return fmt.Errorf("%w for package %s", db.ErrNoActiveLease, packageID)
	}
	if owner != encoderID {
		return fmt.Errorf("package %s is %w %s", packageID, db.ErrPackageLeasedByOther, owner)
	}
	if status != string(db.PackageStatusProcessing) {
		return fmt.Errorf("package %s is %w", packageID, db.ErrPackageNotProcessing)
	}
	return nil
}

func receivePackageTar(body io.Reader, packageRoot string) error {
	parent := filepath.Dir(packageRoot)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create package parent: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, ".upload-*")
	if err != nil {
		return fmt.Errorf("create upload temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	seen := map[string]bool{}
	tr := tar.NewReader(body)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name, err := validatePackageTarEntry(hdr)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		target := filepath.Join(tmp, name)
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return fmt.Errorf("write %s: %w", name, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %s: %w", name, err)
		}
		seen[name] = true
	}
	if !seen[layout.InitName] {
		return fmt.Errorf("package tar must contain %s", layout.InitName)
	}
	if !seen[layout.PlaylistName] {
		return fmt.Errorf("package tar must contain %s", layout.PlaylistName)
	}
	hasSegment := false
	for name := range seen {
		if layout.IsSegment(name) {
			hasSegment = true
			break
		}
	}
	if !hasSegment {
		return fmt.Errorf("package tar must contain at least one %s", layout.SegmentGlob)
	}
	if err := os.RemoveAll(packageRoot); err != nil {
		return fmt.Errorf("clear package root: %w", err)
	}
	if err := os.Rename(tmp, packageRoot); err != nil {
		return fmt.Errorf("publish package root: %w", err)
	}
	return nil
}

func validatePackageTarEntry(hdr *tar.Header) (string, error) {
	name := filepath.ToSlash(strings.TrimSpace(hdr.Name))
	name = strings.TrimPrefix(name, "./")
	if name == "" {
		return "", nil
	}
	if strings.Contains(name, "/") || strings.HasPrefix(name, ".") || filepath.IsAbs(name) {
		return "", fmt.Errorf("tar entry %q is not allowed", hdr.Name)
	}
	if hdr.Typeflag == tar.TypeDir {
		return "", nil
	}
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
		return "", fmt.Errorf("tar entry %q is not a regular file", hdr.Name)
	}
	if name != layout.InitName && name != layout.PlaylistName && !layout.IsSegment(name) {
		return "", fmt.Errorf("tar entry %q is not part of the package contract", hdr.Name)
	}
	return name, nil
}

func (a *App) handleEncoderFail(w http.ResponseWriter, r *http.Request) {
	enc := encoderFromContext(r.Context())
	if enc == nil {
		writeError(w, http.StatusUnauthorized, "missing_encoder", "encoder context not present")
		return
	}
	packageID := strings.TrimSpace(r.PathValue("packageID"))
	if packageID == "" {
		writeError(w, http.StatusBadRequest, "missing_package", "package id is required")
		return
	}
	defer r.Body.Close()
	var req encoderFailRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return
	}
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if req.Kind != "transient" && req.Kind != "terminal" {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be 'transient' or 'terminal'")
		return
	}
	nowMs := a.now().UTC().UnixMilli()
	sweeperSettings, err := db.GetEncoderSweeperSettings(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to read sweeper settings")
		return
	}
	newStatus, err := db.FailEncoderJob(r.Context(), a.dbConn, packageID, enc.ID,
		req.Kind, req.Reason, sweeperSettings.MaxAttempts, nowMs)
	if err != nil {
		status := classifyJobOpError(err)
		writeError(w, status, "fail_failed", err.Error())
		return
	}
	writeJSON(w, encoderFailResponse{NewStatus: string(newStatus)})
}

// classifyJobOpError maps the typed sentinel errors returned by the encoder
// job-op helpers (HeartbeatEncoderJob / CompleteEncoderJob / FailEncoderJob and
// requireEncoderLease) onto HTTP status codes via errors.Is. Anything
// unrecognized is a 500.
func classifyJobOpError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, db.ErrEncoderNotRegistered):
		return http.StatusUnauthorized
	case errors.Is(err, db.ErrEncoderRevoked):
		return http.StatusForbidden
	case errors.Is(err, db.ErrNoActiveLease),
		errors.Is(err, db.ErrPackageLeasedByOther),
		errors.Is(err, db.ErrPackageNotProcessing),
		errors.Is(err, db.ErrPackageNotFound):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
