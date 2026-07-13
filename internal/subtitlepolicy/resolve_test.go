package subtitlepolicy

import (
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

func TestResolveForcedBurnFromGameOfThronesSeasonOneProbe(t *testing.T) {
	transcodeProfile := packageprofile.Profile{
		Name: "test-transcode",
		Video: packageprofile.VideoSettings{
			Mode: packageprofile.VideoModeTranscode,
		},
	}
	copyProfile := packageprofile.Profile{
		Name: "test-copy",
		Video: packageprofile.VideoSettings{
			Mode: packageprofile.VideoModeCopy,
		},
	}

	req := Request{Mode: ModeForcedBurn, Language: "eng"}
	tests := []struct {
		name    string
		profile packageprofile.Profile
		tracks  []db.PackageTrack
		want    Decision
	}{
		{
			name:    "S01E01 has only English SDH so forced burn resolves clean",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				gotPGS(4, "eng", false),
			},
			want: None(ReasonNoForcedTrack),
		},
		{
			name:    "S01E02 has English forced stream 4 so forced burn resolves burn stream 4",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				gotPGS(4, "eng", true),
				gotPGS(5, "eng", false),
			},
			want: Decision{Action: ActionBurn, StreamIndex: 4, Language: "eng", Source: db.TrackSourceEmbeddedBitmap, Reason: ReasonForcedDisposition},
		},
		{
			name:    "S01E04 has only English SDH so forced burn resolves clean",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				gotPGS(4, "eng", false),
			},
			want: None(ReasonNoForcedTrack),
		},
		{
			name:    "S01E05 has only English SDH so forced burn resolves clean",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				gotPGS(3, "eng", false),
			},
			want: None(ReasonNoForcedTrack),
		},
		{
			name:    "copy profile on S01E02 cannot burn so resolves clean",
			profile: copyProfile,
			tracks: []db.PackageTrack{
				gotPGS(4, "eng", true),
				gotPGS(5, "eng", false),
			},
			want: None(ReasonProfileCannotBurn),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(req, tt.profile, tt.tracks)
			if got != tt.want {
				t.Fatalf("Resolve() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func gotPGS(streamIndex int, language string, forced bool) db.PackageTrack {
	return db.PackageTrack{
		Kind:        "subtitle",
		StreamIndex: streamIndex,
		Language:    language,
		Codec:       "hdmv_pgs_subtitle",
		Source:      db.TrackSourceEmbeddedBitmap,
		Forced:      forced,
	}
}

func TestResolveForcedBurnFromRingsOfPowerSeasonOneProbe(t *testing.T) {
	transcodeProfile := packageprofile.Profile{
		Name: "test-transcode",
		Video: packageprofile.VideoSettings{
			Mode: packageprofile.VideoModeTranscode,
		},
	}
	copyProfile := packageprofile.Profile{
		Name: "test-copy",
		Video: packageprofile.VideoSettings{
			Mode: packageprofile.VideoModeCopy,
		},
	}

	req := Request{Mode: ModeForcedBurn, Language: "eng"}
	tests := []struct {
		name    string
		profile packageprofile.Profile
		tracks  []db.PackageTrack
		want    Decision
	}{
		{
			name:    "S01E01 forced SRT stream 2 burns as text",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				ropSRT(2, "eng", true),
				ropSRT(3, "eng", false),
				ropSRT(4, "eng", false),
			},
			want: Decision{Action: ActionBurn, StreamIndex: 2, Language: "eng", Source: db.TrackSourceEmbedded, Reason: ReasonForcedDisposition},
		},
		{
			name:    "S01E02 has no forced track so forced burn resolves clean",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				ropSRT(2, "eng", false),
				ropSRT(3, "eng", false),
			},
			want: None(ReasonNoForcedTrack),
		},
		{
			name:    "forced bitmap outranks forced text even when text comes first",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				ropSRT(2, "eng", true),
				gotPGS(5, "eng", true),
			},
			want: Decision{Action: ActionBurn, StreamIndex: 5, Language: "eng", Source: db.TrackSourceEmbeddedBitmap, Reason: ReasonForcedDisposition},
		},
		{
			name:    "forced track in another language resolves clean",
			profile: transcodeProfile,
			tracks: []db.PackageTrack{
				ropSRT(2, "fre", true),
			},
			want: None(ReasonNoForcedTrack),
		},
		{
			name:    "copy profile cannot burn forced text",
			profile: copyProfile,
			tracks: []db.PackageTrack{
				ropSRT(2, "eng", true),
			},
			want: None(ReasonProfileCannotBurn),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(req, tt.profile, tt.tracks)
			if got != tt.want {
				t.Fatalf("Resolve() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func ropSRT(streamIndex int, language string, forced bool) db.PackageTrack {
	return db.PackageTrack{
		Kind:        "subtitle",
		StreamIndex: streamIndex,
		Language:    language,
		Codec:       "subrip",
		Source:      db.TrackSourceEmbedded,
		Forced:      forced,
	}
}
