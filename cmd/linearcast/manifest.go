package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/codec"
	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

func (a *app) handleKeepalive(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	if a.lookupChannelOr404(r.Context(), w, channelID) == nil {
		return
	}
	alive := true
	if a.sessions != nil {
		alive = a.sessions.KeepAlive(channelID)
	}
	if !alive {
		w.Header().Set("Retry-After", "2")
		http.Error(w, "keepalive ceiling reached", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *app) handleManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if rt.PlaybackMode == db.PlaybackModePlexRelay {
		http.Redirect(w, r, "/channel/"+channelID+"/plexrelay.m3u8", http.StatusFound)
		return
	}
	if a.sessions != nil {
		a.sessions.Touch(rt.ID)
	}
	profile := rt.RequiredPackageProfile
	nowMs := time.Now().UTC().UnixMilli()

	entries, err := db.ScheduleWindow(r.Context(), a.dbConn, channelID, nowMs, nowMs+manifestAheadMs)
	if err != nil || len(entries) == 0 {
		a.writePackagedManifest(w, r, channelID, profile)
		return
	}

	currentEntry := db.FindScheduleEntry(entries, nowMs)
	if currentEntry == nil {
		currentEntry = &entries[0]
	}

	burnActive := a.sessions != nil && a.sessions.BurnSubtitleLanguage(channelID) != ""
	variants := a.readyPackagedVariants(r.Context(), rt, currentEntry.MediaID)
	var pkg *db.MediaPackage
	for _, v := range variants {
		if v.Profile == profile {
			pkg = v.Package
			break
		}
	}
	if burnActive {
		pkg = nil
		variants = nil
	}
	if pkg == nil {
		if rt.PrefillMode != "on_demand" {
			// No package is ready; fall back to the segment playlist directly so
			// the player sees the 503 "warming up" UX.
			a.writePackagedManifest(w, r, channelID, profile)
			return
		}
		initPath, bps, err := a.ensureOnDemandMasterReady(r.Context(), channelID, profile, *currentEntry, nowMs)
		if err != nil {
			if errors.Is(err, ondemand.ErrAtCapacity) {
				metrics.OnDemandAtCapacity503Total.Inc()
				w.Header().Set("Retry-After", "2")
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			if strings.Contains(err.Error(), "warming") {
				metrics.OnDemandWarming503Total.Inc()
				w.Header().Set("Retry-After", "2")
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		codecs := a.codecStringForInit(initPath)
		a.writeMasterManifest(r.Context(), w, channelID, []masterVariant{{
			Profile: profile,
			BPS:     bps,
			Codecs:  codecs,
		}}, entries)
		return
	}
	a.writeMasterManifest(r.Context(), w, channelID, variants, entries)
}

type masterVariant struct {
	Profile    string
	Package    *db.MediaPackage
	BPS        int64
	Res        string
	Codecs     string
	VideoRange string // "" = SDR (default), "PQ" or "HLG" for HDR variants
}

func (a *app) readyPackagedVariants(ctx context.Context, rt *channelRuntime, mediaID string) []masterVariant {
	ladder := rt.ABRLadder
	if len(ladder) == 0 {
		ladder = []string{rt.RequiredPackageProfile}
	}
	var variants []masterVariant
	for _, profile := range ladder {
		pkg, _ := db.ReadyMediaPackage(ctx, a.dbConn, mediaID, profile)
		if pkg == nil {
			continue
		}
		variants = append(variants, a.masterVariantForPackage(ctx, profile, pkg))
	}
	return variants
}

func (a *app) masterVariantForPackage(ctx context.Context, profile string, pkg *db.MediaPackage) masterVariant {
	var profileBPS int64
	if p, err := db.GetPackageProfile(ctx, a.dbConn, profile); err == nil && p != nil && p.Video.VideoMaxBitrate != "" {
		profileBPS = parseBitrateString(p.Video.VideoMaxBitrate)
	}
	bps := db.PeakSegmentBps(ctx, a.dbConn, pkg.ID)
	if bps == 0 {
		bps = profileBPS
	} else if pkg.VideoCodec == "hevc" && profileBPS > 0 && bps > profileBPS {
		// Copy-profile segments inherit source keyframe spacing, which can
		// produce irregular spikes. Cap at the profile's max bitrate so the
		// BANDWIDTH attribute doesn't scare hls.js away from the HEVC variant.
		bps = profileBPS
	}
	if bps == 0 {
		bps = 8_000_000
	}
	codecs := "avc1.4d401f,mp4a.40.2"
	if pkg.InitSegmentPath != nil {
		codecs = a.codecStringForInit(*pkg.InitSegmentPath)
	}
	res := ""
	if pkg.VideoWidth != nil && pkg.VideoHeight != nil {
		res = fmt.Sprintf(",RESOLUTION=%dx%d", *pkg.VideoWidth, *pkg.VideoHeight)
	}
	videoRange := hdrVideoRange(ctx, a.dbConn, pkg.MediaID, profile)
	return masterVariant{Profile: profile, Package: pkg, BPS: bps, Res: res, Codecs: codecs, VideoRange: videoRange}
}

// hdrVideoRange queries the source media's color_transfer to determine the HDR
// video range. Returns "PQ", "HLG", or "" (SDR default).
//
// Only HEVC copy profiles preserve HDR metadata. SDR transcoded rungs must not
// advertise VIDEO-RANGE even when the source is HDR.
func hdrVideoRange(ctx context.Context, conn *sql.DB, mediaID string, profileName string) string {
	if !strings.Contains(profileName, "hevc-copy") {
		return ""
	}
	m, err := db.MediaByID(ctx, conn, mediaID)
	if err != nil || m == nil {
		return ""
	}
	if codec.IsHDRTransfer(m.ColorTransfer) {
		switch strings.ToLower(strings.TrimSpace(m.ColorTransfer)) {
		case "arib-std-b67":
			return "HLG"
		default:
			return "PQ"
		}
	}
	return ""
}

func (a *app) writeMasterManifest(ctx context.Context, w http.ResponseWriter, channelID string, variants []masterVariant, entries []db.ScheduleEntry) {
	mediaIDs := make([]string, 0, len(entries))
	seen := make(map[string]bool)
	for _, e := range entries {
		if !seen[e.MediaID] {
			seen[e.MediaID] = true
			mediaIDs = append(mediaIDs, e.MediaID)
		}
	}
	subtitleTracks, err := db.SubtitleTracksForMediaIDs(ctx, a.dbConn, mediaIDs)
	if err != nil {
		subtitleTracks = nil
	}

	autoEnable, _ := db.GetSubtitleAutoEnable(ctx, a.dbConn)
	langPrefs, _ := db.GetSubtitleLanguagePreference(ctx, a.dbConn)
	topLang := ""
	if autoEnable && len(langPrefs) > 0 {
		topLang = strings.ToLower(langPrefs[0])
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")

	if len(subtitleTracks) > 0 {
		for _, t := range subtitleTracks {
			lang := t.Language
			if lang == "" {
				lang = "und"
			}
			name := languageLabel(lang)
			isDefault := "NO"
			if topLang != "" && strings.ToLower(lang) == topLang {
				isDefault = "YES"
			}
			fmt.Fprintf(&b,
				"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=%s,URI=\"/channel/%s/%s/subs/%s/playlist.m3u8\"\n",
				name, lang, isDefault, isDefault, channelID, packagedPath, lang)
		}
	}

	subsAttr := ""
	if len(subtitleTracks) > 0 {
		subsAttr = ",SUBTITLES=\"subs\""
	}

	for _, v := range variants {
		extraAttr := subsAttr
		if v.VideoRange != "" {
			extraAttr += ",VIDEO-RANGE=" + v.VideoRange
		}
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d%s,CODECS=%q%s\n",
			v.BPS, v.Res, v.Codecs, extraAttr)
		fmt.Fprintf(&b, "/channel/%s/%s/%s/stream.m3u8\n", channelID, packagedPath, v.Profile)
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

func (a *app) ensureOnDemandMasterReady(ctx context.Context, channelID, profile string, entry db.ScheduleEntry, nowMs int64) (string, int64, error) {
	if a.sessions == nil {
		return "", 0, fmt.Errorf("on-demand sessions unavailable")
	}
	p, err := db.GetPackageProfile(ctx, a.dbConn, profile)
	if err != nil {
		return "", 0, fmt.Errorf("package profile %s: %w", profile, err)
	}
	if p == nil {
		return "", 0, fmt.Errorf("package profile %s not found", profile)
	}
	media, err := db.MediaByID(ctx, a.dbConn, entry.MediaID)
	if err != nil {
		return "", 0, fmt.Errorf("media %s: %w", entry.MediaID, err)
	}
	if media == nil {
		return "", 0, fmt.Errorf("media %s not found", entry.MediaID)
	}
	opts := ondemand.SessionOptions{BurnSubtitleStreamIndex: a.burnSubtitleStreamIndexForMedia(ctx, channelID, media.ID)}
	if err := a.sessions.EnsureSessionWithOptions(ctx, channelID, entry, media.Path, *p, scheduler.TargetSegmentMs, opts); err != nil {
		return "", 0, err
	}
	mediaPosMs := entry.OffsetMs + (nowMs - entry.StartMs)
	segs := a.sessions.SegmentsFrom(channelID, entry.ID, mediaPosMs, 1)
	if len(segs) == 0 {
		return "", 0, fmt.Errorf("on-demand session warming media=%s profile=%s", entry.MediaID, profile)
	}
	bps := parseBitrateString(p.Video.VideoMaxBitrate)
	if p.Video.Mode == packageprofile.VideoModeCopy {
		// Copy mode preserves the source video bitrate, so the profile's
		// transcode cap (or the 8 Mbps fallback) can understate BANDWIDTH by
		// 3-4x for remux sources. hls.js sizes its buffer byte budget from
		// BANDWIDTH; understating it makes players buffer far more bytes than
		// intended and trip the browser's SourceBuffer quota.
		if src := sourceAverageBps(media.Path, media.DurationMs); src > bps {
			bps = src
		}
	}
	if bps == 0 {
		bps = 8_000_000
	}
	return segs[0].InitPath, bps, nil
}

// sourceAverageBps estimates stream bitrate from the source file's size and
// duration. The whole-file average (video + source audio + container) slightly
// overstates the copy-mode HLS output (audio is transcoded down to AAC), which
// is the safe direction for a BANDWIDTH attribute.
func sourceAverageBps(path string, durationMs int64) int64 {
	if durationMs <= 0 {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size() * 8000 / durationMs
}

// handleRenditionManifest serves per-profile segment playlists at
// /channel/{channelID}/packaged/{rendition}/stream.m3u8.
func (a *app) handleRenditionManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if rt.PlaybackMode != db.PlaybackModePackaged {
		http.NotFound(w, r)
		return
	}
	rendition := r.PathValue("rendition")
	profile, err := db.GetPackageProfile(r.Context(), a.dbConn, rendition)
	if err != nil || profile == nil {
		http.NotFound(w, r)
		return
	}
	a.writePackagedManifest(w, r, channelID, rendition)
}

// codecStringForInit runs ffprobe on initPath (combined with the first segment
// if needed) and returns the HLS CODECS attribute string, e.g.
// "avc1.4d401f,mp4a.40.2". Results are cached by init path for the process
// lifetime since codec params are immutable once packaged.
func (a *app) codecStringForInit(initPath string) string {
	if v, ok := a.codecCache.Load(initPath); ok {
		return v.(string)
	}
	codec := probeCodecString(initPath)
	if codec != "" {
		a.codecCache.Store(initPath, codec)
	}
	return codec
}

func probeCodecString(initPath string) string {
	// Probe the HLS playlist alongside init.mp4 — ffprobe reads the playlist
	// header and enough of the first segment to expose profile and level.
	// Falling back to init.mp4 alone returns level=-99 which is unusable.
	playlist := filepath.Join(filepath.Dir(initPath), "stream.m3u8")
	probe := playlist
	if err := exec.Command("test", "-f", probe).Run(); err != nil {
		probe = initPath
	}
	out, err := exec.Command("ffprobe",
		"-v", "quiet",
		"-show_streams",
		"-select_streams", "v:0",
		"-of", "json",
		probe,
	).Output()
	if err != nil {
		return "avc1.4d401f,mp4a.40.2"
	}
	var result struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			Profile   string `json:"profile"`
			Level     int    `json:"level"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &result); err != nil || len(result.Streams) == 0 {
		return "avc1.4d401f,mp4a.40.2"
	}
	s := result.Streams[0]
	if s.Profile == "" || s.Level <= 0 {
		return "avc1.4d401f,mp4a.40.2"
	}

	if strings.ToLower(s.CodecName) == "hevc" {
		// hvc1 codec string per ISO 14496-15 Annex E.
		// general_profile_compatibility_flags hex (bit 0 = profile 0, bit N = profile N):
		//   Main:     0x01 → "1"
		//   Main 10:  0x06 → "6" (Main + Main 10 bits both set)
		compatHex := map[string]string{
			"Main":    "1",
			"Main 10": "6",
		}
		ch := compatHex[s.Profile]
		if ch == "" {
			ch = "6" // default to Main 10 for HDR content
		}
		return fmt.Sprintf("hvc1.1.%s.L%03d.B0,mp4a.40.2", ch, s.Level)
	}

	// avc1 codec string per ISO 14496-15 §5.3.3.1: profile_idc, constraint_flags, level_idc.
	profileByte := map[string]string{
		"Baseline":             "42",
		"Constrained Baseline": "42",
		"Main":                 "4d",
		"High":                 "64",
	}
	constraintByte := map[string]string{
		"Baseline":             "c0",
		"Constrained Baseline": "c0",
		"Main":                 "40",
		"High":                 "00",
	}
	pid, ok := profileByte[s.Profile]
	if !ok {
		return "avc1.4d401f,mp4a.40.2"
	}
	cc := constraintByte[s.Profile]
	ll := fmt.Sprintf("%02x", s.Level)
	return fmt.Sprintf("avc1.%s%s%s,mp4a.40.2", pid, cc, ll)
}

// parseBitrateString converts a bitrate string like "6000k" or "8M" to bps.
func parseBitrateString(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1000
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1_000_000
		s = s[:len(s)-1]
	case 'g', 'G':
		mult = 1_000_000_000
		s = s[:len(s)-1]
	}
	var v int64
	fmt.Sscan(s, &v)
	return v * mult
}

func languageLabel(bcp47 string) string {
	switch strings.ToLower(bcp47) {
	case "en", "eng":
		return "English"
	case "es", "spa":
		return "Spanish"
	case "fr", "fra", "fre":
		return "French"
	case "de", "deu", "ger":
		return "German"
	case "pt", "por":
		return "Portuguese"
	case "it", "ita":
		return "Italian"
	case "ja", "jpn":
		return "Japanese"
	case "ko", "kor":
		return "Korean"
	case "zh", "zho", "chi":
		return "Chinese"
	case "ru", "rus":
		return "Russian"
	case "ar", "ara":
		return "Arabic"
	case "nl", "nld", "dut":
		return "Dutch"
	case "pl", "pol":
		return "Polish"
	case "sv", "swe":
		return "Swedish"
	case "da", "dan":
		return "Danish"
	case "fi", "fin":
		return "Finnish"
	case "no", "nor", "nob", "nno":
		return "Norwegian"
	case "cs", "ces", "cze":
		return "Czech"
	case "hu", "hun":
		return "Hungarian"
	case "ro", "ron", "rum":
		return "Romanian"
	case "tr", "tur":
		return "Turkish"
	case "und":
		return "Unknown"
	}
	return bcp47
}
