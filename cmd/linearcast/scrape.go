package main

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/metrics"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/playback"
	"github.com/tckrcr/linearcast/internal/sysinfo"
)

// scrapeOwner is the realtime collector for signals that can't be trusted to a
// push gauge. Lease state advances by a time-based sweeper, on-demand capacity
// is a live in-process count against a budget, disk-free is a syscall fact,
// queue depth is a DB group-by, and schedule runway/gaps are DB horizon
// queries — all go stale between write events, so Collect derives them from
// their sources on every scrape and /metrics always reports the current state.
//
// The owner also derives a degraded gauge: 1 when a signal crosses an
// operator-attention threshold, 0 otherwise. Degraded is not a traffic-routing
// signal (it does not affect /readyz) — it surfaces "still serving, but
// something needs attention" for alerting without triggering a container
// restart.
//
// Counters (repair requeues, state transitions, 503 events) and scan-result
// gauges (cache bytes, unknown-duration) stay as push instruments in
// internal/metrics because their values are event counts or scan outputs that
// don't go stale the way state gauges do.
type scrapeOwner struct {
	db        *sql.DB
	encodings *ondemand.Manager
	cacheRoot string
	channels  func() []playback.ChannelSnapshot

	// degraded thresholds — overridable in tests, set to production defaults
	// by newScrapeOwner.
	diskFreeThresholdGB float64
	capacityUtilization float64

	activeLeases          *prometheus.Desc
	onDemandActive        *prometheus.Desc
	onDemandMaxConcurrent *prometheus.Desc
	diskFreeGB            *prometheus.Desc
	packageQueueDepth     *prometheus.Desc
	scheduleRunway        *prometheus.Desc
	scheduleRunwayByChan  *prometheus.Desc
	scheduleGapCount      *prometheus.Desc
	scheduleGapActive     *prometheus.Desc
	packageReadyDuration  *prometheus.Desc
	degraded              *prometheus.Desc
}

func newScrapeOwner(db *sql.DB, encodings *ondemand.Manager, cacheRoot string, channels func() []playback.ChannelSnapshot) *scrapeOwner {
	return &scrapeOwner{
		db:                  db,
		encodings:           encodings,
		cacheRoot:           cacheRoot,
		channels:            channels,
		diskFreeThresholdGB: metrics.DefaultDiskFreeThresholdGB,
		capacityUtilization: metrics.DefaultCapacityUtilization,
		activeLeases: prometheus.NewDesc(
			"linearcast_encoder_active_leases",
			"Encoder job leases currently held (encoder_jobs rows with lease_expires_ms >= now). Falls as leases expire and the sweeper reclaims them.",
			nil, nil,
		),
		onDemandActive: prometheus.NewDesc(
			"linearcast_on_demand_active_encodings",
			"On-demand channel encodings currently competing for MaxConcurrent (starting or serving with a running process).",
			nil, nil,
		),
		onDemandMaxConcurrent: prometheus.NewDesc(
			"linearcast_on_demand_max_concurrent",
			"Configured on-demand encoding MaxConcurrent budget. Capacity utilization is the ratio of active to this budget.",
			nil, nil,
		),
		diskFreeGB: prometheus.NewDesc(
			"linearcast_disk_free_gb",
			"Free disk space in gigabytes on the filesystem holding the package cache (CACHE_DIR). The disk-pressure signal for artifact growth.",
			nil, nil,
		),
		packageQueueDepth: prometheus.NewDesc(
			"linearcast_package_queue_depth",
			"Current package rows by rendition profile and bounded status.",
			[]string{"rendition_profile", "status"}, nil,
		),
		scheduleRunway: prometheus.NewDesc(
			"linearcast_schedule_runway_seconds",
			"Seconds between now and the latest schedule horizon end across all channels.",
			nil, nil,
		),
		scheduleRunwayByChan: prometheus.NewDesc(
			"linearcast_schedule_runway_by_channel_seconds",
			"Seconds between now and each channel's latest schedule entry end.",
			[]string{"channel_id"}, nil,
		),
		scheduleGapCount: prometheus.NewDesc(
			"linearcast_schedule_gap_count",
			"Number of schedule gaps exceeding threshold per channel.",
			[]string{"channel_id"}, nil,
		),
		scheduleGapActive: prometheus.NewDesc(
			"linearcast_schedule_gap_active",
			"1 if now falls inside a schedule gap for the channel, 0 otherwise.",
			[]string{"channel_id"}, nil,
		),
		packageReadyDuration: prometheus.NewDesc(
			"linearcast_package_ready_duration_ms",
			"Total packaged_duration_ms of all ready packages per channel.",
			[]string{"channel_id", "rendition_profile"}, nil,
		),
		degraded: prometheus.NewDesc(
			"linearcast_degraded",
			"1 when a signal crosses its operator-attention threshold, 0 otherwise. Does not affect /readyz — degraded means still serving, not unhealthy. Signals: disk_pressure (free GB below threshold), at_capacity (active encodings / max concurrent >= threshold).",
			[]string{"signal"}, nil,
		),
	}
}

func (s *scrapeOwner) Describe(ch chan<- *prometheus.Desc) {
	ch <- s.activeLeases
	ch <- s.onDemandActive
	ch <- s.onDemandMaxConcurrent
	ch <- s.diskFreeGB
	ch <- s.packageQueueDepth
	ch <- s.scheduleRunway
	ch <- s.scheduleRunwayByChan
	ch <- s.scheduleGapCount
	ch <- s.scheduleGapActive
	ch <- s.packageReadyDuration
	ch <- s.degraded
}

func (s *scrapeOwner) Collect(ch chan<- prometheus.Metric) {
	now := time.Now().UTC().UnixMilli()
	ctx := context.Background()

	s.collectLeaseCapacityDisk(ch, now)
	s.collectQueueDepth(ch, ctx)
	s.collectScheduleMetrics(ch, ctx, now)
}

func (s *scrapeOwner) collectLeaseCapacityDisk(ch chan<- prometheus.Metric, nowMs int64) {
	if n, err := s.countActiveLeases(nowMs); err != nil {
		slog.Warn("scrape: active lease count failed", "err", err)
	} else {
		ch <- prometheus.MustNewConstMetric(s.activeLeases, prometheus.GaugeValue, float64(n))
	}

	active := s.encodings.ActiveCount()
	maxC := s.encodings.MaxConcurrent()
	ch <- prometheus.MustNewConstMetric(s.onDemandActive, prometheus.GaugeValue, float64(active))
	ch <- prometheus.MustNewConstMetric(s.onDemandMaxConcurrent, prometheus.GaugeValue, float64(maxC))

	var diskFree float64
	if s.cacheRoot != "" {
		diskFree = sysinfo.DiskFreeGB(s.cacheRoot)
		ch <- prometheus.MustNewConstMetric(s.diskFreeGB, prometheus.GaugeValue, diskFree)
	}

	if diskFree < s.diskFreeThresholdGB && s.cacheRoot != "" {
		ch <- prometheus.MustNewConstMetric(s.degraded, prometheus.GaugeValue, 1, "disk_pressure")
	} else {
		ch <- prometheus.MustNewConstMetric(s.degraded, prometheus.GaugeValue, 0, "disk_pressure")
	}

	capacityDegraded := 0.0
	if maxC > 0 && float64(active)/float64(maxC) >= s.capacityUtilization {
		capacityDegraded = 1
	}
	ch <- prometheus.MustNewConstMetric(s.degraded, prometheus.GaugeValue, capacityDegraded, "at_capacity")
}

func (s *scrapeOwner) collectQueueDepth(ch chan<- prometheus.Metric, ctx context.Context) {
	rows, err := db.PackageProfileSummaries(ctx, s.db)
	if err != nil {
		slog.Warn("scrape: package queue depth failed", "err", err)
		return
	}
	for _, row := range rows {
		ch <- prometheus.MustNewConstMetric(s.packageQueueDepth, prometheus.GaugeValue,
			float64(row.PackageCount), row.RenditionProfile, metrics.PackageStatusLabel(row.Status))
	}
}

func (s *scrapeOwner) collectScheduleMetrics(ch chan<- prometheus.Metric, ctx context.Context, nowMs int64) {
	channels := s.channels()
	lookahead := nowMs + int64(48*3600*1000)
	var maxRunway float64
	for _, chn := range channels {
		runway := s.collectChannelSchedule(ch, ctx, chn, nowMs, lookahead)
		if runway > maxRunway {
			maxRunway = runway
		}
	}
	ch <- prometheus.MustNewConstMetric(s.scheduleRunway, prometheus.GaugeValue, maxRunway)
}

func (s *scrapeOwner) collectChannelSchedule(ch chan<- prometheus.Metric, ctx context.Context, chn playback.ChannelSnapshot, nowMs, lookahead int64) float64 {
	profile := chn.RequiredPackageProfile
	if profile == "" {
		profile = db.DefaultPackageProfile
	}

	pkgMs, err := db.ChannelPackageCoverageMs(ctx, s.db, chn.ID, profile)
	if err != nil {
		slog.Warn("scrape: package coverage failed", "channel", chn.ID, "err", err)
	} else {
		ch <- prometheus.MustNewConstMetric(s.packageReadyDuration, prometheus.GaugeValue,
			float64(pkgMs), chn.ID, profile)
	}

	gaps, err := db.ScheduleGaps(ctx, s.db, chn.ID, nowMs, lookahead)
	if err != nil {
		slog.Warn("scrape: schedule gaps failed", "channel", chn.ID, "err", err)
	} else {
		ch <- prometheus.MustNewConstMetric(s.scheduleGapCount, prometheus.GaugeValue,
			float64(len(gaps)), chn.ID)
		active := 0
		for _, gap := range gaps {
			if gap.StartMs <= nowMs && nowMs < gap.EndMs {
				active = 1
				break
			}
		}
		ch <- prometheus.MustNewConstMetric(s.scheduleGapActive, prometheus.GaugeValue,
			float64(active), chn.ID)
	}

	var runway float64
	last, err := db.LastScheduleEntry(ctx, s.db, chn.ID)
	if err != nil {
		slog.Warn("scrape: last schedule entry failed", "channel", chn.ID, "err", err)
	} else if last != nil {
		runway = float64(last.StartMs+last.DurationMs-nowMs) / 1000
		if runway < 0 {
			runway = 0
		}
	}
	ch <- prometheus.MustNewConstMetric(s.scheduleRunwayByChan, prometheus.GaugeValue,
		runway, chn.ID)
	return runway
}

func (s *scrapeOwner) countActiveLeases(nowMs int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM encoder_jobs WHERE lease_expires_ms >= ?`, nowMs).Scan(&n)
	return n, err
}
