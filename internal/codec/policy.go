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
	// VideoBitrateBps is the source video bitrate in bits per second, used to
	// estimate packaged size (exact for copy profiles, since video is remuxed
	// unchanged). 0 = unknown. Falls back to the whole-container bitrate when the
	// source reports no per-stream value (common in Matroska).
	VideoBitrateBps int64
	// ColorTransfer and ColorPrimaries are ffprobe's color_transfer /
	// color_primaries stream fields (e.g. "smpte2084", "bt2020"). Recorded for
	// HDR detection; not part of the v1 allowlist check.
	ColorTransfer  string
	ColorPrimaries string
	// CodecTagString is ffprobe's codec_tag_string for the selected video
	// stream (e.g. "hvc1", "dvhe"). Recorded for Dolby Vision Profile 5
	// detection (see IsDolbyVisionProfile5). Captured and persisted, but not
	// yet consulted by Admit — see the Phase 2 note in Admit.
	CodecTagString string
	AudioCodec     string
}

var (
	allowedContainers = map[string]bool{"mkv": true, "mp4": true}
	// HEVC is admitted (SDR and HDR): the H.264 transcode rungs decode any
	// source and the hevc-copy-source rung remuxes HEVC directly. DV Profile 5
	// is the one HEVC variant rejected (no HDR10 base) — see Admit. AV1 stays
	// excluded: there is no AV1 copy rung and no AV1 source in the library yet.
	allowedVideo = map[string]bool{"h264": true, "hevc": true, "vc1": true, "mpeg2video": true, "mpeg4": true, "vp9": true}
	allowedAudio = map[string]bool{"aac": true, "ac3": true, "eac3": true, "dts": true, "dts-hd-ma": true, "dts-hd-hra": true, "truehd": true, "flac": true, "opus": true, "mp3": true, "pcm_s16le": true, "pcm_s24le": true}
)

// Decision is the result of the media admission policy. OK reports whether the
// probe satisfies the allowlist; Reason carries a structured "key=value; ..."
// explanation when it does not (empty when OK).
type Decision struct {
	OK     bool
	Reason string
}

// Admit is the single media admission gate. It returns an OK Decision when p
// satisfies the v1 allowlist; otherwise OK is false and Reason is a structured
// reason string. Ingest, the scheduler re-check, and the admin probe flow all
// route through this one function so the policy is defined and verified in
// exactly one place.
//
// Video resolution is not capped here: the H.264 transcode rungs downscale
// taller sources and the copy rungs pass them through, so 4K (SDR or HDR) is
// admitted. The one rejected HEVC variant is Dolby Vision Profile 5 (codec tag
// dvhe/dvh1): it has no usable HDR10 base layer and cannot be copied or
// transcoded into a watchable stream. Rejecting it here mirrors the packager's
// terminal packager.ErrUnsupportedDolbyVision, so the failure surfaces at scan
// time with a clear reason instead of late in packaging.
func Admit(p Probe) Decision {
	var fails []string
	c := strings.ToLower(p.Container)
	if !allowedContainers[c] {
		fails = append(fails, fmt.Sprintf("container=%s", p.Container))
	}
	v := strings.ToLower(p.VideoCodec)
	switch {
	case IsDolbyVisionProfile5(p.CodecTagString):
		fails = append(fails, "dolby_vision_p5="+strings.ToLower(strings.TrimSpace(p.CodecTagString)))
	case !allowedVideo[v]:
		fails = append(fails, fmt.Sprintf("video_codec=%s", p.VideoCodec))
	}
	a := NormalizeAudio(p.AudioCodec)
	if !allowedAudio[a] {
		fails = append(fails, fmt.Sprintf("audio_codec=%s", p.AudioCodec))
	}
	if len(fails) > 0 {
		return Decision{Reason: strings.Join(fails, "; ")}
	}
	return Decision{OK: true}
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

// IsDolbyVisionProfile5 reports whether codecTag (ffprobe codec_tag_string) is a
// DV-only HEVC bitstream tag. Dolby Vision Profile 5 carries no HDR10 base
// layer — its picture is cross-channel ICtCp that depends on the per-frame RPU,
// which a standard HEVC re-encode (or any non-DV decode) drops, yielding
// pink/green output. DV Profile 7/8 streams carry a real HDR10 base and are
// tagged hvc1/hev1, so gating on the dvhe/dvh1 tag isolates the unsupported
// profile while letting HDR10-compatible DV pass through.
func IsDolbyVisionProfile5(codecTag string) bool {
	switch strings.ToLower(strings.TrimSpace(codecTag)) {
	case "dvhe", "dvh1":
		return true
	}
	return false
}
