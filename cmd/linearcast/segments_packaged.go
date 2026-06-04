package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/metrics"
)

const pdtLayout = "2006-01-02T15:04:05.000Z"

type packagedManifestItem struct {
	Package               db.MediaPackage
	Segment               db.PackagedSegment
	Sequence              int64
	DiscontinuitySequence int64
	WallClockStartMs      int64 // wall-clock time when this segment begins
}

func (a *app) handlePackagedManifest(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	if a.lookupChannelOr404(r.Context(), w, channelID) == nil {
		return
	}
	profile := a.packagedProfile
	if rt := a.channel(channelID); rt != nil && rt.RequiredPackageProfile != "" {
		profile = rt.RequiredPackageProfile
	}
	a.writePackagedManifest(w, r, channelID, profile)
}

func (a *app) writePackagedManifest(w http.ResponseWriter, r *http.Request, channelID, profile string) {
	started := time.Now()
	result := "ready"
	defer func() {
		metrics.ManifestGenerationDuration.WithLabelValues(profile, result).Observe(time.Since(started).Seconds())
	}()
	nowMs := time.Now().UTC().UnixMilli()
	items, err := a.packagedManifestItems(r.Context(), channelID, profile, nowMs)
	if err != nil {
		result = metrics.ReasonLabel(err.Error())
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
		if sec := ceilSeconds(item.Segment.DurationMs); sec > targetDuration {
			targetDuration = sec
		}
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", items[0].Sequence)
	fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", items[0].DiscontinuitySequence)
	var lastPackageID string
	for _, item := range items {
		if item.Package.ID != lastPackageID {
			if lastPackageID != "" {
				b.WriteString("#EXT-X-DISCONTINUITY\n")
			}
			pdt := time.UnixMilli(item.WallClockStartMs).UTC().Format(pdtLayout)
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"/channel/%s/%s/init/%s/init.mp4\"\n", channelID, packagedPath, item.Package.ID)
			lastPackageID = item.Package.ID
		}
		fmt.Fprintf(&b, "#EXTINF:%s,\n", formatEXTINF(item.Segment.DurationMs))
		fmt.Fprintf(&b, "/channel/%s/%s/segments/%s/%d.m4s\n", channelID, packagedPath, item.Package.ID, item.Segment.SegmentNumber)
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
	metrics.ManifestSegmentsListed.Observe(float64(len(items)))
}

func (a *app) packagedManifestItems(ctx context.Context, channelID, profile string, nowMs int64) ([]packagedManifestItem, error) {
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
	for wallMs < deadlineMs && len(items) < packagedManifestLimit {
		entry := db.FindScheduleEntry(entries, wallMs)
		if entry == nil {
			break
		}
		pkg, err := db.ReadyMediaPackage(ctx, a.dbConn, entry.MediaID, profile)
		if err != nil {
			return nil, fmt.Errorf("ready package media=%s profile=%s: %w", entry.MediaID, profile, err)
		}
		if pkg == nil {
			if len(items) == 0 {
				return nil, fmt.Errorf("package not ready media=%s profile=%s", entry.MediaID, profile)
			}
			break
		}
		if !recordedCurrentEntry && wallMs == nowMs {
			if _, err := db.RecordPlayHistory(ctx, a.dbConn, *entry); err != nil {
				log.Printf("record play history channel=%s entry=%s: %v", channelID, entry.ID, err)
			}
			recordedCurrentEntry = true
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
		sequenceBase, err := a.packagedManifestSequenceBase(ctx, channelID, profile, entry.StartMs)
		if err != nil {
			return nil, err
		}
		discontinuitySequence, err := a.packagedManifestDiscontinuitySequence(ctx, channelID, profile, entry.StartMs)
		if err != nil {
			return nil, err
		}
		priorSegments, err := a.packagedManifestEntrySegmentOffset(ctx, pkg.ID, entry.OffsetMs, entryMediaEnd, segs[0].SegmentNumber)
		if err != nil {
			return nil, err
		}
		emittedInEntry := int64(0)
		for _, seg := range segs {
			if len(items) >= packagedManifestLimit || wallMs >= deadlineMs {
				break
			}
			if seg.MediaStartMs >= entryMediaEnd {
				break
			}
			items = append(items, packagedManifestItem{
				Package:               *pkg,
				Segment:               seg,
				Sequence:              sequenceBase + priorSegments + emittedInEntry,
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
