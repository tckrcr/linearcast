package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{channelID}/stream.m3u8", a.handleManifest)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/stream.m3u8", a.handleRenditionManifest)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/init/{packageID}/init.mp4", a.handlePackagedInit)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/segments/{packageID}/{name}", a.handlePackagedSegment)
	mux.HandleFunc("GET /channels/{channelID}/"+encodingPath+"/{encodingID}/init.mp4", a.handleEncodingInit)
	mux.HandleFunc("GET /channels/{channelID}/"+encodingPath+"/{encodingID}/{name}", a.handleEncodingSegment)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/subs/{language}/playlist.m3u8", a.handleSubtitlePlaylist)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/subs/{packageID}/{name}", a.handleSubtitleVTT)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/subs/empty.vtt", a.handleEmptySubtitle)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/"+onDemandSubtitlePath+"/{rest...}", a.handleOnDemandSubtitleFile)
	mux.HandleFunc("GET /channels/{channelID}/subtitles", a.handleBurnSubtitleList)
	mux.HandleFunc("POST /channels/{channelID}/subtitles", a.handleBurnSubtitleSet)
	mux.HandleFunc("POST /channels/{channelID}/ondemand/restart", a.handleOnDemandRestart)
	mux.HandleFunc("GET /channels/{channelID}/now", a.handleNow)
	mux.HandleFunc("GET /channels/{channelID}/direct-play", a.handleDirectPlay)
	mux.HandleFunc("GET /external/{channelID}/stream.m3u8", a.handleExternalHLSManifest)
	mux.HandleFunc("GET /external/{channelID}/proxy/{path...}", a.handleExternalHLSProxy)
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /readyz", a.handleReady)
	mux.HandleFunc("GET /status", a.handleStatus)
	mux.Handle("GET /metrics", promhttp.Handler())
	return requestLogMiddleware(mux)
}

// requestLogMiddleware logs every HTTP request with method, path, status, and
// duration as structured JSON fields for Loki.
func requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		crw := &captureResponse{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(crw, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", crw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type captureResponse struct {
	http.ResponseWriter
	status int
}

func (c *captureResponse) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}
