package lcingest

import (
	"testing"
)

func TestDeriveMusicTitle(t *testing.T) {
	tests := []struct {
		name string
		path string
		mp   MusicProbeResult
		want string
	}{
		{
			name: "tagged track",
			path: "/music/Example Artist - First Light (2007)/01 - Morning Signal.mp3",
			mp: MusicProbeResult{
				DurationMs: 195_000,
				Tags:       musicTags{Title: "Morning Signal", Track: "1"},
			},
			want: "01. Morning Signal",
		},
		{
			name: "tagged track no number",
			path: "/music/Band/Album/03 Song.flac",
			mp: MusicProbeResult{
				DurationMs: 200_000,
				Tags:       musicTags{Title: "Song"},
			},
			want: "Song",
		},
		{
			name: "track/total format",
			path: "/music/Band/Album/04 Song.flac",
			mp: MusicProbeResult{
				DurationMs: 200_000,
				Tags:       musicTags{Title: "Song", Track: "4/12"},
			},
			want: "04. Song",
		},
		{
			name: "filename fallback with leading number",
			path: "/music/Band/Album/03 - My Track.flac",
			mp:   MusicProbeResult{DurationMs: 200_000},
			want: "My Track",
		},
		{
			name: "single file album via cue sidecar",
			path: "/music/Example Artist/2000 First Light/Example Artist - First Light.flac",
			mp: MusicProbeResult{
				DurationMs:    38 * 60 * 1000,
				HasCueSidecar: true,
				Tags:          musicTags{Album: "First Light", Artist: "Example Artist"},
			},
			want: "[Full Album] First Light",
		},
		{
			name: "single file album via duration",
			path: "/music/Example Artist/2008 Signals Across The Silent City/Example Artist - Signals Across The Silent City.flac",
			mp: MusicProbeResult{
				DurationMs: 46 * 60 * 1000,
				Tags:       musicTags{Album: "Signals Across The Silent City", Artist: "Example Artist"},
			},
			want: "[Full Album] Signals Across The Silent City",
		},
		{
			name: "single file album no album tag uses path",
			path: "/music/Example Artist/2000 First Light/Example Artist - First Light.flac",
			mp: MusicProbeResult{
				DurationMs:    38 * 60 * 1000,
				HasCueSidecar: true,
			},
			want: "[Full Album] First Light",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveMusicTitle(tc.path, tc.mp)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeriveMusicSchedulingGroup(t *testing.T) {
	tests := []struct {
		name string
		path string
		mp   MusicProbeResult
		want string
	}{
		{
			name: "tags present",
			path: "/music/Example Artist - First Light (2007)/01 - Morning Signal.mp3",
			mp: MusicProbeResult{
				DurationMs: 195_000,
				Tags:       musicTags{Artist: "Example Artist", Album: "First Light"},
			},
			want: "Example Artist — First Light",
		},
		{
			name: "album artist takes priority over track artist",
			path: "/music/Various/01 Song.flac",
			mp: MusicProbeResult{
				DurationMs: 200_000,
				Tags:       musicTags{Artist: "Some Guest", AlbumArtist: "Various Artists", Album: "Compilation"},
			},
			want: "Various Artists — Compilation",
		},
		{
			name: "full album with tags",
			path: "/music/Example Artist/2008 Signals Across The Silent City/Example Artist - Signals Across The Silent City.flac",
			mp: MusicProbeResult{
				DurationMs: 46 * 60 * 1000,
				Tags:       musicTags{Artist: "Example Artist", Album: "Signals Across The Silent City"},
			},
			want: "Example Artist — Signals Across The Silent City [Full Album]",
		},
		{
			name: "path fallback artist-album split",
			path: "/music/Example Artist - First Light (2007)/01 - Morning Signal.mp3",
			mp:   MusicProbeResult{DurationMs: 195_000},
			want: "Example Artist — First Light",
		},
		{
			name: "path fallback year-prefixed dir with artist grandparent",
			path: "/music/Example Artist/2008 Signals Across The Silent City/01 Opening Track.flac",
			mp:   MusicProbeResult{DurationMs: 200_000},
			want: "Example Artist — Signals Across The Silent City",
		},
		{
			name: "stereo remaster keeps suffix",
			path: "/music/The Example Band - Stereo and Mono Box Sets + Extras/The Example Band - Midnight Signals [2009 Stereo Remaster]/01 Midnight Signals.flac",
			mp:   MusicProbeResult{DurationMs: 150_000},
			want: "The Example Band — Midnight Signals [2009 Stereo Remaster]",
		},
		{
			name: "mono remaster separate group",
			path: "/music/The Example Band - Stereo and Mono Box Sets + Extras/The Example Band - Midnight Signals [2009 Mono Remaster]/01 Midnight Signals.flac",
			mp:   MusicProbeResult{DurationMs: 150_000},
			want: "The Example Band — Midnight Signals [2009 Mono Remaster]",
		},
		{
			name: "year-prefixed root dir no artist",
			path: "/music/1977 - The Night Birds - Open Highway [SACD DSF][2011]/01 Open Highway.dsf",
			mp:   MusicProbeResult{DurationMs: 380_000},
			want: "The Night Birds — Open Highway [SACD DSF]",
		},
		{
			name: "single file full album path fallback",
			path: "/music/Example Artist/2000 First Light/Example Artist - First Light.flac",
			mp: MusicProbeResult{
				DurationMs:    38 * 60 * 1000,
				HasCueSidecar: true,
			},
			want: "Example Artist — First Light [Full Album]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveMusicSchedulingGroup(tc.path, tc.mp)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
