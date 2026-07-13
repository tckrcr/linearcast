package routes

import "net/url"

func HLSManifest(channelID string) string {
	return "/hls/channels/" + url.PathEscape(channelID) + "/stream.m3u8"
}

func ExternalHLSManifest(channelID string) string {
	return "/hls/external/" + url.PathEscape(channelID) + "/stream.m3u8"
}

func DirectPlay(channelID string) string {
	return "/channels/" + url.PathEscape(channelID) + "/direct-play"
}

func MediaArtwork(mediaID string) string {
	return "/api/art/media/" + url.PathEscape(mediaID)
}
