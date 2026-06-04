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
