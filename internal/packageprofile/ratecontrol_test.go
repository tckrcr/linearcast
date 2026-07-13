package packageprofile

import "testing"

func TestVideoSettingsRateControl(t *testing.T) {
	cases := []struct {
		name string
		v    VideoSettings
		want RateControl
	}{
		{"copy", VideoSettings{Mode: VideoModeCopy, CodecRequired: "hevc"}, RateControlCopy},
		{"capped-crf", VideoSettings{Mode: VideoModeTranscode, CRF: 23, VideoMaxBitrate: "8000k"}, RateControlCappedCRF},
		{"crf-uncapped", VideoSettings{Mode: VideoModeTranscode, CRF: 20}, RateControlCRF},
		{"quality-capped", VideoSettings{Mode: VideoModeTranscode, VideoQuality: 60, VideoMaxBitrate: "8000k"}, RateControlCappedCRF},
		{"bare-maxrate", VideoSettings{Mode: VideoModeTranscode, VideoMaxBitrate: "8000k"}, RateControlCappedCRF},
		{"target", VideoSettings{Mode: VideoModeTranscode, VideoBitrate: "5000k"}, RateControlTarget},
		{"target-with-peak", VideoSettings{Mode: VideoModeTranscode, VideoBitrate: "5000k", VideoMaxBitrate: "8000k"}, RateControlTarget},
		{"cbr", VideoSettings{Mode: VideoModeTranscode, VideoBitrate: "5000k", VideoMaxBitrate: "5000k"}, RateControlCBR},
		{"cbr-mixed-units", VideoSettings{Mode: VideoModeTranscode, VideoBitrate: "5M", VideoMaxBitrate: "5000k"}, RateControlCBR},
		{"unknown", VideoSettings{Mode: VideoModeTranscode}, RateControlUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.RateControl(); got != tc.want {
				t.Fatalf("RateControl() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuiltInRateControl(t *testing.T) {
	want := map[string]RateControl{
		DefaultName:            RateControlCappedCRF, // CRF 23 + 8 Mbps VBV cap
		H2641080p20MbpsName:    RateControlCappedCRF,
		HEVC1080p16MbpsHDRName: RateControlCappedCRF,
		HEVC2160p40MbpsHDRName: RateControlCappedCRF,
		HEVCCopySourceName:     RateControlCopy,
		// Music packages a static video source with encoder defaults; its sizing
		// is driven by audio, not video rate control (see EstimateSize).
		MusicName: RateControlUnknown,
	}
	for name, wantRC := range want {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("profile %q missing", name)
		}
		if got := p.Video.RateControl(); got != wantRC {
			t.Errorf("%s: RateControl() = %q, want %q", name, got, wantRC)
		}
	}
}

func TestParseBitrate(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"  ", 0},
		{"8000k", 8_000_000},
		{"256K", 256_000},
		{"8M", 8_000_000},
		{"40m", 40_000_000},
		{"2G", 2_000_000_000},
		{"8000000", 8_000_000},
		{" 8000k ", 8_000_000},
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := ParseBitrate(tc.in); got != tc.want {
			t.Errorf("ParseBitrate(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
