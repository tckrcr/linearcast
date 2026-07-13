package packageprofile

import "testing"

// dur8s makes byte math trivial to assert: bytesFor(bps, 8000) == bps, so an
// expected byte count equals the summed output bitrate in bps.
const dur8s = int64(8000)

func transcodeAudio256k() AudioSettings {
	return AudioSettings{Mode: AudioModeTranscode, Codec: "aac", Bitrate: "256k", Channels: 2, SampleHz: 48000}
}

func TestEstimateSizeCopyExact(t *testing.T) {
	p := Profile{
		MediaKind: MediaKindVideo,
		Video:     VideoSettings{Mode: VideoModeCopy, CodecRequired: "hevc"},
		Audio:     transcodeAudio256k(),
	}
	est := EstimateSize(p, SizeInputs{DurationMs: dur8s, SourceVideoBitrateBps: 20_000_000})
	if est.Mode != RateControlCopy {
		t.Fatalf("mode = %q", est.Mode)
	}
	if !est.ExpectedKnown {
		t.Fatalf("copy with known source bitrate should be known")
	}
	// 20 Mbps video remuxed unchanged + 256 kbps transcoded audio.
	if want := int64(20_256_000); est.ExpectedBytes != want {
		t.Errorf("ExpectedBytes = %d, want %d", est.ExpectedBytes, want)
	}
	// Copy is exact: the budgeting ceiling equals the expected size.
	if est.MaxBytes != est.ExpectedBytes {
		t.Errorf("MaxBytes = %d, want = ExpectedBytes %d", est.MaxBytes, est.ExpectedBytes)
	}
}

func TestEstimateSizeCopyUnknownSource(t *testing.T) {
	p := Profile{MediaKind: MediaKindVideo, Video: VideoSettings{Mode: VideoModeCopy}, Audio: transcodeAudio256k()}
	est := EstimateSize(p, SizeInputs{DurationMs: dur8s}) // no source bitrate
	if est.ExpectedKnown || est.ExpectedBytes != 0 {
		t.Errorf("copy with unknown source bitrate should not be known: %+v", est)
	}
}

func cappedCRFDefault() Profile {
	return Profile{
		MediaKind: MediaKindVideo,
		Video:     VideoSettings{Mode: VideoModeTranscode, Codec: "libx264", CRF: 23, VideoMaxBitrate: "8000k"},
		Audio:     transcodeAudio256k(),
	}
}

func TestEstimateSizeCappedCRFCeilingOnly(t *testing.T) {
	est := EstimateSize(cappedCRFDefault(), SizeInputs{DurationMs: dur8s})
	if est.Mode != RateControlCappedCRF {
		t.Fatalf("mode = %q", est.Mode)
	}
	if est.ExpectedKnown {
		t.Errorf("capped CRF without an empirical bitrate must not report expected: %+v", est)
	}
	// Worst case: 8 Mbps cap + 256 kbps audio.
	if want := int64(8_256_000); est.MaxBytes != want {
		t.Errorf("MaxBytes = %d, want %d", est.MaxBytes, want)
	}
}

func TestEstimateSizeCappedCRFWithEmpirical(t *testing.T) {
	est := EstimateSize(cappedCRFDefault(), SizeInputs{DurationMs: dur8s, ExpectedVideoBitrateBps: 4_000_000})
	if !est.ExpectedKnown {
		t.Fatalf("empirical input should make expected known")
	}
	if want := int64(4_256_000); est.ExpectedBytes != want {
		t.Errorf("ExpectedBytes = %d, want %d", est.ExpectedBytes, want)
	}
	if want := int64(8_256_000); est.MaxBytes != want {
		t.Errorf("MaxBytes = %d, want %d", est.MaxBytes, want)
	}
	if est.ExpectedBytes >= est.MaxBytes {
		t.Errorf("expected (%d) should be below ceiling (%d)", est.ExpectedBytes, est.MaxBytes)
	}
}

func TestEstimateSizeCappedCRFEmpiricalClampedToCap(t *testing.T) {
	// An empirical bitrate above the cap is impossible in practice; clamp it.
	est := EstimateSize(cappedCRFDefault(), SizeInputs{DurationMs: dur8s, ExpectedVideoBitrateBps: 9_000_000})
	if want := int64(8_256_000); est.ExpectedBytes != want {
		t.Errorf("ExpectedBytes = %d, want clamped %d", est.ExpectedBytes, want)
	}
}

func TestEstimateSizeTarget(t *testing.T) {
	p := Profile{
		MediaKind: MediaKindVideo,
		Video:     VideoSettings{Mode: VideoModeTranscode, Codec: "libx264", VideoBitrate: "5000k", VideoMaxBitrate: "8000k"},
		Audio:     transcodeAudio256k(),
	}
	est := EstimateSize(p, SizeInputs{DurationMs: dur8s})
	if est.Mode != RateControlTarget {
		t.Fatalf("mode = %q", est.Mode)
	}
	// ABR holds the average at the target; the -maxrate peak does not raise size.
	if want := int64(5_256_000); est.ExpectedBytes != want || est.MaxBytes != want {
		t.Errorf("expected=%d max=%d, want both %d", est.ExpectedBytes, est.MaxBytes, want)
	}
}

func TestEstimateSizeMusic(t *testing.T) {
	p := Profile{
		MediaKind: MediaKindMusic,
		Video:     VideoSettings{Mode: VideoModeTranscode},
		Audio:     transcodeAudio256k(),
	}
	est := EstimateSize(p, SizeInputs{DurationMs: dur8s})
	if !est.ExpectedKnown {
		t.Fatalf("music audio is fixed-bitrate; expected should be known")
	}
	if want := int64(256_000); est.ExpectedBytes != want || est.MaxBytes != want {
		t.Errorf("expected=%d max=%d, want both %d", est.ExpectedBytes, est.MaxBytes, want)
	}
}

func TestEstimateSizeZeroDuration(t *testing.T) {
	est := EstimateSize(cappedCRFDefault(), SizeInputs{DurationMs: 0, ExpectedVideoBitrateBps: 4_000_000})
	if est.ExpectedKnown || est.ExpectedBytes != 0 || est.MaxBytes != 0 {
		t.Errorf("zero duration should yield empty estimate: %+v", est)
	}
}
