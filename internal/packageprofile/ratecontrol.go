package packageprofile

import (
	"fmt"
	"strings"
)

// RateControl is the encoder rate-control mode a profile's video settings
// resolve to. It is the single classification that size estimation and
// bitrate-budgeting code (manifest BANDWIDTH, encoding-tab estimates) branch on,
// so the meaning of a profile's bitrate number — a target the encoder spends, or
// a ceiling it must not exceed — is defined in exactly one place.
//
// The distinction is load-bearing for size: a target/CBR profile spends ~its
// stated bitrate, so output size is bitrate×duration; a capped-CRF profile
// targets a quality (CRF) and treats the bitrate only as a VBV ceiling, so its
// stated number is a worst case, not an expected size. See the roadmap
// "Profile rate-control contract".
type RateControl string

const (
	// RateControlCopy remuxes the source video unchanged (-c:v copy); the output
	// video bitrate equals the source bitrate.
	RateControlCopy RateControl = "copy"
	// RateControlCRF is constant-quality (-crf, or VideoToolbox -q:v) with no
	// bitrate ceiling; output bitrate is content-dependent and unbounded by the
	// profile.
	RateControlCRF RateControl = "crf"
	// RateControlCappedCRF is constant-quality with a VBV ceiling
	// (-crf + -maxrate/-bufsize). The bitrate is a worst-case cap, not a target.
	RateControlCappedCRF RateControl = "capped-crf"
	// RateControlTarget is average/target bitrate (-b:v, ABR); output size is
	// ~bitrate×duration regardless of content.
	RateControlTarget RateControl = "target"
	// RateControlCBR is constant bitrate (-b:v equal to -maxrate); output size is
	// ~bitrate×duration with little variance.
	RateControlCBR RateControl = "cbr"
	// RateControlUnknown is a transcode profile with no recognizable rate-control
	// knob (no CRF, quality, or bitrate); the encoder applies its own default.
	RateControlUnknown RateControl = "unknown"
)

// RateControl classifies v into the encoder rate-control mode it resolves to.
// The branches mirror the ffmpeg argument construction in the packager: copy
// mode remuxes; -b:v makes it target/ABR (CBR when -b:v equals -maxrate); a
// quality knob (CRF or VideoQuality) without -b:v is CRF, capped when -maxrate
// is present; a bare -maxrate still drives a capped constant-quality encode.
func (v VideoSettings) RateControl() RateControl {
	if v.Mode == VideoModeCopy {
		return RateControlCopy
	}
	hasMax := strings.TrimSpace(v.VideoMaxBitrate) != ""
	if strings.TrimSpace(v.VideoBitrate) != "" {
		if hasMax && ParseBitrate(v.VideoBitrate) == ParseBitrate(v.VideoMaxBitrate) {
			return RateControlCBR
		}
		return RateControlTarget
	}
	if v.CRF > 0 || v.VideoQuality > 0 || hasMax {
		if hasMax {
			return RateControlCappedCRF
		}
		return RateControlCRF
	}
	return RateControlUnknown
}

// ParseBitrate converts an ffmpeg bitrate string like "6000k", "8M", or a bare
// "8000000" to bits per second. It returns 0 for an empty or unparseable value.
func ParseBitrate(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1_000
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1_000_000
		s = s[:len(s)-1]
	case 'g', 'G':
		mult = 1_000_000_000
		s = s[:len(s)-1]
	}
	var val int64
	if _, err := fmt.Sscan(strings.TrimSpace(s), &val); err != nil {
		return 0
	}
	return val * mult
}
