// Package packageprofile defines the seed package profile.
//
// A package profile is both an encoder preset and one half of package identity:
// SQLite stores the profile key as media_packages.rendition_profile, paired with
// media_id. Runtime packagers load active profiles from the database; this
// package only carries the conservative default profile that is seeded into new
// databases.
package packageprofile

import (
	"fmt"
	"sort"
	"strings"
)

const (
	DefaultName            = "h264-1080p-8mbps"
	H2641080p20MbpsName    = "h264-1080p-20mbps"
	HEVC1080p16MbpsHDRName = "hevc-1080p-16mbps-hdr"
	HEVC2160p40MbpsHDRName = "hevc-2160p-40mbps-hdr"
	HEVCCopySourceName     = "hevc-copy-source"
	MusicName              = "music-aac-720p"
)

// BrowserHLSCopyVideoBitrateCeilingBps is the source-video bitrate ceiling for
// copy/remux profiles served through browser HLS. Transcode profiles enforce
// their own VideoMaxBitrate caps, but copy profiles inherit source bitrate and
// can overwhelm browser MSE buffers.
const BrowserHLSCopyVideoBitrateCeilingBps int64 = 40_000_000

type MediaKind string

const (
	MediaKindVideo MediaKind = "video"
	MediaKindMusic MediaKind = "music"
)

type VideoMode string

const (
	VideoModeTranscode VideoMode = "transcode"
	VideoModeCopy      VideoMode = "copy"
)

type AudioMode string

const (
	AudioModeTranscode AudioMode = "transcode"
	AudioModeCopy      AudioMode = "copy"
)

type Profile struct {
	Name        string           `json:"name"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	Tags        []string         `json:"tags,omitempty"`
	MediaKind   MediaKind        `json:"mediaKind"`
	Video       VideoSettings    `json:"video"`
	Audio       AudioSettings    `json:"audio"`
	Subtitles   SubtitleSettings `json:"subtitles,omitempty"`
}

// SubtitleSettings describes package-time subtitle burn-in policy. The first
// supported mode is forced_burn: burn a matching forced embedded subtitle
// track (bitmap or text) when present; otherwise encode clean output.
type SubtitleSettings struct {
	Mode     string `json:"mode,omitempty"`
	Language string `json:"language,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

type VideoSettings struct {
	Mode          VideoMode `json:"mode"`
	Codec         string    `json:"codec,omitempty"`
	CodecRequired string    `json:"codecRequired,omitempty"`
	Profile       string    `json:"profile,omitempty"`
	Level         string    `json:"level,omitempty"`
	Preset        string    `json:"preset,omitempty"`
	CRF           int       `json:"crf,omitempty"`
	// ScaleHeight, when > 0, adds a -vf scale=-2:min(ih\,ScaleHeight) filter so
	// sources taller than ScaleHeight are downscaled while shorter sources are
	// left unchanged.
	ScaleHeight int64 `json:"scaleHeight,omitempty"`
	// PixelFormat overrides the encoder output pixel format (ffmpeg -pix_fmt).
	// Empty defaults to yuv420p (8-bit SDR). HDR-preserving profiles set a
	// 10-bit format such as yuv420p10le (libx265) so the PQ
	// signal survives the transcode.
	PixelFormat     string `json:"pixelFormat,omitempty"`
	VideoBitrate    string `json:"videoBitrate,omitempty"`
	VideoMaxBitrate string `json:"videoMaxBitrate,omitempty"`
	// VideoQuality is the encoder's quality knob on a 0–100 scale (higher = better).
	// Used by VideoToolbox encoders which don't expose CRF. Mapped to ffmpeg -q:v.
	VideoQuality int `json:"videoQuality,omitempty"`
}

type AudioSettings struct {
	Mode     AudioMode `json:"mode"`
	Codec    string    `json:"codec,omitempty"`
	Bitrate  string    `json:"bitrate,omitempty"`
	Channels int       `json:"channels,omitempty"`
	SampleHz int       `json:"sampleHz,omitempty"`
}

var builtIns = []Profile{
	{
		Name:        DefaultName,
		Label:       "broad compatibility - 1080p h.264",
		Description: "Transcode H.264 Main 1080p with CRF 23, 8 Mbps VBV maxrate, and AAC stereo.",
		Tags:        []string{"default"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "libx264",
			Profile:         "main",
			Level:           "4.1",
			Preset:          "veryfast",
			CRF:             23,
			ScaleHeight:     1080,
			VideoMaxBitrate: "8000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "256k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H2641080p20MbpsName,
		Label:       "archive quality - 1080p h.264",
		Description: "Transcode video to high-bitrate H.264 main profile at 1080p and audio to AAC stereo. Intended for pre-packaged/eager channels.",
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "libx264",
			Profile:         "main",
			Level:           "4.1",
			Preset:          "veryfast",
			CRF:             20,
			ScaleHeight:     1080,
			VideoMaxBitrate: "20000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "256k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:  HEVC1080p16MbpsHDRName,
		Label: "hdr capable - 1080p hevc",
		Description: "Transcode video to 10-bit HEVC Main 10 at 1080p with a 16 Mbps cap and audio to AAC stereo. " +
			"Preserves HDR metadata (PQ/HLG, BT.2020) instead of tone-mapping to SDR. " +
			"Intended for startup-optimized HDR playback where CPU is cheap and cold-start latency matters more than source copy.",
		Tags:      []string{"hdr"},
		MediaKind: MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "libx265",
			Profile:         "main10",
			Preset:          "veryfast",
			CRF:             20,
			ScaleHeight:     1080,
			PixelFormat:     "yuv420p10le",
			VideoMaxBitrate: "16000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "256k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:  HEVC2160p40MbpsHDRName,
		Label: "hdr archive - 2160p hevc",
		Description: "Transcode video to 10-bit HEVC Main 10 at up to 2160p with a 40 Mbps cap and audio to AAC stereo. " +
			"Preserves HDR metadata (PQ/HLG, BT.2020) instead of tone-mapping to SDR. " +
			"Intended for high-quality HDR playback when the source-preserving copy profile is not the right startup trade.",
		Tags:      []string{"hdr"},
		MediaKind: MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "libx265",
			Profile:         "main10",
			Preset:          "veryfast",
			CRF:             20,
			ScaleHeight:     2160,
			PixelFormat:     "yuv420p10le",
			VideoMaxBitrate: "40000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "256k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:  HEVCCopySourceName,
		Label: "source quality via copy video - preserves hdr",
		Description: "Remux compatible HEVC (HDR10) video and transcode audio to AAC stereo. " +
			"Preserves HDR metadata; requires a HEVC source with an HDR10 base layer.",
		Tags:      []string{"abr", "hdr"},
		MediaKind: MediaKindVideo,
		Video: VideoSettings{
			Mode:          VideoModeCopy,
			CodecRequired: "hevc",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "256k",
			Channels: 2,
			SampleHz: 48000,
		},
	},

	{
		Name:        MusicName,
		Label:       "audio only - aac music",
		Description: "Package audio-only sources as AAC HLS with a static dark video track.",
		MediaKind:   MediaKindMusic,
		Video: VideoSettings{
			Mode: VideoModeTranscode,
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "256k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
}

func BuiltIns() []Profile {
	out := make([]Profile, len(builtIns))
	copy(out, builtIns)
	for i := range out {
		out[i] = withDefaultSubtitleSettings(out[i])
	}
	return out
}

func withDefaultSubtitleSettings(p Profile) Profile {
	if p.MediaKind == MediaKindVideo && p.Video.Mode == VideoModeTranscode && p.Subtitles.Mode == "" {
		p.Subtitles = SubtitleSettings{Mode: "forced_burn", Language: "eng", Fallback: "none"}
	}
	return p
}

func Names() []string {
	profiles := BuiltIns()
	out := make([]string, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func Lookup(name string) (Profile, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultName
	}
	for _, p := range builtIns {
		if p.Name == name {
			return withDefaultSubtitleSettings(p), true
		}
	}
	return Profile{}, false
}

func MustLookup(name string) (Profile, error) {
	p, ok := Lookup(name)
	if !ok {
		return Profile{}, fmt.Errorf("unknown package profile %q", strings.TrimSpace(name))
	}
	return p, nil
}

func Known(name string) bool {
	_, ok := Lookup(name)
	return ok
}

func NormalizeMediaKind(kind MediaKind) MediaKind {
	switch kind {
	case MediaKindMusic:
		return MediaKindMusic
	default:
		return MediaKindVideo
	}
}
