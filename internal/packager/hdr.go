package packager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tckrcr/linearcast/internal/codec"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
)

// ErrUnsupportedDolbyVision is returned when a source is Dolby Vision Profile 5
// (HEVC tag dvhe/dvh1, no HDR10 base layer). It cannot be transcoded or copied
// into a usable HLS stream, so it is treated as a terminal (no-retry) failure.
var ErrUnsupportedDolbyVision = errors.New("dolby vision profile 5 is not supported (no HDR10 base layer)")

// isDolbyVisionProfile5Stream reports whether the selected video stream is a
// DV-only HEVC bitstream (codec tag dvhe/dvh1).
func isDolbyVisionProfile5Stream(s probeStream) bool {
	return codec.IsDolbyVisionProfile5(s.CodecTagString)
}

// probeSideData is one ffprobe stream `side_data_list` entry. Only the HDR10
// static-metadata fields are modelled; everything else is ignored. The
// mastering-display coordinates arrive as rationals (e.g. "13250/50000"); the
// content-light fields arrive as plain integers.
type probeSideData struct {
	SideDataType string `json:"side_data_type"`
	RedX         string `json:"red_x"`
	RedY         string `json:"red_y"`
	GreenX       string `json:"green_x"`
	GreenY       string `json:"green_y"`
	BlueX        string `json:"blue_x"`
	BlueY        string `json:"blue_y"`
	WhitePointX  string `json:"white_point_x"`
	WhitePointY  string `json:"white_point_y"`
	MinLuminance string `json:"min_luminance"`
	MaxLuminance string `json:"max_luminance"`
	MaxContent   int64  `json:"max_content"`
	MaxAverage   int64  `json:"max_average"`
}

// isHEVCEncoder reports whether codecName is one of the HEVC encoders
// (libx265, hevc_nvenc, hevc_qsv, ...). HDR-preserving output and the hvc1 tag
// only apply to HEVC.
func isHEVCEncoder(codecName string) bool {
	c := strings.ToLower(codecName)
	return strings.Contains(c, "x265") || strings.Contains(c, "hevc")
}

// hdrTransfer normalises an ffprobe color_transfer into the x265/ffmpeg token
// used on the output. Defaults to PQ (smpte2084); HLG sources map to
// arib-std-b67.
func hdrTransfer(colorTransfer string) string {
	if strings.EqualFold(strings.TrimSpace(colorTransfer), "arib-std-b67") {
		return "arib-std-b67"
	}
	return "smpte2084"
}

// hdrColorArgs returns the ffmpeg output color-tag flags that mark the encoded
// stream as HDR BT.2020 (PQ or HLG) so players/displays tone-map it. These
// apply to any HEVC encoder (libx265 and NVENC alike).
func hdrColorArgs(colorTransfer string) []string {
	return []string{
		"-colorspace", "bt2020nc",
		"-color_primaries", "bt2020",
		"-color_trc", hdrTransfer(colorTransfer),
	}
}

// hevcHDRx265Params builds the libx265 -x265-params value that preserves HDR:
// the keyframe controls that hold the segment contract (libx265 can ignore a
// bare -g), the HDR signalling, and best-effort HDR10 static metadata
// (mastering display + content light level) when the source carries it.
func hevcHDRx265Params(gopFrames int, v *probeStream) string {
	parts := []string{
		fmt.Sprintf("keyint=%d", gopFrames),
		fmt.Sprintf("min-keyint=%d", gopFrames),
		"no-scenecut=1",
		"hdr-opt=1",
		"repeat-headers=1",
		"colorprim=bt2020",
		"transfer=" + hdrTransfer(v.ColorTransfer),
		"colormatrix=bt2020nc",
	}
	if md, ok := masteringDisplayParam(v.SideDataList); ok {
		parts = append(parts, "master-display="+md)
	}
	if cll, ok := contentLightParam(v.SideDataList); ok {
		parts = append(parts, "max-cll="+cll)
	}
	return strings.Join(parts, ":")
}

// masteringDisplayParam formats the x265 master-display string from a stream's
// "Mastering display metadata" side data. Returns ok=false unless every
// coordinate and luminance value parses, so a partial/garbled record never
// produces an invalid -x265-params (which would fail the encode).
func masteringDisplayParam(sd []probeSideData) (string, bool) {
	for _, d := range sd {
		if !strings.EqualFold(d.SideDataType, "Mastering display metadata") {
			continue
		}
		// Display primaries/white point are in units of 0.00002; luminance in
		// units of 0.0001 (HEVC SEI conventions, matching x265's master-display).
		gx, ok1 := scaledRational(d.GreenX, 50000)
		gy, ok2 := scaledRational(d.GreenY, 50000)
		bx, ok3 := scaledRational(d.BlueX, 50000)
		by, ok4 := scaledRational(d.BlueY, 50000)
		rx, ok5 := scaledRational(d.RedX, 50000)
		ry, ok6 := scaledRational(d.RedY, 50000)
		wpx, ok7 := scaledRational(d.WhitePointX, 50000)
		wpy, ok8 := scaledRational(d.WhitePointY, 50000)
		maxL, ok9 := scaledRational(d.MaxLuminance, 10000)
		minL, ok10 := scaledRational(d.MinLuminance, 10000)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8 && ok9 && ok10) {
			return "", false
		}
		return fmt.Sprintf("G(%d,%d)B(%d,%d)R(%d,%d)WP(%d,%d)L(%d,%d)",
			gx, gy, bx, by, rx, ry, wpx, wpy, maxL, minL), true
	}
	return "", false
}

// contentLightParam formats the x265 max-cll string ("MaxCLL,MaxFALL") from a
// stream's "Content light level metadata" side data.
func contentLightParam(sd []probeSideData) (string, bool) {
	for _, d := range sd {
		if !strings.EqualFold(d.SideDataType, "Content light level metadata") {
			continue
		}
		if d.MaxContent <= 0 {
			return "", false
		}
		return fmt.Sprintf("%d,%d", d.MaxContent, d.MaxAverage), true
	}
	return "", false
}

// scaledRational parses an ffprobe rational ("num/den" or a plain number) and
// returns round(value * scale) as an integer. ok=false on any parse failure.
func scaledRational(s string, scale float64) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var value float64
	if num, den, found := strings.Cut(s, "/"); found {
		n, err1 := strconv.ParseFloat(strings.TrimSpace(num), 64)
		d, err2 := strconv.ParseFloat(strings.TrimSpace(den), 64)
		if err1 != nil || err2 != nil || d == 0 {
			return 0, false
		}
		value = n / d
	} else {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		value = v
	}
	if value < 0 {
		return 0, false
	}
	return int64(math.Round(value * scale)), true
}

// sourceIsHDR reports whether the selected video stream signals HDR (PQ/HLG).
func sourceIsHDR(v *probeStream) bool {
	return v != nil && codec.IsHDRTransfer(v.ColorTransfer)
}

// tonemapSDRFilter is the ffmpeg -vf chain that converts an HDR (PQ or HLG,
// BT.2020) source to SDR BT.709 with a linear-light Hable tone-map. zscale
// reads the input transfer/primaries from the decoded frames' color metadata,
// so the same chain handles both PQ and HLG; npl=100 targets a 100-nit SDR
// display. Requires an ffmpeg built with libzimg (zscale) and the tonemap
// filter (the Alpine runtime image's ffmpeg has both). Used by SDR transcode
// profiles so an HDR source doesn't come out crushed and desaturated.
func tonemapSDRFilter() string {
	return "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=tonemap=hable:desat=0,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
}

type hdrFrameProbe struct {
	Frames []struct {
		SideDataList []probeSideData `json:"side_data_list"`
	} `json:"frames"`
}

// probeHDRSideData reads the first video frame's side data via ffprobe. HDR10
// mastering-display / content-light metadata lives at the frame level in MKV
// (and is absent from -show_streams), so this recovers it for the encode.
// Returns nil on any error or when no side data is present (best-effort).
func probeHDRSideData(ctx context.Context, path string) []probeSideData {
	cmd, err := ffmpegexec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-read_intervals", "%+#1",
		"-show_frames",
		"-print_format", "json",
		path,
	)
	if err != nil {
		return nil
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var fp hdrFrameProbe
	if err := json.Unmarshal(out, &fp); err != nil {
		return nil
	}
	for _, f := range fp.Frames {
		if len(f.SideDataList) > 0 {
			return f.SideDataList
		}
	}
	return nil
}

// enrichHDRSideData attaches frame-level HDR10 static metadata to the selected
// video stream when the stream-level probe didn't already carry it. No-op for
// SDR sources or when the metadata is already present / unreadable; the color
// tags are preserved regardless.
func enrichHDRSideData(ctx context.Context, probe *sourceProbe, path string) {
	sel := selectSourceStreams(*probe)
	if !sourceIsHDR(sel.Video) {
		return
	}
	if _, ok := masteringDisplayParam(sel.Video.SideDataList); ok {
		return
	}
	if sd := probeHDRSideData(ctx, path); len(sd) > 0 {
		// sel.Video points into probe.Streams' backing array.
		sel.Video.SideDataList = append(sel.Video.SideDataList, sd...)
	}
}
