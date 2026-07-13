package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/liveproxy"
)

// upstreamProbeClient is used only for advisory reachability checks of the
// Spotify URL upstream. It mirrors the playback proxy's GET-based fetch but with
// a shorter timeout since the probe is interactive. Like external-HLS playback
// it permits any resolved address: the Spotify URL is an operator-set,
// private-by-nature service (e.g. a LAN Spotify→HLS bridge), so a probe that
// refused loopback/LAN would reject the real use case. See
// cmd/linearcast/runtime.go.
var upstreamProbeClient = liveproxy.NewGuardedClient(6*time.Second, liveproxy.AllowAllAddresses)

func validUpstreamHLSURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

type probeUpstreamRequest struct {
	UpstreamHLSURL string `json:"upstreamHlsUrl"`
}

type probeUpstreamResponse struct {
	Reachable    bool   `json:"reachable"`
	Status       int    `json:"status,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	LooksLikeHLS bool   `json:"looksLikeHls"`
	Error        string `json:"error,omitempty"`
}

// handleChannelProbeUpstream checks whether an upstream HLS URL is reachable.
// It is strictly advisory: it never creates or gates a channel. A failed probe
// still returns 200 with reachable=false so the UI can warn without blocking,
// matching the design intent that Spotify URLs are not reachability-gated.
func (a *App) handleChannelProbeUpstream(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req probeUpstreamRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.UpstreamHLSURL = strings.TrimSpace(req.UpstreamHLSURL)
	if !validUpstreamHLSURL(req.UpstreamHLSURL) {
		writeError(w, http.StatusBadRequest, "invalid_upstream_hls_url", "upstreamHlsUrl must be an http or https URL")
		return
	}
	// The interactive probe uses the guarded (deny-private) client so a paste of
	// a loopback/LAN URL is rejected the same way playback would refuse it.
	writeJSON(w, probeUpstreamHLSWith(r.Context(), upstreamProbeClient, req.UpstreamHLSURL))
}

// probeUpstreamHLSWith performs a single GET against the upstream and reports
// what it found. Transport errors are returned as reachable=false rather than
// as an HTTP error so the caller can surface an advisory warning. The client is
// passed in so the interactive probe (guarded, deny-private) and the background
// heartbeat (shared App client, matching fetchExternalNowPlaying) can share this
// logic.
func probeUpstreamHLSWith(ctx context.Context, client *http.Client, rawURL string) probeUpstreamResponse {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return probeUpstreamResponse{Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return probeUpstreamResponse{Error: err.Error()}
	}
	defer resp.Body.Close()

	out := probeUpstreamResponse{
		Reachable:   true,
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
	}
	// Read a small prefix so we can recognize an HLS playlist even when the
	// origin serves it with a generic content type.
	prefix, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if strings.Contains(strings.ToLower(out.ContentType), "mpegurl") ||
		strings.HasPrefix(strings.TrimSpace(string(prefix)), "#EXTM3U") {
		out.LooksLikeHLS = true
	}
	return out
}

func (a *App) handleChannelUpstreamHLSUpdate(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	defer r.Body.Close()
	var req struct {
		UpstreamHLSURL string `json:"upstreamHlsUrl"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.UpstreamHLSURL = strings.TrimSpace(req.UpstreamHLSURL)
	if !validUpstreamHLSURL(req.UpstreamHLSURL) {
		writeError(w, http.StatusBadRequest, "invalid_upstream_hls_url", "upstreamHlsUrl must be an http or https URL")
		return
	}
	updated, err := db.SetChannelUpstreamHLSURL(r.Context(), a.dbConn, channelID, req.UpstreamHLSURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "not_found", "channel not found or not an external channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
