package packager

import (
	"testing"

	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestClassifyProbeStreams(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		kind      packageprofile.MediaKind
		wantCodec string
		wantOK    bool
	}{
		{
			name:   "video stream with packets",
			json:   `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"150"},{"codec_type":"audio","codec_name":"aac","nb_read_packets":"90"}]}`,
			kind:   packageprofile.MediaKindVideo,
			wantOK: true,
		},
		{
			name:   "video stream listed but no packets (empty/corrupt segment)",
			json:   `{"streams":[{"codec_type":"video","codec_name":"h264"}]}`,
			kind:   packageprofile.MediaKindVideo,
			wantOK: false,
		},
		{
			name:   "video profile but only audio",
			json:   `{"streams":[{"codec_type":"audio","codec_name":"aac","nb_read_packets":"90"}]}`,
			kind:   packageprofile.MediaKindVideo,
			wantOK: false,
		},
		{
			name:   "no streams at all",
			json:   `{"streams":[]}`,
			kind:   packageprofile.MediaKindVideo,
			wantOK: false,
		},
		{
			name:   "video stream with empty codec",
			json:   `{"streams":[{"codec_type":"video","codec_name":"","nb_read_packets":"150"}]}`,
			kind:   packageprofile.MediaKindVideo,
			wantOK: false,
		},
		{
			name:      "copy profile codec matches",
			json:      `{"streams":[{"codec_type":"video","codec_name":"hevc","nb_read_packets":"150"}]}`,
			kind:      packageprofile.MediaKindVideo,
			wantCodec: "hevc",
			wantOK:    true,
		},
		{
			name:      "copy profile codec mismatch",
			json:      `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"150"}]}`,
			kind:      packageprofile.MediaKindVideo,
			wantCodec: "hevc",
			wantOK:    false,
		},
		{
			name:   "music profile with audio packets",
			json:   `{"streams":[{"codec_type":"audio","codec_name":"aac","nb_read_packets":"90"}]}`,
			kind:   packageprofile.MediaKindMusic,
			wantOK: true,
		},
		{
			name:   "music profile audio listed but no packets",
			json:   `{"streams":[{"codec_type":"audio","codec_name":"aac"}]}`,
			kind:   packageprofile.MediaKindMusic,
			wantOK: false,
		},
		{
			name:   "music profile without audio",
			json:   `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"150"}]}`,
			kind:   packageprofile.MediaKindMusic,
			wantOK: false,
		},
		{
			name:   "unparseable output",
			json:   `not json`,
			kind:   packageprofile.MediaKindVideo,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := classifyProbeStreams([]byte(tt.json), tt.kind, tt.wantCodec)
			if ok != tt.wantOK {
				t.Fatalf("classifyProbeStreams ok=%v (reason=%q), want %v", ok, reason, tt.wantOK)
			}
			if !ok && reason == "" {
				t.Fatalf("failure case returned empty reason")
			}
		})
	}
}
