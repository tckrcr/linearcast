package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ondemand"
)

type subtitleSegmentInfo struct {
	MediaStartMs     int64
	DurationMs       int64
	Sequence         int64
	WallClockStartMs int64
}

type subtitleInfo struct {
	MediaID          string
	EntryStartMs     int64
	EntryOffsetMs    int64
	EntryDurationMs  int64
	BaseMediaStartMs int64
	Segments         []subtitleSegmentInfo
}

// handleSubtitlePlaylist serves a live HLS subtitle media playlist for one
// language tag. Packaged channels mirror the video media playlist segment-for-
// segment; each subtitle URI asks handleSubtitleVTT to clip the media-wide
// WebVTT sidecar down to that packaged segment's media-time window.
//
// #EXT-X-PROGRAM-DATE-TIME tags anchor every segment to wall-clock time so
// that hls.js can synchronise subtitle cues with the video playlist, which
// also carries PDT tags.
func (a *app) handleSubtitlePlaylist(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	language := r.PathValue("language")
	if !safeSubtitleRouteToken(language) {
		http.NotFound(w, r)
		return
	}
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if rt.PlaybackMode != db.PlaybackModePackaged {
		http.NotFound(w, r)
		return
	}
	profile := r.PathValue("profile")
	if profile == "" {
		http.NotFound(w, r)
		return
	}

	nowMs := time.Now().UTC().UnixMilli()
	items, err := a.packagedManifestItemsForPlayback(r.Context(), channelID, profile, nowMs)
	if err != nil || len(items) == 0 {
		http.NotFound(w, r)
		return
	}

	targetDuration := int64(1)
	for _, item := range items {
		if sec := ceilSeconds(item.DurationMs); sec > targetDuration {
			targetDuration = sec
		}
	}

	trackExistsByPackageID := make(map[string]bool)
	trackLoadedByPackageID := make(map[string]bool)

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", items[0].Sequence)
	fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", items[0].DiscontinuitySequence)

	var lastSourceKey string
	for _, item := range items {
		if item.SourceKey != lastSourceKey {
			if lastSourceKey != "" {
				b.WriteString("#EXT-X-DISCONTINUITY\n")
			}
			lastSourceKey = item.SourceKey
		}
		pdt := time.UnixMilli(item.WallClockStartMs).UTC().Format(pdtLayout)
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
		fmt.Fprintf(&b, "#EXTINF:%s,\n", formatEXTINF(item.DurationMs))

		packageID := item.Package.ID
		if !trackLoadedByPackageID[packageID] {
			tracks, _ := db.PackageTracksByPackageID(r.Context(), a.dbConn, packageID)
			trackExistsByPackageID[packageID] = selectSubtitleRenditionTrack(tracks, language) != nil
			trackLoadedByPackageID[packageID] = true
		}
		if trackExistsByPackageID[packageID] {
			fmt.Fprintf(&b, "../%s/%s.vtt?start=%d&dur=%d\n", item.Package.ID, language, item.Segment.MediaStartMs, item.DurationMs)
			continue
		}
		// No subtitle for this segment's media — serve empty WebVTT to maintain timing.
		b.WriteString("../empty.vtt\n")
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

// handleSubtitleVTT serves the WebVTT sidecar for a specific package and
// language. The file was extracted from the source media at packaging time.
func (a *app) handleSubtitleVTT(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name") // e.g. "en.vtt"
	if !strings.HasSuffix(name, ".vtt") {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimSuffix(name, ".vtt")
	if !safeSubtitleRouteToken(token) {
		http.NotFound(w, r)
		return
	}

	packageID := r.PathValue("packageID")

	// Try the packaged path first.
	pkg, err := db.MediaPackageByID(r.Context(), a.dbConn, packageID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if pkg != nil && pkg.Status == db.PackageStatusReady && pkg.RenditionProfile == r.PathValue("profile") {
	} else {
		http.NotFound(w, r)
		return
	}

	tracks, err := db.PackageTracksByPackageID(r.Context(), a.dbConn, packageID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	var vttPath string
	if t := selectSubtitleRenditionTrack(tracks, token); t != nil {
		vttPath = *t.Path
	}
	if vttPath == "" || missingPackagedFile(vttPath) {
		http.NotFound(w, r)
		return
	}

	if mediaStartMs, durationMs, ok := subtitleClipWindow(r); ok {
		serveCachedSubtitleSegment(w, vttPath, mediaStartMs, durationMs, mediaStartMs*90)
		return
	} else if hasSubtitleClipQuery(r) {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/vtt")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, vttPath)
}

func hasSubtitleClipQuery(r *http.Request) bool {
	q := r.URL.Query()
	return q.Has("start") || q.Has("dur")
}

func subtitleClipWindow(r *http.Request) (int64, int64, bool) {
	q := r.URL.Query()
	if !q.Has("start") || !q.Has("dur") {
		return 0, 0, false
	}
	mediaStartMs, err := strconv.ParseInt(q.Get("start"), 10, 64)
	if err != nil || mediaStartMs < 0 {
		return 0, 0, false
	}
	durationMs, err := strconv.ParseInt(q.Get("dur"), 10, 64)
	if err != nil || durationMs <= 0 {
		return 0, 0, false
	}
	return mediaStartMs, durationMs, true
}

// subtitleManagerFor returns the live-encoding manager that owns a channel's WebVTT
// renditions: the on-demand manager for on_demand channels. Eager channels serve
// cached sidecars instead and get nil here.
func (a *app) subtitleManagerFor(rt *channelRuntime) *ondemand.Manager {
	if rt == nil {
		return nil
	}
	if rt.PrefillMode == "on_demand" {
		return a.encodings
	}
	return nil
}

// handleOnDemandSubtitleFile serves a channel's live (on-demand) subtitle
// rendition. The master playlist advertises a channel-encoding-independent
// URL so it survives schedule-entry rotation; this handler resolves the channel's
// current live encoding on each request. Two shapes are served:
//
//	{slug}/playlist.m3u8          -> a regenerated, PDT-anchored media playlist
//	                                 for the current encoding, segment URIs pinned
//	                                 to that encoding id.
//	{slug}/{encodingID}/{seg}.vtt -> the WebVTT segment ffmpeg wrote, with an
//	                                 injected X-TIMESTAMP-MAP.
func (a *app) handleOnDemandSubtitleFile(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rest := r.PathValue("rest")
	if !safeSubtitleRouteToken(rest) {
		http.NotFound(w, r)
		return
	}
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	mgr := a.subtitleManagerFor(rt)
	if mgr == nil {
		http.NotFound(w, r)
		return
	}
	slug, rem, ok := strings.Cut(rest, "/")
	if !ok || !isSubtitleSlug(slug) {
		http.NotFound(w, r)
		return
	}
	if rem == "playlist.m3u8" {
		a.writeOnDemandSubtitlePlaylist(w, r, channelID, slug, mgr)
		return
	}
	// Segment: {encodingID}/{seg}.vtt — the encoding id is pinned by the playlist
	// we generated, so it always names the encoding those segments belong to.
	encodingID, file, ok := strings.Cut(rem, "/")
	if !ok || !safeSubtitleRouteToken(encodingID) || !strings.HasSuffix(file, ".vtt") || !safeSubtitleRouteToken(file) {
		http.NotFound(w, r)
		return
	}
	encodingDir, ok := mgr.EncodingDir(channelID, encodingID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/vtt")
	w.Header().Set("Cache-Control", "no-cache")
	serveOnDemandSubtitleSegment(w, filepath.Join(encodingDir, "subs", slug, filepath.FromSlash(file)))
}

// writeOnDemandSubtitlePlaylist regenerates the current channel encoding's WebVTT media
// playlist with #EXT-X-PROGRAM-DATE-TIME on every segment, anchored so subtitle
// output t=0 maps to the same wall clock as the video rendition's segment at the
// encoding's media start. Without this anchoring hls.js cannot place the cues on
// the video timeline and the CC track renders nothing. EXT-X-ENDLIST is dropped
// so the playlist stays live and rolls onto the next encoding at entry boundaries.
func (a *app) writeOnDemandSubtitlePlaylist(w http.ResponseWriter, r *http.Request, channelID, slug string, mgr *ondemand.Manager) {
	nowMs := time.Now().UTC().UnixMilli()
	entries, err := db.ScheduleWindow(r.Context(), a.dbConn, channelID, nowMs, nowMs+manifestAheadMs)
	if err != nil || len(entries) == 0 {
		http.NotFound(w, r)
		return
	}
	entry := entries[0]
	for _, e := range entries {
		if e.StartMs <= nowMs && nowMs < e.StartMs+e.DurationMs {
			entry = e
			break
		}
	}
	encodingID, ok := mgr.EncodingID(channelID, entry.ID)
	if !ok {
		slog.Warn("subtitle playlist: no channel encoding", "channel", channelID, "entry", entry.ID, "entries", len(entries))
		http.NotFound(w, r)
		return
	}
	encodingDir, ok := mgr.EncodingDir(channelID, encodingID)
	if !ok {
		slog.Warn("subtitle playlist: no channel encoding dir", "channel", channelID, "encoding_id", encodingID)
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(filepath.Join(encodingDir, "subs", slug, "playlist.m3u8"))
	if err != nil {
		slog.Warn("subtitle playlist: read failed", "channel", channelID, "encoding_id", encodingID, "slug", slug, "dir", encodingDir, "err", err)
		http.NotFound(w, r)
		return
	}

	type seg struct {
		name  string
		durMs int64
	}
	var segs []seg
	pendDurMs := int64(-1)
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			pendDurMs = parseExtinfMs(line)
		case line != "" && !strings.HasPrefix(line, "#"):
			segs = append(segs, seg{name: line, durMs: pendDurMs})
			pendDurMs = -1
		}
	}

	// Subtitle output t=0 == the encoding's first segment media start; map that to
	// wall clock the same way appendOnDemandManifestItems does for video PDT.
	anchorWallMs := entry.StartMs
	if first := mgr.SegmentsFrom(channelID, entry.ID, 0, 1); len(first) > 0 {
		anchorWallMs = entry.StartMs + (first[0].MediaStartMs - entry.OffsetMs)
	}

	targetDuration := int64(1)
	for _, s := range segs {
		if sec := ceilSeconds(s.durMs); sec > targetDuration {
			targetDuration = sec
		}
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	cumMs := int64(0)
	for _, s := range segs {
		pdt := time.UnixMilli(anchorWallMs + cumMs).UTC().Format(pdtLayout)
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
		fmt.Fprintf(&b, "#EXTINF:%s,\n", formatEXTINF(s.durMs))
		fmt.Fprintf(&b, "%s/%s\n", encodingID, s.name)
		if s.durMs > 0 {
			cumMs += s.durMs
		}
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

// parseExtinfMs extracts the duration in milliseconds from an #EXTINF line.
func parseExtinfMs(line string) int64 {
	rest := strings.TrimPrefix(line, "#EXTINF:")
	if i := strings.IndexByte(rest, ','); i >= 0 {
		rest = rest[:i]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil {
		return 0
	}
	return int64(f*1000 + 0.5)
}

// serveOnDemandSubtitleSegment writes ffmpeg's WebVTT segment with an injected
// X-TIMESTAMP-MAP header. ffmpeg's segment muxer omits it, and without it hls.js
// has no anchor mapping the cue timeline onto the fMP4 video presentation
// timeline, so the CC track registers but no cues render. The channel encoding's video is
// rebased to output t=0 and its WebVTT cues share that output timeline, so
// MPEGTS:0/LOCAL:0 is the correct identity map.
func serveOnDemandSubtitleSegment(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "subtitle not found", http.StatusNotFound)
		return
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.Contains(text, "X-TIMESTAMP-MAP") {
		const header = "WEBVTT"
		if rest, ok := strings.CutPrefix(text, header+"\n"); ok {
			text = header + "\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n" + rest
		} else if rest, ok := strings.CutPrefix(text, header); ok {
			text = header + "\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0" + rest
		}
	}
	_, _ = w.Write([]byte(text))
}

// forcedRenditionSuffix marks a subtitle rendition token as addressing the
// forced-narrative track rather than the plain per-language one. The master
// playlist advertises forced renditions at subs/<lang>-forced/playlist.m3u8 and
// their segments at subs/<packageID>/<lang>-forced.vtt.
const forcedRenditionSuffix = "-forced"

// parseSubtitleRendition splits a subtitle rendition token into its base
// language and whether it addresses the forced rendition.
func parseSubtitleRendition(token string) (language string, forced bool) {
	if lang, ok := strings.CutSuffix(token, forcedRenditionSuffix); ok {
		return lang, true
	}
	return token, false
}

// selectSubtitleRenditionTrack resolves a rendition token to the package track
// that should serve it, or nil when the package has no matching track. Plain
// tokens pick the preferred non-forced track; <lang>-forced tokens pick the
// forced track.
func selectSubtitleRenditionTrack(tracks []db.PackageTrack, token string) *db.PackageTrack {
	lang, forced := parseSubtitleRendition(token)
	if forced {
		return db.ForcedSubtitleTrack(tracks, lang)
	}
	return db.PreferredSubtitleTrack(tracks, lang)
}

func isEnglishSubtitleLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "eng", "en", "english":
		return true
	default:
		return false
	}
}

// isSubtitleSlug reports whether token is a subtitle rendition slug ("s" followed
// by one or more digits, e.g. "s3"), which is also the cache filename stem.
func isSubtitleSlug(token string) bool {
	if len(token) < 2 || token[0] != 's' {
		return false
	}
	for _, r := range token[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func subtitleSegmentInfosFromOndemand(in []ondemand.SubtitleSegmentInfo) []subtitleSegmentInfo {
	out := make([]subtitleSegmentInfo, len(in))
	for i, seg := range in {
		out[i] = subtitleSegmentInfo{
			MediaStartMs:     seg.MediaStartMs,
			DurationMs:       seg.DurationMs,
			Sequence:         seg.Sequence,
			WallClockStartMs: seg.WallClockStartMs,
		}
	}
	return out
}

// writeCachedSubtitlePlaylist emits a live WebVTT media playlist mirroring the
// active on-demand video segments. Matching sequence numbers, durations, and PDT
// let hls.js load the subtitle fragment for the same playback window as video.
func writeCachedSubtitlePlaylist(w http.ResponseWriter, info subtitleInfo) {
	targetDuration := int64(1)
	for _, seg := range info.Segments {
		if sec := ceilSeconds(seg.DurationMs); sec > targetDuration {
			targetDuration = sec
		}
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	if len(info.Segments) > 0 {
		fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", info.Segments[0].Sequence)
	}
	for _, seg := range info.Segments {
		pdt := time.UnixMilli(seg.WallClockStartMs).UTC().Format(pdtLayout)
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
		fmt.Fprintf(&b, "#EXTINF:%s,\n", formatEXTINF(seg.DurationMs))
		fmt.Fprintf(&b, "seg/%d-%d.vtt\n", seg.MediaStartMs, seg.DurationMs)
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

func parseSubtitleSegmentName(name string) (int64, int64, bool) {
	name = strings.TrimSuffix(name, ".vtt")
	start, dur, ok := strings.Cut(name, "-")
	if !ok {
		return 0, 0, false
	}
	mediaStartMs, err := strconv.ParseInt(start, 10, 64)
	if err != nil || mediaStartMs < 0 {
		return 0, 0, false
	}
	durationMs, err := strconv.ParseInt(dur, 10, 64)
	if err != nil || durationMs <= 0 {
		return 0, 0, false
	}
	return mediaStartMs, durationMs, true
}

func subtitleSegmentExists(info subtitleInfo, mediaStartMs, durationMs int64) bool {
	for _, seg := range info.Segments {
		if seg.MediaStartMs == mediaStartMs && seg.DurationMs == durationMs {
			return true
		}
	}
	return false
}

func serveCachedSubtitleSegment(w http.ResponseWriter, path string, mediaStartMs, durationMs, mpegts int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "subtitle not found", http.StatusNotFound)
		return
	}
	body := clipWebVTT(data, mediaStartMs, durationMs, mpegts)
	w.Header().Set("Content-Type", "text/vtt")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

func clipWebVTT(data []byte, mediaStartMs, durationMs, mpegts int64) []byte {
	segmentEndMs := mediaStartMs + durationMs
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	blocks := strings.Split(text, "\n\n")
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	fmt.Fprintf(&b, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n\n", mpegts)
	for _, block := range blocks {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		if len(lines) == 0 {
			continue
		}
		timeLine := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timeLine = i
				break
			}
		}
		if timeLine < 0 {
			continue
		}
		startMs, endMs, settings, ok := parseWebVTTCueTiming(lines[timeLine])
		if !ok || endMs <= mediaStartMs || startMs >= segmentEndMs {
			continue
		}
		clippedStart := startMs
		if clippedStart < mediaStartMs {
			clippedStart = mediaStartMs
		}
		clippedEnd := endMs
		if clippedEnd > segmentEndMs {
			clippedEnd = segmentEndMs
		}
		for _, line := range lines[:timeLine] {
			if strings.TrimSpace(line) != "" {
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
		b.WriteString(formatWebVTTTime(clippedStart - mediaStartMs))
		b.WriteString(" --> ")
		b.WriteString(formatWebVTTTime(clippedEnd - mediaStartMs))
		if settings != "" {
			b.WriteByte(' ')
			b.WriteString(settings)
		}
		b.WriteByte('\n')
		for _, line := range lines[timeLine+1:] {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func parseWebVTTCueTiming(line string) (int64, int64, string, bool) {
	startText, rest, ok := strings.Cut(line, "-->")
	if !ok {
		return 0, 0, "", false
	}
	fields := strings.Fields(strings.TrimSpace(rest))
	if len(fields) == 0 {
		return 0, 0, "", false
	}
	startMs, ok := parseWebVTTTime(strings.TrimSpace(startText))
	if !ok {
		return 0, 0, "", false
	}
	endMs, ok := parseWebVTTTime(fields[0])
	if !ok {
		return 0, 0, "", false
	}
	settings := strings.Join(fields[1:], " ")
	return startMs, endMs, settings, true
}

func parseWebVTTTime(s string) (int64, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	var hours int64
	var minutesText, secondsText string
	if len(parts) == 3 {
		h, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, false
		}
		hours = h
		minutesText = parts[1]
		secondsText = parts[2]
	} else {
		minutesText = parts[0]
		secondsText = parts[1]
	}
	minutes, err := strconv.ParseInt(minutesText, 10, 64)
	if err != nil {
		return 0, false
	}
	secParts := strings.Split(secondsText, ".")
	if len(secParts) != 2 || len(secParts[1]) != 3 {
		return 0, false
	}
	seconds, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	millis, err := strconv.ParseInt(secParts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return ((hours*60+minutes)*60+seconds)*1000 + millis, true
}

func formatWebVTTTime(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	hours := ms / 3_600_000
	ms %= 3_600_000
	minutes := ms / 60_000
	ms %= 60_000
	seconds := ms / 1000
	millis := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

func safeSubtitleRouteToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(token, "/") {
		return false
	}
	// Reject any path component that is "." or "..".
	for _, part := range strings.Split(token, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

// handleEmptySubtitle returns a valid but empty WebVTT document. Used as a
// placeholder for schedule entries that have no subtitle track in the
// requested language, preserving timing continuity in the subtitle playlist.
func (a *app) handleEmptySubtitle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/vtt")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte("WEBVTT\n\n"))
}
