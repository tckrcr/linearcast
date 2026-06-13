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
	VideoWidth  int64
	VideoHeight int64
	// ColorTransfer and ColorPrimaries are ffprobe's color_transfer /
	// color_primaries stream fields (e.g. "smpte2084", "bt2020"). Recorded for
	// HDR detection; not part of the v1 allowlist check.
	ColorTransfer  string
	ColorPrimaries string
	AudioCodec     string
}

var (
	allowedContainers = map[string]bool{"mkv": true, "mp4": true}
	// hevc/av1 deliberately excluded until the HDR probe lands — most HEVC/AV1
	// in the wild is HDR, and there's no transfer-characteristic gate yet.
	allowedVideo = map[string]bool{"h264": true, "vc1": true, "mpeg2video": true, "mpeg4": true, "vp9": true}
	allowedAudio = map[string]bool{"aac": true, "ac3": true, "eac3": true, "dts": true, "dts-hd-ma": true, "dts-hd-hra": true, "truehd": true, "flac": true, "opus": true, "mp3": true, "pcm_s16le": true, "pcm_s24le": true}
)

// Check returns ("", true) when p satisfies the allowlist. Otherwise it
// returns a structured reason string and false.
//
// HDR sources (PQ/HLG transfer characteristic) are allowed through even if
// the video codec is HEVC and the height exceeds 1080. This gate relies on
// the presence of an HDR10 base layer; DV P5 (no HDR10 fallback) is not
// separately detected here.
func Check(p Probe) (reason string, ok bool) {
	var fails []string
	c := strings.ToLower(p.Container)
	if !allowedContainers[c] {
		fails = append(fails, fmt.Sprintf("container=%s", p.Container))
	}
	v := strings.ToLower(p.VideoCodec)
	isHDR := IsHDRTransfer(p.ColorTransfer)
	if v == "hevc" && isHDR {
		// HEVC with HDR transfer characteristic: allow through for the
		// hevc-copy-source ABR rung.
	} else if !allowedVideo[v] {
		fails = append(fails, fmt.Sprintf("video_codec=%s", p.VideoCodec))
	}
	if !isHDR && p.VideoHeight > 1080 {
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

// IsHDRTransfer reports whether transfer (ffprobe color_transfer) is an HDR
// transfer characteristic: smpte2084 (PQ — HDR10, HDR10+, Dolby Vision) or
// arib-std-b67 (HLG). Transfer alone is the reliable HDR signal; bt2020
// primaries also appear on SDR-in-BT.2020 content, so they are deliberately
// not consulted here.
func IsHDRTransfer(transfer string) bool {
	switch strings.ToLower(strings.TrimSpace(transfer)) {
	case "smpte2084", "arib-std-b67":
		return true
	}
	return false
}
