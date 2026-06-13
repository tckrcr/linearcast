package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (a *app) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /channel/{channelID}/stream.m3u8", a.handleManifest)
	mux.HandleFunc("GET /channel/{channelID}/"+packagedPath+"/stream.m3u8", a.handlePackagedManifest)
	mux.HandleFunc("GET /channel/{channelID}/"+packagedPath+"/{rendition}/stream.m3u8", a.handleRenditionManifest)
	mux.HandleFunc("GET /channel/{channelID}/"+packagedPath+"/init/{packageID}/init.mp4", a.handlePackagedInit)
	mux.HandleFunc("GET /channel/{channelID}/"+packagedPath+"/segments/{packageID}/{name}", a.handlePackagedSegment)
	mux.HandleFunc("GET /channel/{channelID}/"+sessionPath+"/{sessionID}/init.mp4", a.handleSessionInit)
	mux.HandleFunc("GET /channel/{channelID}/"+sessionPath+"/{sessionID}/{name}", a.handleSessionSegment)
	mux.HandleFunc("GET /channel/{channelID}/"+packagedPath+"/subs/{language}/playlist.m3u8", a.handleSubtitlePlaylist)
	mux.HandleFunc("GET /channel/{channelID}/"+packagedPath+"/subs/{packageID}/{name}", a.handleSubtitleVTT)
	mux.HandleFunc("GET /channel/{channelID}/"+packagedPath+"/subs/empty.vtt", a.handleEmptySubtitle)
	mux.HandleFunc("GET /channel/{channelID}/subtitles", a.handleBurnSubtitleList)
	mux.HandleFunc("POST /channel/{channelID}/subtitles", a.handleBurnSubtitleSet)
	mux.HandleFunc("GET /channel/{channelID}/now", a.handleNow)
	mux.HandleFunc("GET /external/{channelID}/stream.m3u8", a.handleExternalHLSManifest)
	mux.HandleFunc("GET /external/{channelID}/{name}", a.handleExternalHLSSegment)
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /readyz", a.handleReady)
	mux.HandleFunc("GET /status", a.handleStatus)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /channel/{channelID}/plexrelay.m3u8", a.handlePlexRelayEntry)
	mux.HandleFunc("GET /channel/{channelID}/plexrelay/{viewerToken}/{path...}", a.handlePlexRelayProxy)
	mux.HandleFunc("POST /channel/{channelID}/keepalive", a.handleKeepalive)
	return mux
}
