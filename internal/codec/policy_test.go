package codec

import "testing"

func TestAdmitAllowed(t *testing.T) {
	cases := []Probe{
		{Container: "mkv", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "dts-hd-ma"},
		{Container: "mp4", VideoCodec: "h264", VideoHeight: 720, AudioCodec: "aac"},
		{Container: "MKV", VideoCodec: "H264", VideoHeight: 1080, AudioCodec: "AC3"},
		{Container: "mkv", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "eac3"},
		{Container: "mkv", VideoCodec: "vc1", VideoHeight: 1080, AudioCodec: "dts-hd-hra"},
		{Container: "mkv", VideoCodec: "mpeg2video", VideoHeight: 480, AudioCodec: "ac3"},
		{Container: "mp4", VideoCodec: "vp9", VideoHeight: 1080, AudioCodec: "opus"},
		// SDR 4K HEVC web-dl: copy rung remuxes it, transcode rungs downscale it.
		{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, CodecTagString: "hvc1", AudioCodec: "eac3"},
		// SDR HEVC at 1080p.
		{Container: "mkv", VideoCodec: "hevc", VideoHeight: 1080, AudioCodec: "aac"},
		// SDR 4K H.264 — height cap lifted.
		{Container: "mkv", VideoCodec: "h264", VideoHeight: 2160, AudioCodec: "aac"},
	}
	for _, p := range cases {
		if d := Admit(p); !d.OK {
			t.Errorf("expected ok for %+v, got reason=%q", p, d.Reason)
		}
	}
}

func TestAdmitRejected(t *testing.T) {
	cases := []struct {
		probe Probe
		want  string
	}{
		{Probe{Container: "mkv", VideoCodec: "prores", VideoHeight: 1080, AudioCodec: "aac"}, "video_codec=prores"},
		{Probe{Container: "mp4", VideoCodec: "av1", VideoHeight: 1080, AudioCodec: "aac"}, "video_codec=av1"},
		{Probe{Container: "avi", VideoCodec: "h264", VideoHeight: 720, AudioCodec: "aac"}, "container=avi"},
		{Probe{Container: "mkv", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "wmav2"}, "audio_codec=wmav2"},
		// Dolby Vision Profile 5 (dvhe/dvh1 tag) is rejected — no HDR10 base layer,
		// even with an HDR transfer characteristic present.
		{Probe{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, ColorTransfer: "smpte2084", CodecTagString: "dvhe", AudioCodec: "eac3"}, "dolby_vision_p5=dvhe"},
		{Probe{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, CodecTagString: "dvh1", AudioCodec: "eac3"}, "dolby_vision_p5=dvh1"},
	}
	for _, c := range cases {
		d := Admit(c.probe)
		if d.OK {
			t.Errorf("expected reject for %+v", c.probe)
			continue
		}
		if !contains(d.Reason, c.want) {
			t.Errorf("reason %q missing %q", d.Reason, c.want)
		}
	}
}

func TestAdmitHDRAllowed(t *testing.T) {
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
		if d := Admit(p); !d.OK {
			t.Errorf("expected ok for %+v, got reason=%q", p, d.Reason)
		}
	}
}

// TestAdmitCharacterization pins the exact admission decision (OK + full reason
// string) for a corpus of real-world probes. It is the parity record for the
// single-entrypoint refactor: any future policy change (Phase 2: DV P5, SDR 4K
// HEVC, thresholds) must update an expectation here, making the behavior change
// explicit and reviewable rather than silent.
func TestAdmitCharacterization(t *testing.T) {
	cases := []struct {
		name string
		p    Probe
		want Decision
	}{
		{
			// The 40 files from the 2026-06-15 Plex scan: SDR 4K HEVC web-dl
			// (Main profile, no HDR transfer). Now admitted (Phase 2: HEVC
			// allowed, height cap lifted) — previously rejected on both axes.
			name: "sdr-4k-hevc-webdl",
			p:    Probe{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, ColorTransfer: "", CodecTagString: "hvc1", AudioCodec: "eac3"},
			want: Decision{OK: true},
		},
		{
			// The user's working 4K HDR DV TV shows: DV Profile 7/8 with an HDR10
			// base layer, tagged hev1/hvc1 (not dvhe/dvh1). Admitted.
			name: "dv-hdr-remux-2160",
			p:    Probe{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, ColorTransfer: "smpte2084", CodecTagString: "hev1", AudioCodec: "truehd"},
			want: Decision{OK: true},
		},
		{
			// Dolby Vision Profile 5 (dvhe tag, no usable HDR10 base). Rejected at
			// admission to match the packager's terminal ErrUnsupportedDolbyVision,
			// even though an HDR transfer characteristic is present.
			name: "dv-profile5-rejected",
			p:    Probe{Container: "mkv", VideoCodec: "hevc", VideoHeight: 2160, ColorTransfer: "smpte2084", CodecTagString: "dvhe", AudioCodec: "eac3"},
			want: Decision{OK: false, Reason: "dolby_vision_p5=dvhe"},
		},
		{
			name: "h264-1080-clean",
			p:    Probe{Container: "mkv", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "aac"},
			want: Decision{OK: true},
		},
		{
			name: "h264-2160-sdr-admitted",
			p:    Probe{Container: "mkv", VideoCodec: "h264", VideoHeight: 2160, AudioCodec: "aac"},
			want: Decision{OK: true},
		},
		{
			name: "bad-container-and-audio",
			p:    Probe{Container: "avi", VideoCodec: "h264", VideoHeight: 720, AudioCodec: "wmav2"},
			want: Decision{OK: false, Reason: "container=avi; audio_codec=wmav2"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Admit(c.p)
			if got != c.want {
				t.Errorf("Admit(%+v) = %+v, want %+v", c.p, got, c.want)
			}
		})
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

func TestIsDolbyVisionProfile5(t *testing.T) {
	dv5 := []string{"dvhe", "dvh1", "DVHE", " dvh1 "}
	for _, tag := range dv5 {
		if !IsDolbyVisionProfile5(tag) {
			t.Errorf("IsDolbyVisionProfile5(%q)=false, want true", tag)
		}
	}
	// hvc1/hev1 are HDR10-compatible HEVC (incl. DV Profile 7/8 with a base
	// layer) and must pass through.
	notDV5 := []string{"", "hvc1", "hev1", "avc1", "vp09"}
	for _, tag := range notDV5 {
		if IsDolbyVisionProfile5(tag) {
			t.Errorf("IsDolbyVisionProfile5(%q)=true, want false", tag)
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
