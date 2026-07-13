package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/liveproxy"
)

// External HLS is a thin adapter over the proxy engine, internal/liveproxy: it
// builds a liveproxy.Proxy and calls Serve with a liveproxy.Request, so
// cooldown/backoff, the failure map, the SSRF-guarded dialer, manifest body
// limits, and response streaming are shared code.
// tokens, the redirect, timeline/keepalive) — does not exist for external HLS,
// which is a stateless GET-and-rewrite. What remains per adapter (upstream-URL
// derivation, header injection, manifest-rewrite rules, failure-key shape) is
// protocol-specific glue; folding it into one function would just reconstruct
// liveproxy.Request behind if-branches. liveproxy is the shared seam.
const (
	externalHLSTimeout       = 5 * time.Second
	externalHLSCooldown      = 5 * time.Second
	externalHLSLogSampleEach = 60 * time.Second
)

func (a *app) handleExternalHLSManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	upstream, ok := a.externalHLSURL(w, r, channelID)
	if !ok {
		return
	}
	a.proxyExternalHLS(w, r, channelID, upstream, "", "application/vnd.apple.mpegurl")
}

func (a *app) handleExternalHLSProxy(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	restPath := strings.TrimPrefix(r.PathValue("path"), "/")
	if !liveproxy.SafeRelativePath(restPath) {
		http.NotFound(w, r)
		return
	}
	upstream, ok := a.externalHLSURL(w, r, channelID)
	if !ok {
		return
	}
	proxiedPath := restPath
	if r.URL.RawQuery != "" {
		proxiedPath += "?" + r.URL.RawQuery
	}
	proxiedURL, err := externalHLSUpstreamURL(upstream, proxiedPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.proxyExternalHLS(w, r, channelID, proxiedURL, restPath, "")
}

func (a *app) externalHLSURL(w http.ResponseWriter, r *http.Request, channelID string) (*url.URL, bool) {
	ch, err := db.ChannelByID(r.Context(), a.dbConn, channelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("channel lookup: %v", err), http.StatusInternalServerError)
		return nil, false
	}
	if ch == nil || !ch.Enabled || ch.UpstreamHLSURL == nil {
		http.NotFound(w, r)
		return nil, false
	}
	u, err := url.Parse(*ch.UpstreamHLSURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "invalid upstream hls url", http.StatusBadGateway)
		return nil, false
	}
	return u, true
}

func (a *app) proxyExternalHLS(w http.ResponseWriter, r *http.Request, channelID string, upstream *url.URL, sourcePath, contentType string) {
	client := a.externalHLSClient
	if client == nil {
		client = a.httpClient
	}
	proxy := liveproxy.Proxy{
		Client:        client,
		Timeout:       externalHLSTimeout,
		Cooldown:      externalHLSCooldown,
		LogSampleEach: externalHLSLogSampleEach,
		State:         a.externalHLSState(),
		LogPrefix:     "external hls",
	}
	proxy.Serve(w, r, liveproxy.Request{
		Key:          externalHLSFailureKey(channelID, upstream),
		Upstream:     upstream.String(),
		LogUpstream:  upstream.Scheme + "://" + upstream.Host,
		LogFields:    "channel_id=" + channelID,
		ContentType:  contentType,
		CacheControl: "no-store",
		Body: func(resp *http.Response) (io.Reader, error) {
			if contentType != "application/vnd.apple.mpegurl" && !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl") && !strings.HasSuffix(sourcePath, ".m3u8") {
				return resp.Body, nil
			}
			body, err := liveproxy.ReadManifestBody(resp.Body)
			if err != nil {
				return nil, err
			}
			rewritten := liveproxy.RewriteManifest(body, sourcePath, func(raw, sourcePath string) (string, bool) {
				return externalHLSProxyURI(upstream, raw, sourcePath)
			})
			return strings.NewReader(string(rewritten)), nil
		},
	})
}

func (a *app) externalHLSState() *liveproxy.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.externalHLS == nil {
		a.externalHLS = liveproxy.NewState()
	}
	return a.externalHLS
}

func externalHLSFailureKey(channelID string, upstream *url.URL) string {
	return channelID + "|" + upstream.Scheme + "://" + upstream.Host
}

func externalHLSUpstreamURL(base *url.URL, proxiedPath string) (*url.URL, error) {
	u, err := url.Parse(proxiedPath)
	if err != nil || u.Scheme != "" || u.Host != "" || !liveproxy.SafeRelativePath(u.Path) {
		return nil, fmt.Errorf("invalid external hls path")
	}
	return base.ResolveReference(u), nil
}

func externalHLSProxyURI(base *url.URL, raw, sourcePath string) (string, bool) {
	if rel, ok := liveproxy.RelativePath(raw, sourcePath); ok {
		return externalProxyChildURI(sourcePath, rel), true
	}

	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Path == "" {
		return "", false
	}
	target := base.ResolveReference(u)
	if target.Scheme != base.Scheme || target.Host != base.Host {
		return "", false
	}
	baseDir := strings.TrimPrefix(path.Dir(base.Path), "/")
	targetPath := strings.TrimPrefix(target.Path, "/")
	if baseDir != "." && baseDir != "" {
		prefix := strings.TrimSuffix(baseDir, "/") + "/"
		if !strings.HasPrefix(targetPath, prefix) {
			return "", false
		}
		targetPath = strings.TrimPrefix(targetPath, prefix)
	}
	if !liveproxy.SafeRelativePath(targetPath) {
		return "", false
	}
	return externalProxyChildURI(sourcePath, liveproxy.AppendQuery(targetPath, target.RawQuery)), true
}

// externalProxyChildURI converts rel — a resource path rooted at the channel's
// /external/<id>/proxy/ mount — into a reference relative to the manifest being
// rewritten, so any reverse-proxy mount prefix (e.g. /hls) on the manifest URL
// is preserved. sourcePath is "" for the top manifest (served at
// /external/<id>/stream.m3u8, i.e. one level above the proxy mount) and the
// proxied path otherwise (served at /external/<id>/proxy/<sourcePath>).
func externalProxyChildURI(sourcePath, rel string) string {
	baseDir := ""
	if sourcePath != "" {
		baseDir = "proxy/" + path.Dir(strings.TrimPrefix(sourcePath, "/"))
	}
	return liveproxy.RelativeReference(baseDir, "proxy/"+rel)
}
