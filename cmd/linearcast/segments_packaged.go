package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

const pdtLayout = "2006-01-02T15:04:05.000Z"
const onDemandPlaybackLagMs int64 = 18_000

type packagedManifestItem struct {
	Package               db.MediaPackage
	Segment               db.PackagedSegment
	SourceKey             string
	InitURI               string
	SegmentURI            string
	DurationMs            int64
	Sequence              int64
	DiscontinuitySequence int64
	WallClockStartMs      int64 // wall-clock time when this segment begins
	ProgramDateTimeAlways bool
}

type manifestItemOptions struct {
	AllowOnDemandSessions bool
}

func (a *app) handlePackagedManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	rt := a.lookupChannelOr404(r.Context(), w, channelID)
	if rt == nil {
		return
	}
	if rt.PlaybackMode != db.PlaybackModePackaged {
		http.NotFound(w, r)
		return
	}
	profile := a.packagedProfile
	if rt := a.channel(channelID); rt != nil && rt.RequiredPackageProfile != "" {
		profile = rt.RequiredPackageProfile
	}
	a.writePackagedManifest(w, r, channelID, profile)
}

func (a *app) writePackagedManifest(w http.ResponseWriter, r *http.Request, channelID, profile string) {
	if a.sessions != nil {
		a.sessions.Touch(channelID)
	}
	started := time.Now()
	result := "ready"
	defer func() {
		metrics.ManifestGenerationDuration.WithLabelValues(profile, result).Observe(time.Since(started).Seconds())
	}()
	nowMs := time.Now().UTC().UnixMilli()
	items, err := a.packagedManifestItemsForPlayback(r.Context(), channelID, profile, nowMs)
	if err != nil {
		result = metrics.ReasonLabel(err.Error())
		if errors.Is(err, ondemand.ErrAtCapacity) {
			metrics.OnDemandAtCapacity503Total.Inc()
			w.Header().Set("Retry-After", "2")
		} else if strings.Contains(err.Error(), "warming") {
			metrics.OnDemandWarming503Total.Inc()
			w.Header().Set("Retry-After", "2")
		}
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if len(items) == 0 {
		result = "empty"
		http.NotFound(w, r)
		return
	}

	targetDuration := int64(1)
	for _, item := range items {
		if sec := ceilSeconds(item.DurationMs); sec > targetDuration {
			targetDuration = sec
		}
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", items[0].Sequence)
	fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", items[0].DiscontinuitySequence)
	var lastSourceKey string
	for _, item := range items {
		if item.SourceKey != lastSourceKey {
			if lastSourceKey != "" {
				b.WriteString("#EXT-X-DISCONTINUITY\n")
			}
			pdt := time.UnixMilli(item.WallClockStartMs).UTC().Format(pdtLayout)
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", item.InitURI)
			lastSourceKey = item.SourceKey
		} else if item.ProgramDateTimeAlways {
			pdt := time.UnixMilli(item.WallClockStartMs).UTC().Format(pdtLayout)
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
		}
		fmt.Fprintf(&b, "#EXTINF:%s,\n", formatEXTINF(item.DurationMs))
		b.WriteString(item.SegmentURI)
		b.WriteByte('\n')
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
	metrics.ManifestSegmentsListed.Observe(float64(len(items)))
}

func (a *app) packagedManifestItems(ctx context.Context, channelID, profile string, nowMs int64) ([]packagedManifestItem, error) {
	return a.packagedManifestItemsWithOptions(ctx, channelID, profile, nowMs, manifestItemOptions{})
}

func (a *app) packagedManifestItemsForPlayback(ctx context.Context, channelID, profile string, nowMs int64) ([]packagedManifestItem, error) {
	return a.packagedManifestItemsWithOptions(ctx, channelID, profile, nowMs, manifestItemOptions{AllowOnDemandSessions: true})
}

func (a *app) packagedManifestItemsWithOptions(ctx context.Context, channelID, profile string, nowMs int64, opts manifestItemOptions) ([]packagedManifestItem, error) {
	entries, err := db.ScheduleWindow(ctx, a.dbConn, channelID, nowMs, nowMs+manifestAheadMs)
	if err != nil {
		return nil, fmt.Errorf("schedule window: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	var items []packagedManifestItem
	wallMs := nowMs
	deadlineMs := nowMs + manifestAheadMs
	recordedCurrentEntry := false
	rt := a.channel(channelID)
	for wallMs < deadlineMs && len(items) < packagedManifestLimit {
		entry := db.FindScheduleEntry(entries, wallMs)
		if entry == nil {
			break
		}
		pkg, err := db.ReadyMediaPackage(ctx, a.dbConn, entry.MediaID, profile)
		if err != nil {
			return nil, fmt.Errorf("ready package media=%s profile=%s: %w", entry.MediaID, profile, err)
		}
		if rt != nil && rt.PrefillMode == "on_demand" && a.sessions != nil && a.sessions.BurnSubtitleLanguage(channelID) != "" {
			pkg = nil
		}
		if !recordedCurrentEntry && wallMs == nowMs {
			if _, err := db.RecordPlayHistory(ctx, a.dbConn, *entry); err != nil {
				log.Printf("record play history channel=%s entry=%s: %v", channelID, entry.ID, err)
			}
			recordedCurrentEntry = true
		}
		if pkg == nil {
			if opts.AllowOnDemandSessions && rt != nil && rt.PrefillMode == "on_demand" {
				progressed, err := a.appendOnDemandManifestItems(ctx, &items, channelID, profile, *entry, wallMs, deadlineMs)
				if err != nil {
					// A later entry failing to admit a session (at capacity, spawn
					// error) must not 503 a manifest that already has playable
					// segments — truncate and let the next refresh extend it.
					if len(items) > 0 {
						break
					}
					return nil, err
				}
				if !progressed {
					if len(items) == 0 {
						return nil, fmt.Errorf("on-demand session warming media=%s profile=%s", entry.MediaID, profile)
					}
					break
				}
				last := items[len(items)-1]
				wallMs = last.WallClockStartMs + last.DurationMs
				if wallMs >= entry.StartMs+entry.DurationMs {
					continue
				}
				break
			}
			if len(items) == 0 {
				return nil, fmt.Errorf("package not ready media=%s profile=%s", entry.MediaID, profile)
			}
			break
		}
		mediaPosMs := entry.OffsetMs + (wallMs - entry.StartMs)
		// Packaged segment boundaries are media-relative and may not be exactly
		// 6000 ms. The schedule stays wall-clock based; this loop advances wall
		// time by each resolved packaged segment's exact duration.
		segs, err := db.PackagedSegmentsFrom(ctx, a.dbConn, pkg.ID, mediaPosMs, packagedManifestLimit-len(items))
		if err != nil {
			return nil, fmt.Errorf("packaged segments package=%s: %w", pkg.ID, err)
		}
		if len(segs) == 0 {
			break
		}
		progressed := false
		entryMediaEnd := entry.OffsetMs + entry.DurationMs
		onDemandChannel := rt != nil && rt.PrefillMode == "on_demand"
		sourceKey := pkg.ID
		sequenceBase := int64(0)
		discontinuitySequence := int64(0)
		priorSegments := int64(0)
		if onDemandChannel {
			sourceKey = entry.ID + "/" + pkg.ID
			discontinuitySequence, err = a.onDemandDiscontinuitySequence(ctx, channelID, entry.StartMs)
			if err != nil {
				return nil, err
			}
			if a.sessions != nil {
				discontinuitySequence += a.sessions.ExtraDiscontinuities(channelID)
			}
		} else {
			sequenceBase, err = a.packagedManifestSequenceBase(ctx, channelID, profile, entry.StartMs)
			if err != nil {
				return nil, err
			}
			discontinuitySequence, err = a.packagedManifestDiscontinuitySequence(ctx, channelID, profile, entry.StartMs)
			if err != nil {
				return nil, err
			}
			priorSegments, err = a.packagedManifestEntrySegmentOffset(ctx, pkg.ID, entry.OffsetMs, entryMediaEnd, segs[0].SegmentNumber)
			if err != nil {
				return nil, err
			}
		}
		emittedInEntry := int64(0)
		lastOnDemandSeq := int64(-1)
		for _, seg := range segs {
			if len(items) >= packagedManifestLimit || wallMs >= deadlineMs {
				break
			}
			if seg.MediaStartMs >= entryMediaEnd {
				break
			}
			sequence := sequenceBase + priorSegments + emittedInEntry
			if onDemandChannel {
				sequence = onDemandMediaSequence(*entry, seg.MediaStartMs)
				if lastOnDemandSeq >= 0 && sequence <= lastOnDemandSeq {
					sequence = lastOnDemandSeq + 1
				}
				lastOnDemandSeq = sequence
			}
			items = append(items, packagedManifestItem{
				Package:               *pkg,
				Segment:               seg,
				SourceKey:             sourceKey,
				InitURI:               fmt.Sprintf("/channel/%s/%s/init/%s/init.mp4", channelID, packagedPath, pkg.ID),
				SegmentURI:            fmt.Sprintf("/channel/%s/%s/segments/%s/%d.m4s", channelID, packagedPath, pkg.ID, seg.SegmentNumber),
				DurationMs:            seg.DurationMs,
				Sequence:              sequence,
				DiscontinuitySequence: discontinuitySequence,
				WallClockStartMs:      wallMs,
			})
			emittedInEntry++
			nextWallMs := entry.StartMs + (seg.MediaStartMs + seg.DurationMs - entry.OffsetMs)
			if nextWallMs <= wallMs {
				nextWallMs = wallMs + seg.DurationMs
			}
			wallMs = nextWallMs
			progressed = true
			if wallMs >= entry.StartMs+entry.DurationMs {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return items, nil
}

func (a *app) appendOnDemandManifestItems(ctx context.Context, items *[]packagedManifestItem, channelID, profile string, entry db.ScheduleEntry, wallMs, deadlineMs int64) (bool, error) {
	if a.sessions == nil {
		return false, fmt.Errorf("on-demand sessions unavailable")
	}
	p, err := db.GetPackageProfile(ctx, a.dbConn, profile)
	if err != nil {
		return false, fmt.Errorf("package profile %s: %w", profile, err)
	}
	if p == nil {
		return false, fmt.Errorf("package profile %s not found", profile)
	}
	media, err := db.MediaByID(ctx, a.dbConn, entry.MediaID)
	if err != nil {
		return false, fmt.Errorf("media %s: %w", entry.MediaID, err)
	}
	if media == nil {
		return false, fmt.Errorf("media %s not found", entry.MediaID)
	}
	opts := ondemand.SessionOptions{BurnSubtitleStreamIndex: a.burnSubtitleStreamIndexForMedia(ctx, channelID, media.ID)}
	if err := a.sessions.EnsureSessionWithOptions(ctx, channelID, entry, media.Path, *p, scheduler.TargetSegmentMs, opts); err != nil {
		if errors.Is(err, ondemand.ErrAtCapacity) {
			return false, err
		}
		return false, fmt.Errorf("ensure on-demand session channel=%s entry=%s media=%s profile=%s: %w", channelID, entry.ID, entry.MediaID, profile, err)
	}
	playbackWallMs := wallMs - onDemandPlaybackLagMs
	if playbackWallMs < entry.StartMs {
		playbackWallMs = entry.StartMs
	}
	mediaPosMs := entry.OffsetMs + (playbackWallMs - entry.StartMs)
	segs := a.sessions.SegmentsFrom(channelID, entry.ID, mediaPosMs, packagedManifestLimit-len(*items))
	if len(segs) == 0 {
		return false, nil
	}
	discSeq, err := a.onDemandDiscontinuitySequence(ctx, channelID, entry.StartMs)
	if err != nil {
		return false, err
	}
	discSeq += a.sessions.ExtraDiscontinuities(channelID)
	entryMediaEnd := entry.OffsetMs + entry.DurationMs
	emitted := false
	for _, seg := range segs {
		if len(*items) >= packagedManifestLimit || wallMs >= deadlineMs {
			break
		}
		if seg.MediaStartMs >= entryMediaEnd {
			break
		}
		// Number segments by the manager-assigned base sequence plus the
		// segment's ordinal index. BaseSeq is anchored to the wall-clock grid and
		// advanced past any prior session for the entry, so numbering stays
		// gap-free across copy-mode's irregular durations and monotonic across
		// session restarts — no media sequence number ever maps to two different
		// segments. For a first session with uniform 6s segments this matches
		// onDemandMediaSequence(entry, seg.MediaStartMs).
		seq := seg.BaseSeq + seg.Index
		if !emitted && len(*items) > 0 {
			prev := (*items)[len(*items)-1]
			if prev.SourceKey != entry.ID+"/"+seg.SessionID && seq != prev.Sequence+1 {
				*items = nil
			}
		}
		*items = append(*items, packagedManifestItem{
			SourceKey:             entry.ID + "/" + seg.SessionID,
			InitURI:               fmt.Sprintf("/channel/%s/%s/%s/init.mp4", channelID, sessionPath, seg.SessionID),
			SegmentURI:            fmt.Sprintf("/channel/%s/%s/%s/%d.m4s", channelID, sessionPath, seg.SessionID, seg.Index),
			DurationMs:            seg.DurationMs,
			Sequence:              seq,
			DiscontinuitySequence: discSeq,
			WallClockStartMs:      entry.StartMs + (seg.MediaStartMs - entry.OffsetMs),
			ProgramDateTimeAlways: true,
		})
		wallMs = entry.StartMs + (seg.MediaStartMs + seg.DurationMs - entry.OffsetMs)
		emitted = true
	}
	return emitted, nil
}

func onDemandMediaSequence(entry db.ScheduleEntry, mediaStartMs int64) int64 {
	return entry.StartMs/db.ScheduleGridMs + divRound(mediaStartMs-entry.OffsetMs, db.ScheduleGridMs)
}

func divRound(v, denom int64) int64 {
	if denom <= 0 {
		return 0
	}
	if v >= 0 {
		return (v + denom/2) / denom
	}
	return (v - denom/2) / denom
}

func (a *app) onDemandDiscontinuitySequence(ctx context.Context, channelID string, entryStartMs int64) (int64, error) {
	var sequence int64
	err := a.dbConn.QueryRowContext(ctx, `
		SELECT COALESCE(COUNT(*), 0)
		FROM schedule_entries
		WHERE channel_id = ?
		  AND start_ms < ?`, channelID, entryStartMs).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("on-demand discontinuity sequence channel=%s start_ms=%d: %w", channelID, entryStartMs, err)
	}
	return sequence, nil
}

func (a *app) packagedManifestSequenceBase(ctx context.Context, channelID, profile string, entryStartMs int64) (int64, error) {
	var base int64
	err := a.dbConn.QueryRowContext(ctx, `
		SELECT COALESCE(COUNT(*), 0)
		FROM schedule_entries se
		JOIN media_packages p
		  ON p.media_id = se.media_id
		 AND p.rendition_profile = ?
		 AND p.status = ?
		JOIN packaged_segments ps ON ps.package_id = p.id
		WHERE se.channel_id = ?
		  AND se.start_ms < ?
		  AND ps.media_start_ms + ps.duration_ms > se.offset_ms
		  AND ps.media_start_ms < se.offset_ms + se.duration_ms`,
		profile, string(db.PackageStatusReady), channelID, entryStartMs).Scan(&base)
	if err != nil {
		return 0, fmt.Errorf("manifest sequence base channel=%s profile=%s start_ms=%d: %w", channelID, profile, entryStartMs, err)
	}
	return base, nil
}

func (a *app) packagedManifestEntrySegmentOffset(ctx context.Context, packageID string, offsetMs, entryMediaEndMs, segmentNumber int64) (int64, error) {
	var count int64
	err := a.dbConn.QueryRowContext(ctx, `
		SELECT COALESCE(COUNT(*), 0)
		FROM packaged_segments
		WHERE package_id = ?
		  AND segment_number < ?
		  AND media_start_ms + duration_ms > ?
		  AND media_start_ms < ?`,
		packageID, segmentNumber, offsetMs, entryMediaEndMs).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("manifest segment offset package=%s segment=%d: %w", packageID, segmentNumber, err)
	}
	return count, nil
}

func (a *app) packagedManifestDiscontinuitySequence(ctx context.Context, channelID, profile string, entryStartMs int64) (int64, error) {
	var sequence int64
	err := a.dbConn.QueryRowContext(ctx, `
		WITH ordered AS (
			SELECT
				se.start_ms,
				p.id AS package_id,
				LAG(p.id) OVER (ORDER BY se.start_ms) AS previous_package_id
			FROM schedule_entries se
			JOIN media_packages p
			  ON p.media_id = se.media_id
			 AND p.rendition_profile = ?
			 AND p.status = ?
			WHERE se.channel_id = ?
			  AND se.start_ms <= ?
		)
		SELECT COALESCE(COUNT(*), 0)
		FROM ordered
		WHERE previous_package_id IS NOT NULL
		  AND previous_package_id != package_id`,
		profile, string(db.PackageStatusReady), channelID, entryStartMs).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("manifest discontinuity sequence channel=%s profile=%s start_ms=%d: %w", channelID, profile, entryStartMs, err)
	}
	return sequence, nil
}

func (a *app) handlePackagedInit(w http.ResponseWriter, r *http.Request) {
	rt := a.lookupChannelOr404(r.Context(), w, r.PathValue("channelID"))
	if rt == nil {
		return
	}
	pkg, err := db.MediaPackageByID(r.Context(), a.dbConn, r.PathValue("packageID"))
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if pkg == nil || pkg.Status != db.PackageStatusReady || pkg.InitSegmentPath == nil {
		metrics.PackagedArtifactNotFoundTotal.WithLabelValues("init").Inc()
		http.NotFound(w, r)
		return
	}
	if missingPackagedFile(*pkg.InitSegmentPath) {
		metrics.PackagedArtifactNotFoundTotal.WithLabelValues("init").Inc()
		a.requeueReadyPackageForMissingArtifact(r, r.PathValue("channelID"), pkg.ID, *pkg.InitSegmentPath, "playback_404")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, *pkg.InitSegmentPath)
}

func (a *app) handlePackagedSegment(w http.ResponseWriter, r *http.Request) {
	rt := a.lookupChannelOr404(r.Context(), w, r.PathValue("channelID"))
	if rt == nil {
		return
	}
	name := r.PathValue("name")
	if !strings.HasSuffix(name, ".m4s") {
		metrics.PackagedArtifactNotFoundTotal.WithLabelValues("segment").Inc()
		http.NotFound(w, r)
		return
	}
	segmentNumber, err := strconv.ParseInt(strings.TrimSuffix(name, ".m4s"), 10, 64)
	if err != nil {
		metrics.PackagedArtifactNotFoundTotal.WithLabelValues("segment").Inc()
		http.NotFound(w, r)
		return
	}
	seg, err := db.PackagedSegmentByNumber(r.Context(), a.dbConn, r.PathValue("packageID"), segmentNumber)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if seg == nil || seg.Path == nil {
		metrics.PackagedArtifactNotFoundTotal.WithLabelValues("segment").Inc()
		a.requeueReadyPackageForMissingArtifact(r, r.PathValue("channelID"), r.PathValue("packageID"), "packaged segment "+name, "playback_404")
		http.NotFound(w, r)
		return
	}
	if missingPackagedFile(*seg.Path) {
		metrics.PackagedArtifactNotFoundTotal.WithLabelValues("segment").Inc()
		a.requeueReadyPackageForMissingArtifact(r, r.PathValue("channelID"), r.PathValue("packageID"), *seg.Path, "playback_404")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/iso.segment")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, *seg.Path)
}

func (a *app) handleSessionInit(w http.ResponseWriter, r *http.Request) {
	if a.lookupChannelOr404(r.Context(), w, r.PathValue("channelID")) == nil {
		return
	}
	if a.sessions == nil {
		http.NotFound(w, r)
		return
	}
	path, ok := a.sessions.InitPath(r.PathValue("channelID"), r.PathValue("sessionID"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, path)
}

func (a *app) handleSessionSegment(w http.ResponseWriter, r *http.Request) {
	if a.lookupChannelOr404(r.Context(), w, r.PathValue("channelID")) == nil {
		return
	}
	if a.sessions == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if !strings.HasSuffix(name, ".m4s") {
		http.NotFound(w, r)
		return
	}
	index, err := strconv.ParseInt(strings.TrimSuffix(name, ".m4s"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, ok := a.sessions.SegmentPath(r.PathValue("channelID"), r.PathValue("sessionID"), index)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/iso.segment")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, path)
}

func missingPackagedFile(path string) bool {
	if path == "" {
		return true
	}
	info, err := os.Stat(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	return info.IsDir()
}

func (a *app) requeueReadyPackageForMissingArtifact(r *http.Request, channelID, packageID, missingPath, source string) {
	reason := fmt.Sprintf("packaged artifact missing during playback: %s", missingPath)
	changed, err := db.MarkReadyPackagePendingForReencode(r.Context(), a.dbConn, packageID, time.Now().UTC().UnixMilli(), reason)
	if err != nil {
		log.Printf("WARN package reactive repair failed channel=%s package=%s missing=%q err=%v", channelID, packageID, missingPath, err)
		return
	}
	if changed {
		metrics.PackageRepairRequeuesTotal.WithLabelValues(source).Inc()
		log.Printf("package reactive repair queued channel=%s package=%s missing=%q", channelID, packageID, missingPath)
	}
}

func ceilSeconds(ms int64) int64 {
	if ms <= 0 {
		return 1
	}
	return (ms + 999) / 1000
}

func formatEXTINF(ms int64) string {
	return fmt.Sprintf("%.3f", float64(ms)/1000)
}
