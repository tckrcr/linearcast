package playback

import "net/http"

// Handler returns the channel-scoped playback API. Process-level service and
// admin routes are composed by cmd/linearcast.
func (runtime *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels/{channelID}/stream.m3u8", runtime.handleManifest)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/stream.m3u8", runtime.handleRenditionManifest)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/init/{packageID}/init.mp4", runtime.handlePackagedInit)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/segments/{packageID}/{name}", runtime.handlePackagedSegment)
	mux.HandleFunc("GET /channels/{channelID}/"+encodingPath+"/{encodingID}/init.mp4", runtime.handleEncodingInit)
	mux.HandleFunc("GET /channels/{channelID}/"+encodingPath+"/{encodingID}/{name}", runtime.handleEncodingSegment)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/subs/{language}/playlist.m3u8", runtime.handleSubtitlePlaylist)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/subs/{packageID}/{name}", runtime.handleSubtitleVTT)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/subs/empty.vtt", runtime.handleEmptySubtitle)
	mux.HandleFunc("GET /channels/{channelID}/"+streamPath+"/{profile}/"+onDemandSubtitlePath+"/{rest...}", runtime.handleOnDemandSubtitleFile)
	mux.HandleFunc("GET /channels/{channelID}/subtitles", runtime.handleBurnSubtitleList)
	mux.HandleFunc("POST /channels/{channelID}/subtitles", runtime.handleBurnSubtitleSet)
	mux.HandleFunc("POST /channels/{channelID}/ondemand/restart", runtime.handleOnDemandRestart)
	mux.HandleFunc("GET /channels/{channelID}/now", runtime.handleNow)
	mux.HandleFunc("GET /channels/{channelID}/direct-play", runtime.handleDirectPlay)
	return mux
}
