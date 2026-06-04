package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func (a *app) handleManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
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

	pkg, _ := db.ReadyMediaPackage(r.Context(), a.dbConn, currentEntry.MediaID, profile)
	if pkg == nil {
		// No package is ready; fall back to the segment playlist directly so
		// the player sees the 503 "warming up" UX.
		a.writePackagedManifest(w, r, channelID, profile)
		return
	}
	bps := db.PeakSegmentBps(r.Context(), a.dbConn, pkg.ID)
	if bps == 0 {
		// Fallback: use the profile's declared max bitrate if segments can't be stat'd.
		if p, err := db.GetPackageProfile(r.Context(), a.dbConn, profile); err == nil && p != nil && p.Video.VideoMaxBitrate != "" {
			bps = parseBitrateString(p.Video.VideoMaxBitrate)
		}
	}
	codecs := "avc1.4d401f,mp4a.40.2" // safe default for H.264 Main
	if pkg.InitSegmentPath != nil {
		codecs = a.codecStringForInit(*pkg.InitSegmentPath)
	}

	mediaIDs := make([]string, 0, len(entries))
	seen := make(map[string]bool)
	for _, e := range entries {
		if !seen[e.MediaID] {
			seen[e.MediaID] = true
			mediaIDs = append(mediaIDs, e.MediaID)
		}
	}
	subtitleTracks, err := db.SubtitleTracksForMediaIDs(r.Context(), a.dbConn, mediaIDs)
	if err != nil {
		subtitleTracks = nil
	}

	autoEnable, _ := db.GetSubtitleAutoEnable(r.Context(), a.dbConn)
	langPrefs, _ := db.GetSubtitleLanguagePreference(r.Context(), a.dbConn)
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

	res := ""
	if pkg.VideoWidth != nil && pkg.VideoHeight != nil {
		res = fmt.Sprintf(",RESOLUTION=%dx%d", *pkg.VideoWidth, *pkg.VideoHeight)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d%s,CODECS=%q%s\n",
		bps, res, codecs, subsAttr)
	fmt.Fprintf(&b, "/channel/%s/%s/%s/stream.m3u8\n", channelID, packagedPath, profile)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

// handleRenditionManifest serves per-profile segment playlists at
// /channel/{channelID}/packaged/{rendition}/stream.m3u8.
func (a *app) handleRenditionManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	if a.lookupChannelOr404(r.Context(), w, channelID) == nil {
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
			Profile string `json:"profile"`
			Level   int    `json:"level"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &result); err != nil || len(result.Streams) == 0 {
		return "avc1.4d401f,mp4a.40.2"
	}
	s := result.Streams[0]
	if s.Profile == "" || s.Level <= 0 {
		return "avc1.4d401f,mp4a.40.2"
	}
	// profile_idc, constraint_flags, level_idc per ISO 14496-15 §5.3.3.1
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
