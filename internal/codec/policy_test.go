package codec

import "testing"

func TestCheckAllowed(t *testing.T) {
	cases := []Probe{
		{Container: "mkv", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "dts-hd-ma"},
		{Container: "mp4", VideoCodec: "h264", VideoHeight: 720, AudioCodec: "aac"},
		{Container: "MKV", VideoCodec: "H264", VideoHeight: 1080, AudioCodec: "AC3"},
		{Container: "mkv", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "eac3"},
		{Container: "mkv", VideoCodec: "vc1", VideoHeight: 1080, AudioCodec: "dts-hd-hra"},
		{Container: "mkv", VideoCodec: "mpeg2video", VideoHeight: 480, AudioCodec: "ac3"},
		{Container: "mp4", VideoCodec: "vp9", VideoHeight: 1080, AudioCodec: "opus"},
	}
	for _, p := range cases {
		if reason, ok := Check(p); !ok {
			t.Errorf("expected ok for %+v, got reason=%q", p, reason)
		}
	}
}

func TestCheckRejected(t *testing.T) {
	cases := []struct {
		probe Probe
		want  string
	}{
		{Probe{Container: "mkv", VideoCodec: "prores", VideoHeight: 1080, AudioCodec: "aac"}, "video_codec=prores"},
		{Probe{Container: "mkv", VideoCodec: "hevc", VideoHeight: 1080, AudioCodec: "aac"}, "video_codec=hevc"},
		{Probe{Container: "mp4", VideoCodec: "av1", VideoHeight: 1080, AudioCodec: "aac"}, "video_codec=av1"},
		{Probe{Container: "mkv", VideoCodec: "h264", VideoHeight: 2160, AudioCodec: "aac"}, "video_height=2160"},
		{Probe{Container: "avi", VideoCodec: "h264", VideoHeight: 720, AudioCodec: "aac"}, "container=avi"},
		{Probe{Container: "mkv", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "wmav2"}, "audio_codec=wmav2"},
		// HEVC without HDR transfer characteristic is still rejected.
		{Probe{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, AudioCodec: "aac"}, "video_codec=hevc"},
	}
	for _, c := range cases {
		reason, ok := Check(c.probe)
		if ok {
			t.Errorf("expected reject for %+v", c.probe)
			continue
		}
		if !contains(reason, c.want) {
			t.Errorf("reason %q missing %q", reason, c.want)
		}
	}
}

func TestCheckHDRAllowed(t *testing.T) {
	cases := []Probe{
		// HEVC with PQ transfer and 4K → allowed via HDR gate.
		{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, ColorTransfer: "smpte2084", AudioCodec: "aac"},
		// HEVC with HLG and 4K → also HDR.
		{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, ColorTransfer: "arib-std-b67", AudioCodec: "aac"},
		// HEVC with PQ at 1080p → allowed.
		{Container: "mkv", VideoCodec: "hevc", VideoHeight: 1080, ColorTransfer: "smpte2084", AudioCodec: "aac"},
		// h264 with PQ at 4K → height cap lifted by HDR gate (h264 already allowed).
		{Container: "mkv", VideoCodec: "h264", VideoHeight: 2160, ColorTransfer: "smpte2084", AudioCodec: "aac"},
	}
	for _, p := range cases {
		if reason, ok := Check(p); !ok {
			t.Errorf("expected ok for %+v, got reason=%q", p, reason)
		}
	}
}

func TestIsHDRTransfer(t *testing.T) {
	hdr := []string{"smpte2084", "arib-std-b67", "SMPTE2084", " smpte2084 "}
	for _, tr := range hdr {
		if !IsHDRTransfer(tr) {
			t.Errorf("IsHDRTransfer(%q)=false, want true", tr)
		}
	}
	sdr := []string{"", "bt709", "bt601", "smpte170m", "unknown", "bt2020-10"}
	for _, tr := range sdr {
		if IsHDRTransfer(tr) {
			t.Errorf("IsHDRTransfer(%q)=true, want false", tr)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
