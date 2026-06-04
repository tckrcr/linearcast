package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/sysinfo"
)

// isEncoderRoute identifies requests that should use bearer-token auth
// (an HTTP-polling remote encoder) instead of cookie auth (a human operator
// in the admin UI). The encoder polling path is /api/encoder/*; admin CRUD
// for managing those rows lives under /api/admin/encoders/* and continues
// to use cookie auth.
func isEncoderRoute(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/encoder/")
}

// encoderContextKey is the type used to attach an authenticated encoder to
// a request context. Unexported so external packages can't construct or read
// the key directly.
type encoderContextKey struct{}

func contextWithEncoder(ctx context.Context, enc *db.Encoder) context.Context {
	return context.WithValue(ctx, encoderContextKey{}, enc)
}

func encoderFromContext(ctx context.Context) *db.Encoder {
	enc, _ := ctx.Value(encoderContextKey{}).(*db.Encoder)
	return enc
}

// requireEncoderAuth resolves the Authorization: Bearer header to a registered,
// non-revoked encoder and attaches it to the request context for the handler.
// Returns false when the request should be rejected; the response has already
// been written in that case.
func (a *App) requireEncoderAuth(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	raw := bearerToken(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, "missing_token", "Authorization: Bearer <api key> is required")
		return nil, false
	}
	enc, err := db.GetEncoderByAPIKey(r.Context(), a.dbConn, raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup_error", "could not validate api key")
		return nil, false
	}
	if enc == nil {
		writeError(w, http.StatusUnauthorized, "invalid_token", "api key is not registered")
		return nil, false
	}
	if enc.IsRevoked() {
		// 403 not 401 so operators can distinguish "key was once valid" from
		// "key was never valid" in logs without grep tricks.
		writeError(w, http.StatusForbidden, "revoked", "encoder has been revoked")
		return nil, false
	}
	return r.WithContext(contextWithEncoder(r.Context(), enc)), true
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes)).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
	return false
}

// --- Admin CRUD (cookie-authenticated) -----------------------------------

type encoderRegisterRequest struct {
	Name         string          `json:"name"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

type encoderRegisterResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	APIKey      string `json:"apiKey"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// handleEncoderRegister creates an encoder row and returns the raw API key
// in the response. The raw key is shown exactly once — only the SHA-256
// hash is persisted, so a lost key requires re-registering. The caller is
// the admin UI (cookie-authenticated human).
func (a *App) handleEncoderRegister(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req encoderRegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "name is required")
		return
	}
	capabilities := "{}"
	if len(req.Capabilities) > 0 {
		// Validate the caller sent real JSON, even if we don't introspect it.
		var probe any
		if err := json.Unmarshal(req.Capabilities, &probe); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_capabilities", "capabilities must be a JSON value")
			return
		}
		capabilities = string(req.Capabilities)
	}
	nowMs := a.now().UTC().UnixMilli()
	id, raw, err := db.RegisterEncoder(r.Context(), a.dbConn, req.Name, capabilities, nowMs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "register_failed", err.Error())
		return
	}
	writeJSON(w, encoderRegisterResponse{
		ID:          id,
		Name:        req.Name,
		APIKey:      raw,
		CreatedAtMs: nowMs,
	})
}

type encoderCurrentJob struct {
	PackageID      string `json:"packageId"`
	MediaID        string `json:"mediaId"`
	MediaTitle     string `json:"mediaTitle"`
	Profile        string `json:"profile"`
	ProgressPct    *int   `json:"progressPct,omitempty"`
	LeaseExpiresMs int64  `json:"leaseExpiresMs"`
	ClaimedAtMs    int64  `json:"claimedAtMs"`
}

type encoderListItem struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Capabilities json.RawMessage     `json:"capabilities"`
	Status       string              `json:"status"`
	LastSeenMs   int64               `json:"lastSeenMs"`
	CreatedAtMs  int64               `json:"createdAtMs"`
	RevokedAtMs  *int64              `json:"revokedAtMs,omitempty"`
	Concurrency  int                 `json:"concurrency"`
	Jobs         []encoderCurrentJob `json:"jobs,omitempty"`
}

// localWorkerItem is the Local Worker card surfaced in the encoder list.
// Enabled is derived from the encoder row's concurrency (>0 = enabled).
// The card is always emitted so the UI can render the disabled state.
type localWorkerItem struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Capabilities json.RawMessage     `json:"capabilities"`
	Status       string              `json:"status"`
	LastSeenMs   int64               `json:"lastSeenMs"`
	CreatedAtMs  int64               `json:"createdAtMs"`
	Enabled      bool                `json:"enabled"`
	Concurrency  int                 `json:"concurrency"`
	Jobs         []encoderCurrentJob `json:"jobs,omitempty"`
}

type encoderListResponse struct {
	Encoders    []encoderListItem `json:"encoders"`
	LocalWorker *localWorkerItem  `json:"localWorker"`
}

func (a *App) localWorkerCapabilities() json.RawMessage {
	hostname, _ := os.Hostname()
	reported := map[string]any{
		"hostname": hostname,
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
	}
	if gpus := sysinfo.DetectNVIDIAGPUs(context.Background()); len(gpus) > 0 {
		reported["nvidiaGpus"] = gpus
	}
	workDir := ""
	if a.cacheDir != "" {
		workDir = a.cacheDir + "/encoder-work"
	}
	if workDir != "" {
		if gb := sysinfo.DiskFreeGB(workDir); gb > 0 {
			reported["diskFreeGB"] = gb
		}
	}
	caps := map[string]any{
		"type":     "local",
		"reported": reported,
	}
	out, _ := json.Marshal(caps)
	return out
}

func (a *App) handleEncoderList(w http.ResponseWriter, r *http.Request) {
	rows, err := db.ListEncoders(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	jobs, err := db.ListEncoderJobSummaries(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_jobs_failed", err.Error())
		return
	}
	localJobs, err := db.ListLocalWorkerJobs(r.Context(), a.dbConn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_local_jobs_failed", err.Error())
		return
	}
	jobMap := make(map[string]db.EncoderJobSummary, len(jobs))
	for _, j := range jobs {
		jobMap[j.EncoderID] = j
	}

	nowMs := a.now().UTC().UnixMilli()

	// If the packager-worker has self-registered, pull its row out of the encoder
	// list and use it for the localWorker card so capabilities reflect live data.
	localEncoderID := db.GetLocalEncoderID(r.Context(), a.dbConn)
	var localEncoderRow *db.Encoder
	filtered := rows[:0]
	for i := range rows {
		if rows[i].ID == localEncoderID {
			e := rows[i]
			localEncoderRow = &e
		} else {
			filtered = append(filtered, rows[i])
		}
	}
	rows = filtered

	out := encoderListResponse{
		Encoders: make([]encoderListItem, 0, len(rows)),
	}

	for _, e := range rows {
		item := encoderListItem{
			ID:           e.ID,
			Name:         e.Name,
			Capabilities: json.RawMessage(e.Capabilities),
			Status:       string(e.Status),
			LastSeenMs:   e.LastSeenMs,
			CreatedAtMs:  e.CreatedAtMs,
			RevokedAtMs:  e.RevokedAtMs,
			Concurrency:  e.Concurrency,
		}
		if len(item.Capabilities) == 0 {
			item.Capabilities = json.RawMessage("{}")
		}
		if j, ok := jobMap[e.ID]; ok {
			cj := encoderCurrentJob{
				PackageID:      j.PackageID,
				MediaID:        j.MediaID,
				MediaTitle:     j.MediaTitle,
				Profile:        j.Profile,
				LeaseExpiresMs: j.LeaseExpiresMs,
				ClaimedAtMs:    j.ClaimedAtMs,
			}
			if j.ProgressPct != nil {
				v := int(*j.ProgressPct)
				cj.ProgressPct = &v
			}
			item.Jobs = append(item.Jobs, cj)
		}
		out.Encoders = append(out.Encoders, item)
	}

	// Local worker card is always emitted so the UI can render its toggle.
	// When the packager-worker has self-registered, use its live row for
	// capabilities, status, and concurrency. Fall back to a synthetic row
	// (capabilities only; concurrency 0 = not yet registered) otherwise.
	var localCaps json.RawMessage
	var localLastSeenMs, localCreatedAtMs int64
	var localConcurrency int
	localStatus := string(db.EncoderStatusOffline)
	if localEncoderRow != nil {
		localCaps = json.RawMessage(localEncoderRow.Capabilities)
		localLastSeenMs = localEncoderRow.LastSeenMs
		localCreatedAtMs = localEncoderRow.CreatedAtMs
		localConcurrency = localEncoderRow.Concurrency
		localStatus = string(localEncoderRow.Status)
		if localEncoderRow.Concurrency == 0 && len(localJobs) == 0 {
			localStatus = string(db.EncoderStatusOffline)
		}
	} else {
		localCaps = a.localWorkerCapabilities()
		localLastSeenMs = nowMs
	}
	if len(localCaps) == 0 {
		localCaps = json.RawMessage("{}")
	}

	localItem := localWorkerItem{
		ID:           "local",
		Name:         "Local Worker",
		Capabilities: localCaps,
		Status:       localStatus,
		LastSeenMs:   localLastSeenMs,
		CreatedAtMs:  localCreatedAtMs,
		Enabled:      localConcurrency > 0,
		Concurrency:  localConcurrency,
	}
	for _, j := range localJobs {
		localItem.Jobs = append(localItem.Jobs, encoderCurrentJob{
			PackageID:   j.PackageID,
			MediaID:     j.MediaID,
			MediaTitle:  j.MediaTitle,
			Profile:     j.Profile,
			ClaimedAtMs: j.ClaimedAtMs,
		})
	}
	out.LocalWorker = &localItem

	writeJSON(w, out)
}

type encoderUpdateRequest struct {
	Concurrency *int `json:"concurrency,omitempty"`
}

// handleEncoderUpdate accepts a PATCH with partial encoder fields. v1 only
// supports concurrency; rejecting unknown fields would be nice but the request
// schema is small enough that ignoring extras is fine.
func (a *App) handleEncoderUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "encoder id is required")
		return
	}
	defer r.Body.Close()
	var req encoderUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return
	}
	if req.Concurrency != nil {
		if *req.Concurrency < 1 {
			writeError(w, http.StatusBadRequest, "invalid_concurrency", "concurrency must be >= 1")
			return
		}
		if err := db.UpdateEncoderConcurrency(r.Context(), a.dbConn, id, *req.Concurrency); err != nil {
			if errors.Is(err, db.ErrEncoderNotRegistered) {
				writeError(w, http.StatusNotFound, "not_found", err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type localWorkerUpdateRequest struct {
	Concurrency *int `json:"concurrency,omitempty"`
}

// handleLocalWorkerUpdate is the admin PUT for the Local Worker card.
// Concurrency is written to the local encoder's row in the encoders table.
// Setting concurrency=0 disables the local worker (no new claims accepted).
// Returns 404 when the local encoder hasn't registered yet.
func (a *App) handleLocalWorkerUpdate(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req localWorkerUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return
	}
	if req.Concurrency != nil {
		if *req.Concurrency < 0 {
			writeError(w, http.StatusBadRequest, "invalid_concurrency", "concurrency must be >= 0")
			return
		}
		localID := db.GetLocalEncoderID(r.Context(), a.dbConn)
		if localID == "" {
			writeError(w, http.StatusNotFound, "no_local_encoder", "local encoder has not registered yet")
			return
		}
		if err := db.UpdateEncoderConcurrency(r.Context(), a.dbConn, localID, *req.Concurrency); err != nil {
			writeError(w, http.StatusInternalServerError, "set_concurrency_failed", err.Error())
			return
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleEncoderRevoke(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "encoder id is required")
		return
	}
	nowMs := a.now().UTC().UnixMilli()
	if err := db.RevokeEncoder(r.Context(), a.dbConn, id, nowMs); err != nil {
		if errors.Is(err, db.ErrEncoderNotRegistered) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "revoke_failed", err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleEncoderDelete removes an encoder row. Any media_packages currently
// leased to this encoder are released back to pending; encoder_jobs rows are
// cleared. 404 when the encoder isn't registered.
func (a *App) handleEncoderDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "encoder id is required")
		return
	}
	nowMs := a.now().UTC().UnixMilli()
	if err := db.DeleteEncoder(r.Context(), a.dbConn, id, nowMs); err != nil {
		if errors.Is(err, db.ErrEncoderNotRegistered) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- Encoder job ops (bearer-authenticated) ------------------------------

type encoderPingResponse struct {
	OK          bool   `json:"ok"`
	EncoderID   string `json:"encoderId"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Concurrency int    `json:"concurrency"`
	LastSeenMs  int64  `json:"lastSeenMs"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

type encoderPingReport struct {
	Hostname    string             `json:"hostname,omitempty"`
	OS          string             `json:"os,omitempty"`
	Arch        string             `json:"arch,omitempty"`
	FFmpegPath  string             `json:"ffmpegPath,omitempty"`
	FFprobePath string             `json:"ffprobePath,omitempty"`
	Encoders    []string           `json:"encoders,omitempty"`
	NVIDIAGPUs  []encoderGPUReport `json:"nvidiaGpus,omitempty"`
	WorkDir     string             `json:"workDir,omitempty"`
	DiskFreeGB  float64            `json:"diskFreeGB,omitempty"`
	Extra       map[string]string  `json:"extra,omitempty"`
}

type encoderGPUReport struct {
	Name          string `json:"name,omitempty"`
	DriverVersion string `json:"driverVersion,omitempty"`
}

func (a *App) handleEncoderPing(w http.ResponseWriter, r *http.Request) {
	enc := encoderFromContext(r.Context())
	if enc == nil {
		writeError(w, http.StatusUnauthorized, "missing_encoder", "encoder context not present")
		return
	}
	if r.Method == http.MethodPost {
		defer r.Body.Close()
		var req encoderPingReport
		if !decodeOptionalJSON(w, r, 1<<16, &req) {
			return
		}
		merged, err := mergeEncoderCapabilities(enc.Capabilities, req, clientIP(r), a.now().UTC().UnixMilli())
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_capabilities", err.Error())
			return
		}
		if err := db.UpdateEncoderCapabilities(r.Context(), a.dbConn, enc.ID, string(merged), a.now().UTC().UnixMilli()); err != nil {
			writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
		enc.Capabilities = string(merged)
		enc.LastSeenMs = a.now().UTC().UnixMilli()
		enc.Status = db.EncoderStatusOnline
	}
	writeJSON(w, encoderPingResponse{
		OK:          true,
		EncoderID:   enc.ID,
		Name:        enc.Name,
		Status:      string(enc.Status),
		Concurrency: enc.Concurrency,
		LastSeenMs:  enc.LastSeenMs,
		CreatedAtMs: enc.CreatedAtMs,
	})
}

func mergeEncoderCapabilities(existing string, report encoderPingReport, remoteAddr string, nowMs int64) (json.RawMessage, error) {
	var caps map[string]any
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &caps); err != nil {
			return nil, err
		}
	}
	if caps == nil {
		caps = map[string]any{}
	}
	caps["lastRemoteAddr"] = remoteAddr
	caps["lastReportMs"] = nowMs
	caps["reported"] = report
	out, err := json.Marshal(caps)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
