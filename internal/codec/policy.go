// Package codec encodes the v1 linearcast codec allowlist.
//
// See docs/database.md for the media codec metadata model.
package codec

import (
	"fmt"
	"strings"
)

// Probe describes the relevant codec metadata for one media file.
type Probe struct {
	Container   string
	VideoCodec  string
	VideoHeight int64
	AudioCodec  string
}

var (
	allowedContainers = map[string]bool{"mkv": true, "mp4": true}
	// hevc/av1 deliberately excluded until the HDR probe lands — most HEVC/AV1
	// in the wild is HDR, and there's no transfer-characteristic gate yet.
	allowedVideo = map[string]bool{"h264": true, "vc1": true, "mpeg2video": true, "mpeg4": true, "vp9": true}
	allowedAudio = map[string]bool{"aac": true, "ac3": true, "eac3": true, "dts": true, "dts-hd-ma": true, "dts-hd-hra": true, "truehd": true, "flac": true, "opus": true, "mp3": true, "pcm_s16le": true, "pcm_s24le": true}
)

// Check returns ("", true) when p satisfies the v1 allowlist. Otherwise it
// returns a structured reason string and false.
func Check(p Probe) (reason string, ok bool) {
	var fails []string
	c := strings.ToLower(p.Container)
	if !allowedContainers[c] {
		fails = append(fails, fmt.Sprintf("container=%s", p.Container))
	}
	v := strings.ToLower(p.VideoCodec)
	if !allowedVideo[v] {
		fails = append(fails, fmt.Sprintf("video_codec=%s", p.VideoCodec))
	}
	if p.VideoHeight > 1080 {
		fails = append(fails, fmt.Sprintf("video_height=%d", p.VideoHeight))
	}
	a := NormalizeAudio(p.AudioCodec)
	if !allowedAudio[a] {
		fails = append(fails, fmt.Sprintf("audio_codec=%s", p.AudioCodec))
	}
	if len(fails) > 0 {
		return strings.Join(fails, "; "), false
	}
	return "", true
}

// NormalizeAudio maps ffprobe's profile-aware audio names to the canonical
// allowlist tokens. e.g. "dts" + profile "DTS-HD MA" -> "dts-hd-ma".
func NormalizeAudio(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
