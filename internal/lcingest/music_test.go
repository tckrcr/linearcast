package lcingest

import (
	"testing"
)

func TestDeriveMusicTitle(t *testing.T) {
	tests := []struct {
		name string
		path string
		mp   musicProbeResult
		want string
	}{
		{
			name: "tagged track",
			path: "/music/Kanye West - Graduation (2007)/01 - Good Morning.mp3",
			mp: musicProbeResult{
				DurationMs: 195_000,
				Tags:       musicTags{Title: "Good Morning", Track: "1"},
			},
			want: "01. Good Morning",
		},
		{
			name: "tagged track no number",
			path: "/music/Band/Album/03 Song.flac",
			mp: musicProbeResult{
				DurationMs: 200_000,
				Tags:       musicTags{Title: "Song"},
			},
			want: "Song",
		},
		{
			name: "track/total format",
			path: "/music/Band/Album/04 Song.flac",
			mp: musicProbeResult{
				DurationMs: 200_000,
				Tags:       musicTags{Title: "Song", Track: "4/12"},
			},
			want: "04. Song",
		},
		{
			name: "filename fallback with leading number",
			path: "/music/Band/Album/03 - My Track.flac",
			mp:   musicProbeResult{DurationMs: 200_000},
			want: "My Track",
		},
		{
			name: "single file album via cue sidecar",
			path: "/music/Coldplay/2000 Parachutes/Coldplay - Parachutes.flac",
			mp: musicProbeResult{
				DurationMs:    38 * 60 * 1000,
				HasCueSidecar: true,
				Tags:          musicTags{Album: "Parachutes", Artist: "Coldplay"},
			},
			want: "[Full Album] Parachutes",
		},
		{
			name: "single file album via duration",
			path: "/music/Coldplay/2008 Viva La Vida/Coldplay - Viva La Vida.flac",
			mp: musicProbeResult{
				DurationMs: 46 * 60 * 1000,
				Tags:       musicTags{Album: "Viva La Vida Or Death And All His Friends", Artist: "Coldplay"},
			},
			want: "[Full Album] Viva La Vida Or Death And All His Friends",
		},
		{
			name: "single file album no album tag uses path",
			path: "/music/Coldplay/2000 Parachutes/Coldplay - Parachutes.flac",
			mp: musicProbeResult{
				DurationMs:    38 * 60 * 1000,
				HasCueSidecar: true,
			},
			want: "[Full Album] Parachutes",
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
		mp   musicProbeResult
		want string
	}{
		{
			name: "tags present",
			path: "/music/Kanye West - Graduation (2007)/01 - Good Morning.mp3",
			mp: musicProbeResult{
				DurationMs: 195_000,
				Tags:       musicTags{Artist: "Kanye West", Album: "Graduation"},
			},
			want: "Kanye West — Graduation",
		},
		{
			name: "album artist takes priority over track artist",
			path: "/music/Various/01 Song.flac",
			mp: musicProbeResult{
				DurationMs: 200_000,
				Tags:       musicTags{Artist: "Some Guest", AlbumArtist: "Various Artists", Album: "Compilation"},
			},
			want: "Various Artists — Compilation",
		},
		{
			name: "full album with tags",
			path: "/music/Coldplay/2008 Viva La Vida/Coldplay - Viva La Vida.flac",
			mp: musicProbeResult{
				DurationMs: 46 * 60 * 1000,
				Tags:       musicTags{Artist: "Coldplay", Album: "Viva La Vida Or Death And All His Friends"},
			},
			want: "Coldplay — Viva La Vida Or Death And All His Friends [Full Album]",
		},
		{
			name: "path fallback artist-album split",
			path: "/music/Kanye West - Graduation (2007)/01 - Good Morning.mp3",
			mp:   musicProbeResult{DurationMs: 195_000},
			want: "Kanye West — Graduation",
		},
		{
			name: "path fallback year-prefixed dir with artist grandparent",
			path: "/music/Coldplay/2008 Viva La Vida Or Death And All His Friends/01 Life In Technicolor.flac",
			mp:   musicProbeResult{DurationMs: 200_000},
			want: "Coldplay — Viva La Vida Or Death And All His Friends",
		},
		{
			name: "Beatles stereo remaster keeps suffix",
			path: "/music/The Beatles - Stereo and Mono Box Sets + Extras/The Beatles - A Hard Day's Night [2009 Stereo Remaster]/01 A Hard Day's Night.flac",
			mp:   musicProbeResult{DurationMs: 150_000},
			want: "The Beatles — A Hard Day's Night [2009 Stereo Remaster]",
		},
		{
			name: "Beatles mono remaster separate group",
			path: "/music/The Beatles - Stereo and Mono Box Sets + Extras/The Beatles - A Hard Day's Night [2009 Mono Remaster]/01 A Hard Day's Night.flac",
			mp:   musicProbeResult{DurationMs: 150_000},
			want: "The Beatles — A Hard Day's Night [2009 Mono Remaster]",
		},
		{
			name: "year-prefixed root dir no artist",
			path: "/music/1977 - The Eagles - Hotel California [SACD DSF][2011]/01 Hotel California.dsf",
			mp:   musicProbeResult{DurationMs: 380_000},
			want: "The Eagles — Hotel California [SACD DSF]",
		},
		{
			name: "single file full album path fallback",
			path: "/music/Coldplay/2000 Parachutes/Coldplay - Parachutes.flac",
			mp: musicProbeResult{
				DurationMs:    38 * 60 * 1000,
				HasCueSidecar: true,
			},
			want: "Coldplay — Parachutes [Full Album]",
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
