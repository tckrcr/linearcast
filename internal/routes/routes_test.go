package routes

import "testing"

func TestRoutesEscapeIDs(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"hls manifest", HLSManifest("vod one/slash"), "/hls/channels/vod%20one%2Fslash/stream.m3u8"},
		{"external hls manifest", ExternalHLSManifest("spotify one"), "/hls/external/spotify%20one/stream.m3u8"},
		{"direct play", DirectPlay("vod one/slash"), "/channels/vod%20one%2Fslash/direct-play"},
		{"media artwork", MediaArtwork("m 1/slash"), "/api/art/media/m%201%2Fslash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("route=%q, want %q", tt.got, tt.want)
			}
		})
	}
}
