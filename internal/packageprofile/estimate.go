package packageprofile

// SizeInputs carries the per-media facts a size estimate needs beyond the
// profile itself. All bitrates are bits per second; 0 means unknown.
type SizeInputs struct {
	// DurationMs is the media (or scheduled segment) duration. A non-positive
	// value yields an empty estimate.
	DurationMs int64
	// SourceVideoBitrateBps is the source video bitrate (ffprobe), used as the
	// output video bitrate for copy profiles where the stream is remuxed
	// unchanged. 0 = unknown.
	SourceVideoBitrateBps int64
	// SourceAudioBitrateBps is the source audio bitrate, used only when the
	// profile copies audio. 0 = unknown.
	SourceAudioBitrateBps int64
	// ExpectedVideoBitrateBps, when > 0, supplies a measured/expected average
	// video bitrate for quality-based modes (CRF, capped CRF) whose output size
	// cannot be derived from the profile alone. This is the seam for the
	// empirical per-profile model; without it those modes report only a ceiling.
	ExpectedVideoBitrateBps int64
}

// SizeEstimate is the result of EstimateSize. ExpectedBytes (valid only when
// ExpectedKnown) is the best single estimate of finished package size; MaxBytes
// is the worst-case size for capacity/bandwidth budgeting. The two coincide for
// copy/target/CBR and diverge for capped CRF, where expected is below the cap
// (and unknown until an empirical bitrate is supplied).
type SizeEstimate struct {
	Mode          RateControl
	ExpectedBytes int64
	ExpectedKnown bool
	MaxBytes      int64
	// VideoBitrateBps and AudioBitrateBps are the output bitrates used to derive
	// ExpectedBytes (0 when that component is unknown).
	VideoBitrateBps int64
	AudioBitrateBps int64
}

// EstimateSize estimates the finished package size for profile p over media
// described by in. Sizes are stream payload (video+audio) × duration and
// exclude container/muxing overhead (~1–2%). The expected size is firm for copy
// and target/CBR modes; for capped/uncapped CRF it is known only when
// in.ExpectedVideoBitrateBps is supplied, otherwise only a ceiling is returned.
func EstimateSize(p Profile, in SizeInputs) SizeEstimate {
	est := SizeEstimate{Mode: p.Video.RateControl()}
	if in.DurationMs <= 0 {
		return est
	}

	// Output audio bitrate: fixed by the profile when transcoding, the source's
	// own bitrate when copying.
	switch p.Audio.Mode {
	case AudioModeTranscode:
		est.AudioBitrateBps = ParseBitrate(p.Audio.Bitrate)
	case AudioModeCopy:
		est.AudioBitrateBps = in.SourceAudioBitrateBps
	}

	// Audio-only (music) profiles: size is driven by the (fixed-bitrate AAC)
	// audio; the static video track is negligible, so audio alone is a firm
	// estimate and its own budgeting ceiling.
	if NormalizeMediaKind(p.MediaKind) == MediaKindMusic {
		est.ExpectedBytes = bytesFor(est.AudioBitrateBps, in.DurationMs)
		est.ExpectedKnown = est.AudioBitrateBps > 0
		est.MaxBytes = est.ExpectedBytes
		return est
	}

	maxVideoBps := ParseBitrate(p.Video.VideoMaxBitrate)
	switch est.Mode {
	case RateControlCopy:
		est.VideoBitrateBps = in.SourceVideoBitrateBps
		est.ExpectedKnown = est.VideoBitrateBps > 0
	case RateControlTarget, RateControlCBR:
		// ABR/CBR hold the average at the target; a -maxrate is only a peak
		// limiter and does not raise expected size, so expected is also the
		// budgeting size.
		est.VideoBitrateBps = ParseBitrate(p.Video.VideoBitrate)
		est.ExpectedKnown = est.VideoBitrateBps > 0
	case RateControlCappedCRF, RateControlCRF:
		// Quality-based: output bitrate is content-dependent. Known only when an
		// empirical/expected video bitrate is supplied, clamped to the cap.
		if in.ExpectedVideoBitrateBps > 0 {
			est.VideoBitrateBps = in.ExpectedVideoBitrateBps
			if maxVideoBps > 0 && est.VideoBitrateBps > maxVideoBps {
				est.VideoBitrateBps = maxVideoBps
			}
			est.ExpectedKnown = true
		}
		// Worst case: a CRF encode can push its average up to the VBV ceiling.
		if maxVideoBps > 0 {
			est.MaxBytes = bytesFor(maxVideoBps+est.AudioBitrateBps, in.DurationMs)
		}
	}

	if est.ExpectedKnown {
		est.ExpectedBytes = bytesFor(est.VideoBitrateBps+est.AudioBitrateBps, in.DurationMs)
	}
	// For modes whose expected size is firm (copy, target, CBR) the budgeting
	// ceiling is the expected size itself.
	if est.MaxBytes == 0 && est.ExpectedKnown {
		est.MaxBytes = est.ExpectedBytes
	}
	return est
}

// bytesFor returns the byte count for a stream of bps bits per second over
// durationMs milliseconds. bps × ms stays well within int64 for realistic
// bitrates and runtimes (≈1e8 × 1e7 ≪ 9.2e18).
func bytesFor(bps, durationMs int64) int64 {
	if bps <= 0 || durationMs <= 0 {
		return 0
	}
	return bps * durationMs / 8000
}
