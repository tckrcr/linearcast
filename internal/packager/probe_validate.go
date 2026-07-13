package packager

// probe_validate.go adds a decode-level pre-flight to the packager: it confirms
// a ready package's m3u8 actually references segments a player can decode, not
// merely that the files exist and the playlist parses. The file-presence and
// duration checks in integrity.go can all pass for a package whose init.mp4 and
// seg*.m4s do not combine into a decodable stream (corrupt moof/mdat, a broken
// init<->segment relationship, the wrong codec for the profile, or a
// truncated/empty tail segment that slips under the duration tolerance).
//
// The probe demuxes the package's first and last fragments with ffprobe: each
// is fed as a byte-concat of init.mp4 + the fragment (a valid MP4 byte stream)
// and ffprobe is asked to COUNT PACKETS. Counting packets reads the fragment's
// sample tables without decoding any frames, which is the cheap level that
// actually distinguishes a real fragment from a stub: a stream-metadata probe
// reports codec/kind from init.mp4's moov box alone and so passes even a 0-byte
// segment. An empty or corrupt fragment yields zero packets (or an ffprobe
// error); a healthy one yields a packet count > 0 for the expected stream.
//
// Probing only the first and last fragment bounds cost at two single-fragment
// demuxes per package regardless of media length, while catching the common
// breakages: a bad init<->segment relationship at the head and a truncated or
// killed-encode stub at the tail. The linearcast-maint validate-segments command
// is the caller; it scopes probing to the packages backing the upcoming schedule.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ffmpegexec"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// ProbeReport is the result of probing one ready package for decodability.
// Reason is populated only when OK is false.
type ProbeReport struct {
	PackageID string
	MediaID   string
	Profile   string
	OK        bool
	Reason    string
}

// probeStreamInfo is the subset of ffprobe's per-stream output the classifier
// needs. NbReadPackets is a string in ffprobe JSON (e.g. "150") and is omitted
// entirely when zero, so an absent field parses as no packets read.
type probeStreamInfo struct {
	Streams []struct {
		CodecType     string `json:"codec_type"`
		CodecName     string `json:"codec_name"`
		NbReadPackets string `json:"nb_read_packets"`
	} `json:"streams"`
}

// ProbePackageDecodable runs a packet-counting ffprobe pass over a ready
// package's first and last fragments and reports whether the m3u8 references
// segments that demux to a decodable stream of the expected kind. It performs no
// DB writes; the caller decides whether to requeue a failing package.
func ProbePackageDecodable(ctx context.Context, pkg db.MediaPackage, profile packageprofile.Profile) ProbeReport {
	rep := ProbeReport{PackageID: pkg.ID, MediaID: pkg.MediaID, Profile: pkg.RenditionProfile}

	if pkg.InitSegmentPath == nil || *pkg.InitSegmentPath == "" {
		rep.Reason = "missing init_segment_path"
		return rep
	}
	initPath := *pkg.InitSegmentPath
	if err := requireRegularFile(initPath); err != nil {
		rep.Reason = fmt.Sprintf("init segment %s: %v", initPath, err)
		return rep
	}

	// readyPackageManifestSegments validates package_root, that stream.m3u8
	// exists/parses, and that it lists at least one segment (shared with the
	// file-integrity guard). Its errors are decode-relevant failures here too.
	root, segments, err := readyPackageManifestSegments(pkg)
	if err != nil {
		rep.Reason = err.Error()
		return rep
	}

	kind := packageprofile.NormalizeMediaKind(profile.MediaKind)
	wantCodec := expectedCodec(profile)

	// Probe the first and last fragment (deduped for a single-segment package).
	for _, i := range []int{0, len(segments) - 1} {
		seg := segments[i]
		if i != 0 && seg.URI == segments[0].URI {
			continue
		}
		segPath := filepath.Join(root, filepath.FromSlash(seg.URI))
		if err := requireRegularFile(segPath); err != nil {
			rep.Reason = fmt.Sprintf("segment %s: %v", seg.URI, err)
			return rep
		}
		out, err := ffprobeSegmentPackets(ctx, initPath, segPath)
		if err != nil {
			rep.Reason = fmt.Sprintf("ffprobe segment %s: %v", seg.URI, err)
			return rep
		}
		if ok, reason := classifyProbeStreams(out, kind, wantCodec); !ok {
			rep.Reason = "segment " + seg.URI + ": " + reason
			return rep
		}
	}

	rep.OK = true
	return rep
}

// expectedCodec returns the codec name a probe should see for a copy profile,
// where the output codec is pinned to the source (CodecRequired). Transcode
// profiles name an encoder (e.g. libx264) rather than a stream codec, so they
// return "" and skip the cross-check.
func expectedCodec(profile packageprofile.Profile) string {
	if packageprofile.NormalizeMediaKind(profile.MediaKind) != packageprofile.MediaKindVideo {
		return ""
	}
	if profile.Video.Mode == packageprofile.VideoModeCopy {
		return strings.ToLower(strings.TrimSpace(profile.Video.CodecRequired))
	}
	return ""
}

// ffprobeSegmentPackets demuxes one fragment (init.mp4 byte-concatenated with
// the segment) and returns ffprobe's per-stream codec + packet-count JSON.
// -count_packets reads packet boundaries from the fragment's sample tables
// without decoding frames.
func ffprobeSegmentPackets(ctx context.Context, initPath, segPath string) ([]byte, error) {
	cmd, err := ffmpegexec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-count_packets",
		"-show_entries", "stream=codec_type,codec_name,nb_read_packets",
		"-of", "json",
		"concat:"+initPath+"|"+segPath,
	)
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		// Surface ffprobe's stderr (captured by exec.ExitError) so the failure
		// reason is actionable rather than a bare exit status.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// classifyProbeStreams decides whether an ffprobe stream list is acceptable for
// a package of the given media kind. A video profile requires at least one video
// stream with a non-empty codec_name and packets read; a music profile requires
// at least one audio stream with packets read. When wantCodec is set (copy
// profiles), the video stream's codec_name must match it. Requiring packets > 0
// is what distinguishes a real fragment from a stub: codec/kind are reported
// from init.mp4's moov box even when the segment carries no samples. It is pure
// so it can be unit-tested without ffprobe.
func classifyProbeStreams(jsonBytes []byte, kind packageprofile.MediaKind, wantCodec string) (bool, string) {
	var info probeStreamInfo
	if err := json.Unmarshal(jsonBytes, &info); err != nil {
		return false, fmt.Sprintf("unparseable ffprobe output: %v", err)
	}
	if len(info.Streams) == 0 {
		return false, "no decodable streams"
	}

	if kind == packageprofile.MediaKindMusic {
		sawAudio := false
		for _, s := range info.Streams {
			if s.CodecType != "audio" || strings.TrimSpace(s.CodecName) == "" {
				continue
			}
			sawAudio = true
			if packetCount(s.NbReadPackets) > 0 {
				return true, ""
			}
		}
		if sawAudio {
			return false, "audio stream has no packets (empty or corrupt segment)"
		}
		return false, "no audio stream"
	}

	for _, s := range info.Streams {
		if s.CodecType != "video" {
			continue
		}
		got := strings.ToLower(strings.TrimSpace(s.CodecName))
		if got == "" {
			return false, "video stream has no codec"
		}
		if wantCodec != "" && got != wantCodec {
			return false, fmt.Sprintf("video codec %s does not match required %s", got, wantCodec)
		}
		if packetCount(s.NbReadPackets) <= 0 {
			return false, "video stream has no packets (empty or corrupt segment)"
		}
		return true, ""
	}
	return false, "no video stream"
}

// packetCount parses ffprobe's nb_read_packets string; an empty/absent value
// (omitted when zero) reads as 0.
func packetCount(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
