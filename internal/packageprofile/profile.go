// Package packageprofile defines the seed package profile.
//
// A package profile is both a durable package identity and an encoder preset.
// SQLite stores the profile key as media_packages.rendition_profile. Runtime
// packagers load active profiles from the database; this package only carries
// the conservative default profile that is seeded into new databases.
package packageprofile

import (
	"fmt"
	"sort"
	"strings"
)

const (
	DefaultName           = "h264-main-1080p"
	H264CopySourceName    = "h264-copy-source"
	HEVCCopySourceName    = "hevc-copy-source"
	H264Main720pName      = "h264-main-720p"
	H264Main480pName      = "h264-main-480p"
	H264NVENC1080pName    = "h264-nvenc-main-1080p"
	H264NVENCCopySrcName  = "h264-nvenc-copy-source"
	H264NVENC720pName     = "h264-nvenc-main-720p"
	H264NVENC480pName     = "h264-nvenc-main-480p"
	MusicName             = "music-aac-720p"
)

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
	Name        string        `json:"name"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Tags        []string      `json:"tags,omitempty"`
	MediaKind   MediaKind     `json:"mediaKind"`
	Video       VideoSettings `json:"video"`
	Audio       AudioSettings `json:"audio"`
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
	ScaleHeight     int64  `json:"scaleHeight,omitempty"`
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
		Label:       "1080p good",
		Description: "Transcode video to capped H.264 main profile and audio to AAC stereo.",
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
			Bitrate:  "192k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H264CopySourceName,
		Label:       "Source copy",
		Description: "Remux compatible H.264 video and transcode audio to AAC stereo.",
		Tags:        []string{"abr"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:          VideoModeCopy,
			CodecRequired: "h264",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "192k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:  HEVCCopySourceName,
		Label: "HEVC source copy",
		Description: "Remux compatible HEVC (HDR10) video and transcode audio to AAC stereo. " +
			"Preserves HDR metadata; requires a HEVC source with an HDR10 base layer.",
		Tags:      []string{"abr", "hdr"},
		MediaKind: MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeCopy,
			CodecRequired:   "hevc",
			VideoMaxBitrate: "30000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "192k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H264Main720pName,
		Label:       "720p data saver",
		Description: "Transcode video to capped H.264 main profile at 720p and audio to AAC stereo.",
		Tags:        []string{"abr"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "libx264",
			Profile:         "main",
			Level:           "4.1",
			Preset:          "veryfast",
			CRF:             23,
			ScaleHeight:     720,
			VideoMaxBitrate: "4000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "160k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H264Main480pName,
		Label:       "480p data saver",
		Description: "Transcode video to capped H.264 main profile at 480p and audio to AAC stereo.",
		Tags:        []string{"abr"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "libx264",
			Profile:         "main",
			Level:           "3.1",
			Preset:          "veryfast",
			CRF:             23,
			ScaleHeight:     480,
			VideoMaxBitrate: "2000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "128k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        MusicName,
		Label:       "Music AAC",
		Description: "Package audio-only sources as AAC HLS with a static dark video track.",
		MediaKind:   MediaKindMusic,
		Video: VideoSettings{
			Mode: VideoModeTranscode,
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "192k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H264NVENC1080pName,
		Label:       "1080p NVENC",
		Description: "Transcode video to capped H.264 main profile via NVENC and audio to AAC stereo.",
		Tags:        []string{"default"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "h264_nvenc",
			Profile:         "main",
			Level:           "4.1",
			Preset:          "p4",
			ScaleHeight:     1080,
			VideoBitrate:    "8000k",
			VideoMaxBitrate: "8000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "192k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H264NVENCCopySrcName,
		Label:       "Source copy (NVENC ladder)",
		Description: "Remux compatible H.264 video and transcode audio to AAC stereo. Paired with NVENC transcode rungs.",
		Tags:        []string{"abr"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:          VideoModeCopy,
			CodecRequired: "h264",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "192k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H264NVENC720pName,
		Label:       "720p data saver NVENC",
		Description: "Transcode video to capped H.264 main profile at 720p via NVENC and audio to AAC stereo.",
		Tags:        []string{"abr"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "h264_nvenc",
			Profile:         "main",
			Level:           "4.1",
			Preset:          "p4",
			ScaleHeight:     720,
			VideoBitrate:    "4000k",
			VideoMaxBitrate: "4000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "160k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
	{
		Name:        H264NVENC480pName,
		Label:       "480p data saver NVENC",
		Description: "Transcode video to capped H.264 main profile at 480p via NVENC and audio to AAC stereo.",
		Tags:        []string{"abr"},
		MediaKind:   MediaKindVideo,
		Video: VideoSettings{
			Mode:            VideoModeTranscode,
			Codec:           "h264_nvenc",
			Profile:         "main",
			Level:           "3.1",
			Preset:          "p4",
			ScaleHeight:     480,
			VideoBitrate:    "2000k",
			VideoMaxBitrate: "2000k",
		},
		Audio: AudioSettings{
			Mode:     AudioModeTranscode,
			Codec:    "aac",
			Bitrate:  "128k",
			Channels: 2,
			SampleHz: 48000,
		},
	},
}

func BuiltIns() []Profile {
	out := make([]Profile, len(builtIns))
	copy(out, builtIns)
	return out
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
			return p, true
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
