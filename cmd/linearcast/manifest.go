package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/codec"
	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packageprofile"
	"github.com/tckrcr/linearcast/internal/packager"
	"github.com/tckrcr/linearcast/internal/scheduler"
	"github.com/tckrcr/linearcast/internal/subtitlepolicy"
)

// redirectRelative issues a 302 whose Location is a path relative to the
// request URL. Unlike http.Redirect, it does not resolve location against the
// backend request path: that resolution would drop any reverse-proxy mount
// prefix (e.g. /hls) the client used, because the backend never sees that
// prefix. Emitting a bare relative reference lets the browser resolve it
// against its original request URL, preserving the prefix.
func redirectRelative(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

func (a *app) handleManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if a.encodings != nil {
		a.encodings.Touch(rt.ID)
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

	burnActive := a.encodings != nil && a.encodings.BurnSubtitleLanguage(channelID) != ""
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
		if rt.PrefillMode == "eager" {
			// No package is ready; fall back to the segment playlist directly so
			// the player sees the 503 "warming up" UX.
			a.writePackagedManifest(w, r, channelID, profile)
			return
		}
		readyCoverageMs := readyCoverageMsForMasterGate()
		warmDeadline := time.Now().Add(15 * time.Second)
		var initPath string
		var bps int64
		var subEntries []subtitleEntry
		var err error
		for {
			initPath, bps, subEntries, err = a.ensureOnDemandMasterReady(r.Context(), channelID, profile, *currentEntry, nowMs, readyCoverageMs)
			if err == nil || !strings.Contains(err.Error(), "warming") || time.Now().After(warmDeadline) {
				break
			}
			select {
			case <-r.Context().Done():
				err = r.Context().Err()
			case <-time.After(500 * time.Millisecond):
			}
			if r.Context().Err() != nil {
				break
			}
		}
		if err != nil {
			if errors.Is(err, ondemand.ErrAtCapacity) {
				metrics.OnDemandAtCapacity503Total.Inc()
				w.Header().Set("Retry-After", "2")
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			if retryAfter, ok := ondemand.RetryAfterSeconds(err, time.Now().UTC().UnixMilli()); ok {
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
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
		manifestSentAt := time.Now().UnixMilli()
		a.writeMasterManifest(r.Context(), w, channelID, []masterVariant{{
			Profile:    profile,
			BPS:        bps,
			Res:        mediaResolution(r.Context(), a.dbConn, currentEntry.MediaID),
			Codecs:     codecs,
			VideoRange: hdrVideoRange(r.Context(), a.dbConn, currentEntry.MediaID, profile),
		}}, entries, subEntries)
		slog.Info("ondemand first manifest response",
			"channel_id", channelID,
			"entry_id", currentEntry.ID,
			"manifest_sent_at_ms", manifestSentAt,
			"total_elapsed_ms", manifestSentAt-nowMs,
		)
		return
	}
	a.writeMasterManifest(r.Context(), w, channelID, variants, entries, nil)
}

func readyCoverageMsForMasterGate() int64 {
	return onDemandReadyCoverageMs
}

// subtitleEntry describes one WebVTT track to include in the master playlist for
// an on-demand channel encoding. EncodingID points at the encoding directory and
// Slug maps to the subtitle rendition stem (s{streamIndex}).
type subtitleEntry struct {
	EncodingID string
	Lang       string
	Name       string
	Slug       string
	Default    bool
	Forced     bool
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
		profileBPS = packageprofile.ParseBitrate(p.Video.VideoMaxBitrate)
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
// Only HDR-preserving profiles (tagged "hdr": the HEVC copy rung and the
// HDR-preserving HEVC transcode profiles) carry HDR through to the segments.
// SDR transcoded rungs must not advertise VIDEO-RANGE even when the source is
// HDR.
func hdrVideoRange(ctx context.Context, conn *sql.DB, mediaID string, profileName string) string {
	p, err := db.GetPackageProfile(ctx, conn, profileName)
	if err != nil || p == nil {
		return ""
	}
	hdrProfile := false
	for _, t := range p.Tags {
		if strings.EqualFold(t, "hdr") {
			hdrProfile = true
			break
		}
	}
	if !hdrProfile {
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

func mediaResolution(ctx context.Context, conn *sql.DB, mediaID string) string {
	m, err := db.MediaByID(ctx, conn, mediaID)
	if err != nil || m == nil || m.VideoWidth <= 0 || m.VideoHeight <= 0 {
		return ""
	}
	return fmt.Sprintf(",RESOLUTION=%dx%d", m.VideoWidth, m.VideoHeight)
}

func subtitleLangOrUnd(t db.PackageTrack) string {
	lang := strings.TrimSpace(t.Language)
	if lang == "" {
		return "und"
	}
	return lang
}

func (a *app) manifestSubtitleProfile(ctx context.Context, profileName string) packageprofile.Profile {
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return packageprofile.Profile{}
	}
	if a != nil && a.dbConn != nil {
		if p, err := db.GetPackageProfile(ctx, a.dbConn, profileName); err == nil && p != nil {
			return *p
		}
	}
	return packageprofile.Profile{}
}

func (a *app) writeMasterManifest(ctx context.Context, w http.ResponseWriter, channelID string, variants []masterVariant, entries []db.ScheduleEntry, subEntries []subtitleEntry) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")

	hasSubs := false
	if subEntries != nil {
		// On-demand: subtitle renditions live under the active encoding directory.
		// DEFAULT/AUTOSELECT come from the entry itself (ensureOnDemandMasterReady
		// picks at most one non-forced default), while forced renditions are
		// advertised as spec-level auto-selected forced subtitles.
		hasSubs = len(subEntries) > 0
		subtitleProfile := ""
		if len(variants) > 0 {
			subtitleProfile = variants[0].Profile
		}
		for _, se := range subEntries {
			isDefault := "NO"
			if se.Default && !se.Forced {
				isDefault = "YES"
			}
			autoselect := "YES"
			forcedAttr := ""
			if se.Forced {
				forcedAttr = ",FORCED=YES"
			}
			// Stable, channel-encoding-independent URL: hls.js loads the master once and
			// then polls this playlist like a live media playlist. The handler
			// re-resolves the channel's current on-demand encoding on every poll,
			// so the URL survives schedule-entry/encoding rotation (an encoding ID in
			// the path would 404 the moment playback advanced to the next entry).
			fmt.Fprintf(&b,
				"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=%s%s,URI=\"%s/%s/%s/%s/playlist.m3u8\"\n",
				se.Name, se.Lang, isDefault, autoselect, forcedAttr, streamPath, subtitleProfile, onDemandSubtitlePath, se.Slug)
		}
	} else {
		// Packaged: query subtitle tracks from the DB.
		mediaIDs := make([]string, 0, len(entries))
		seen := make(map[string]bool)
		for _, e := range entries {
			if !seen[e.MediaID] {
				seen[e.MediaID] = true
				mediaIDs = append(mediaIDs, e.MediaID)
			}
		}
		subtitleProfile := ""
		if len(variants) > 0 {
			subtitleProfile = variants[0].Profile
		}
		plainTracks, err := db.PackageSubtitleTracksForMediaIDs(ctx, a.dbConn, mediaIDs, subtitleProfile)
		if err != nil {
			plainTracks = nil
		}
		forcedTracks, err := db.ForcedPackageSubtitleTracksForMediaIDs(ctx, a.dbConn, mediaIDs, subtitleProfile)
		if err != nil {
			forcedTracks = nil
		}

		// A forced track this profile bakes into the video must not also be
		// advertised as a soft forced rendition, or the player shows it twice.
		prof := a.manifestSubtitleProfile(ctx, subtitleProfile)
		forcedTracks = slices.DeleteFunc(forcedTracks, func(t db.PackageTrack) bool {
			return subtitlepolicy.BurnsForcedLanguage(prof, subtitleLangOrUnd(t))
		})

		hasSubs = len(plainTracks) > 0 || len(forcedTracks) > 0
		if hasSubs {
			autoEnable, _ := db.GetSubtitleAutoEnable(ctx, a.dbConn)
			langPrefs, _ := db.GetSubtitleLanguagePreference(ctx, a.dbConn)
			topLang := ""
			if autoEnable && len(langPrefs) > 0 {
				topLang = strings.ToLower(langPrefs[0])
			}
			for _, t := range plainTracks {
				lang := subtitleLangOrUnd(t)
				isDefault := "NO"
				if topLang != "" && strings.ToLower(lang) == topLang {
					isDefault = "YES"
				}
				fmt.Fprintf(&b,
					"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=%s,URI=\"%s/%s/subs/%s/playlist.m3u8\"\n",
					languageLabel(lang), lang, isDefault, isDefault, streamPath, subtitleProfile, lang)
			}
			// Forced renditions are auto-selected by the player when the audio
			// language matches, independent of the CC toggle, so they carry
			// FORCED=YES,AUTOSELECT=YES and are never DEFAULT. The URI slug adds a
			// -forced suffix (handleSubtitlePlaylist parses it back).
			for _, t := range forcedTracks {
				lang := subtitleLangOrUnd(t)
				fmt.Fprintf(&b,
					"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=%q,LANGUAGE=%q,DEFAULT=NO,AUTOSELECT=YES,FORCED=YES,URI=\"%s/%s/subs/%s-forced/playlist.m3u8\"\n",
					languageLabel(lang)+" (Forced)", lang, streamPath, subtitleProfile, lang)
			}
		}
	}

	subsAttr := ""
	if hasSubs {
		subsAttr = ",SUBTITLES=\"subs\""
	}

	for _, v := range variants {
		extraAttr := subsAttr
		codecs := v.Codecs
		if hasSubs {
			codecs = codecsWithWebVTT(codecs)
		}
		if v.VideoRange != "" {
			extraAttr += ",VIDEO-RANGE=" + v.VideoRange
		}
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d%s,CODECS=%q%s\n",
			v.BPS, v.Res, codecs, extraAttr)
		fmt.Fprintf(&b, "%s/%s/stream.m3u8\n", streamPath, v.Profile)
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

func codecsWithWebVTT(codecs string) string {
	codecs = strings.TrimSpace(codecs)
	if codecs == "" {
		return "wvtt"
	}
	for _, c := range strings.Split(codecs, ",") {
		if strings.EqualFold(strings.TrimSpace(c), "wvtt") {
			return codecs
		}
	}
	return codecs + ",wvtt"
}

func currentSubtitleEntryID(entries []db.ScheduleEntry) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[0].ID
}

// subtitleStreams returns all source subtitle streams, probed once per media and
// cached so on-demand encoding spawn paths request stable subtitle options.
func (a *app) subtitleStreams(ctx context.Context, mediaID, mediaPath string) []packager.SubtitleStreamInfo {
	if v, ok := a.subtitleStreamCache.Load(mediaID); ok {
		return v.([]packager.SubtitleStreamInfo)
	}
	infos, _ := packager.ProbeSubtitleStreams(ctx, mediaPath)
	a.subtitleStreamCache.Store(mediaID, infos)
	return infos
}

// englishSubtitleStreams returns the media's non-bitmap English subtitle streams.
// Both on-demand channel-encoding spawn paths — the master-ready gate and the
// steady-state rendition manifest — use this so they request the same
// SubtitleStreamIndexes. If they disagree, reserveEncoding treats the option
// mismatch as a config change and tears down / respawns the encoding on every
// video poll, dropping the subtitle outputs.
func (a *app) englishSubtitleStreams(ctx context.Context, mediaID, mediaPath string) []packager.SubtitleStreamInfo {
	infos := a.subtitleStreams(ctx, mediaID, mediaPath)
	out := make([]packager.SubtitleStreamInfo, 0, len(infos))
	for _, si := range infos {
		if si.IsBitmap || !isEnglishSubtitleLanguage(si.Language) {
			continue
		}
		out = append(out, si)
	}
	return out
}

// subtitleStreamIndexesOf extracts the absolute source stream indexes, preserving
// order so both spawn paths produce identical slices for sameIntSlice.
func subtitleStreamIndexesOf(infos []packager.SubtitleStreamInfo) []int {
	idx := make([]int, 0, len(infos))
	for _, si := range infos {
		idx = append(idx, si.Index)
	}
	return idx
}

// subtitleEntriesForMedia builds the advertisable WebVTT renditions for a media's
// English text subtitle streams, with EncodingID set to encodingID. Used by the
// on-demand master path to advertise slugs, names, and the single default
// rendition. Returns nil when the media has no English text subs.
func (a *app) subtitleEntriesForMedia(ctx context.Context, mediaID, mediaPath, encodingID string) []subtitleEntry {
	subInfos := a.englishSubtitleStreams(ctx, mediaID, mediaPath)
	if len(subInfos) == 0 {
		return nil
	}
	autoEnable, _ := db.GetSubtitleAutoEnable(ctx, a.dbConn)
	topLang := ""
	if autoEnable {
		topLang = "eng"
	}
	subEntries := make([]subtitleEntry, 0, len(subInfos))
	defaultAssigned := false
	for _, si := range subInfos {
		name := languageLabel(si.Language)
		if si.Title != "" && !strings.EqualFold(si.Title, name) {
			name += " " + si.Title
		}
		// Mark exactly one non-forced track in the preferred language as the default
		// rendition; a forced track only carries foreign-dialogue cues and must never
		// be auto-selected.
		isDefault := false
		if !si.Forced && !defaultAssigned && topLang != "" && strings.ToLower(si.Language) == topLang {
			isDefault = true
			defaultAssigned = true
		}
		subEntries = append(subEntries, subtitleEntry{
			EncodingID: encodingID,
			Lang:       si.Language,
			Name:       name,
			Slug:       layout.SubtitleSlug(si.Index),
			Forced:     si.Forced,
			Default:    isDefault,
		})
	}
	return subEntries
}

// ensureOnDemandEncoding resolves the package profile, media, and subtitle/burn
// options for a schedule entry, then ensures a channel encoding is running for it
// seeked to startWallMs. It returns the resolved media/profile, the advertisable
// subtitle renditions (with their encoding id filled in), and the encoding id.
//
// EncodingOptions are built here so encoding reuse stays consistent: reserveEncoding
// restarts an encoding whenever the subtitle/burn options change, so every request
// for the same entry must produce identical options or it throws away the warm
// buffer.
func (a *app) ensureOnDemandEncoding(ctx context.Context, channelID, profile string, entry db.ScheduleEntry, startWallMs int64, traceID string) (*db.Media, *packageprofile.Profile, []subtitleEntry, string, error) {
	if a.encodings == nil {
		return nil, nil, nil, "", fmt.Errorf("on-demand channel encodings unavailable")
	}
	p, err := db.GetPackageProfile(ctx, a.dbConn, profile)
	if err != nil {
		slog.Warn("ondemand precheck failed: profile lookup error",
			"channel_id", channelID, "entry_id", entry.ID, "profile", profile, "err", err)
		return nil, nil, nil, "", fmt.Errorf("package profile %s: %w", profile, err)
	}
	if p == nil {
		// A channel can name a profile that no longer exists (renamed/deleted
		// built-in, disabled custom). This dead-ends the encoder before any
		// encoding is reserved, so without this line the failure is invisible —
		// exactly how a renamed default profile silently broke a live channel.
		slog.Warn("ondemand precheck failed: package profile not found",
			"channel_id", channelID, "entry_id", entry.ID, "profile", profile)
		return nil, nil, nil, "", fmt.Errorf("package profile %s not found", profile)
	}
	media, err := db.MediaByID(ctx, a.dbConn, entry.MediaID)
	if err != nil {
		slog.Warn("ondemand precheck failed: media lookup error",
			"channel_id", channelID, "entry_id", entry.ID, "media_id", entry.MediaID, "err", err)
		return nil, nil, nil, "", fmt.Errorf("media %s: %w", entry.MediaID, err)
	}
	if media == nil {
		slog.Warn("ondemand precheck failed: media not found",
			"channel_id", channelID, "entry_id", entry.ID, "media_id", entry.MediaID)
		return nil, nil, nil, "", fmt.Errorf("media %s not found", entry.MediaID)
	}

	// Advertise the English text subtitle streams this channel encoding will mux
	// alongside video/audio. Shared (cached) with the rendition path so both request
	// the same indexes — see englishSubtitleStreams.
	subtitleStreamIndexes := subtitleStreamIndexesOf(a.englishSubtitleStreams(ctx, media.ID, media.Path))

	opts := ondemand.EncodingOptions{
		BurnSubtitleStreamIndex: a.burnSubtitleStreamIndexForMedia(ctx, channelID, media.ID, *p),
		SubtitleStreamIndexes:   subtitleStreamIndexes,
		StartWallMs:             startWallMs,
		TraceID:                 traceID,
	}
	if err := a.encodings.EnsureEncodingWithOptions(ctx, channelID, entry, media.Path, *p, scheduler.TargetSegmentMs, opts); err != nil {
		slog.Warn("ondemand channel encoding spawn failed",
			"trace_id", traceID, "channel_id", channelID, "entry_id", entry.ID,
			"media_id", entry.MediaID, "err", err)
		return nil, nil, nil, "", err
	}
	encodingID, ok := a.encodings.EncodingID(channelID, entry.ID)
	if !ok {
		slog.Warn("ondemand channel encoding missing after spawn",
			"trace_id", traceID, "channel_id", channelID, "entry_id", entry.ID, "media_id", entry.MediaID)
		return nil, nil, nil, "", fmt.Errorf("on-demand channel encoding not available after spawn for entry %s", entry.ID)
	}
	subEntries := a.subtitleEntriesForMedia(ctx, media.ID, media.Path, encodingID)
	return media, p, subEntries, encodingID, nil
}

// ensureOnDemandMasterReady spawns/reuses the live encoding for entry and blocks
// until readyCoverageMs of playable coverage is buffered ahead of the serve
// position before returning a playable master. Callers pass the coverage cushion
// explicitly so startup policy stays outside the low-level encoding readiness check.
func (a *app) ensureOnDemandMasterReady(ctx context.Context, channelID, profile string, entry db.ScheduleEntry, nowMs int64, readyCoverageMs int64) (string, int64, []subtitleEntry, error) {
	if a.encodings == nil {
		return "", 0, nil, fmt.Errorf("on-demand channel encodings unavailable")
	}
	playbackLagMs, warmupMs, err := a.onDemandTiming()
	if err != nil {
		return "", 0, nil, err
	}
	playbackWallMs := nowMs - playbackLagMs
	if playbackWallMs < entry.StartMs {
		playbackWallMs = entry.StartMs
	}
	traceID := make([]byte, 6)
	rand.Read(traceID)
	tid := hex.EncodeToString(traceID)
	slog.Info("ondemand ready-gate begin",
		"trace_id", tid,
		"channel_id", channelID,
		"entry_id", entry.ID,
		"media_id", entry.MediaID,
		"profile", profile,
		"ready_coverage_ms", readyCoverageMs,
	)
	media, p, subEntries, encodingID, err := a.ensureOnDemandEncoding(ctx, channelID, profile, entry, playbackWallMs, tid)
	if err != nil {
		return "", 0, nil, err
	}
	slog.Info("ondemand encoding ready",
		"trace_id", tid, "channel_id", channelID, "entry_id", entry.ID, "encoding_id", encodingID)
	servePosWallMs := playbackWallMs - warmupMs
	if servePosWallMs < entry.StartMs {
		servePosWallMs = entry.StartMs
	}
	mediaPosMs := entry.OffsetMs + (servePosWallMs - entry.StartMs)
	// Gate on actual playable coverage summed from the served segments' real
	// durations, not a nominal segment count. Copy mode (and any future transport
	// duration) produces irregular segment lengths, so "N segments" is not a
	// reliable proxy for "X ms buffered": one long GOP can satisfy the cushion
	// alone, while several short segments may not. Near the entry's end the
	// coverage remaining may be less than the target; accept what is there so a
	// program's tail still serves.
	needCoverageMs := readyCoverageMs
	if remainingMs := entry.OffsetMs + entry.DurationMs - mediaPosMs; remainingMs < needCoverageMs {
		needCoverageMs = remainingMs
	}
	if needCoverageMs < 1 {
		needCoverageMs = 1
	}
	segs := a.encodings.SegmentsFrom(channelID, entry.ID, mediaPosMs, packagedManifestLimit)
	var coverageMs int64
	for _, seg := range segs {
		coverageMs += seg.DurationMs
		if coverageMs >= needCoverageMs {
			break
		}
	}
	if coverageMs < needCoverageMs {
		return "", 0, nil, fmt.Errorf("on-demand channel encoding warming media=%s profile=%s", entry.MediaID, profile)
	}
	bps := packageprofile.ParseBitrate(p.Video.VideoMaxBitrate)
	if p.Video.Mode == packageprofile.VideoModeCopy {
		if src := sourceAverageBps(media.Path, media.DurationMs); src > bps {
			bps = src
		}
	}
	if bps == 0 {
		bps = 8_000_000
	}
	if spawnedAt, firstSegAt, ok := a.encodings.EncodingTiming(channelID, entry.ID); ok {
		now := time.Now().UnixMilli()
		slog.Info("ondemand ready-gate complete",
			"trace_id", tid,
			"channel_id", channelID,
			"entry_id", entry.ID,
			"spawned_at_ms", spawnedAt,
			"first_segment_at_ms", firstSegAt,
			"ready_at_ms", now,
			"spawn_to_ready_ms", now-spawnedAt,
			"spawn_to_segment_ms", firstSegAt-spawnedAt,
			"segment_to_ready_ms", now-firstSegAt,
			"served_media_pos_ms", mediaPosMs,
			"ready_coverage_ms", readyCoverageMs,
			"need_coverage_ms", needCoverageMs,
			"served_coverage_ms", coverageMs,
		)
	}
	return segs[0].InitPath, bps, subEntries, nil
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
// /channels/{channelID}/streams/{profile}/stream.m3u8.
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
	profileName := r.PathValue("profile")
	profile, err := db.GetPackageProfile(r.Context(), a.dbConn, profileName)
	if err != nil || profile == nil {
		http.NotFound(w, r)
		return
	}
	a.writePackagedManifest(w, r, channelID, profileName)
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
	playlist := layout.PlaylistPath(filepath.Dir(initPath))
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
