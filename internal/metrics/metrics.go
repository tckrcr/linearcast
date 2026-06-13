// Package metrics owns Prometheus instruments shared by the service processes.
package metrics

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	PackageQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "linearcast_package_queue_depth",
		Help: "Current package rows by rendition profile and bounded status.",
	}, []string{"rendition_profile", "status"})
	PackageJobDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "linearcast_package_job_duration_seconds",
		Help:    "Package worker job duration by rendition profile and result.",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600},
	}, []string{"rendition_profile", "result"})
	PackageStateTransitionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "linearcast_package_state_transitions_total",
		Help: "Package state transitions by rendition profile and bounded from/to states.",
	}, []string{"rendition_profile", "from_status", "to_status"})
	PackageRepairRequeuesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "linearcast_package_repair_requeues_total",
		Help: "Ready packages moved back to pending for repair by source.",
	}, []string{"source"})
	PackageUnknownDuration = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "linearcast_package_unknown_duration",
		Help: "Ready packages whose packaged/source duration is unknown, so the truncation audit could not run, as of the last integrity sweep.",
	})
	PackagedArtifactNotFoundTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "linearcast_packaged_artifact_not_found_total",
		Help: "Packaged artifact 404 responses by bounded artifact type.",
	}, []string{"artifact_type"})
	ManifestGenerationDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "linearcast_manifest_generation_duration_seconds",
		Help:    "Packaged HLS manifest generation latency by rendition profile and result.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2},
	}, []string{"rendition_profile", "result"})
	ManifestSegmentsListed = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "linearcast_manifest_segments_listed",
		Help:    "Number of packaged media segments listed in HLS manifests.",
		Buckets: []float64{0, 1, 2, 4, 6, 8, 12, 16, 24},
	})

	ScheduleFilesWrittenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_schedule_files_written_total",
		Help: "Schedule files written by the scheduler.",
	})
	ScheduleRunwaySeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "linearcast_schedule_runway_seconds",
		Help: "Seconds between now and the latest schedule horizon end.",
	})
	ScheduleRunwayByChannelSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "linearcast_schedule_runway_by_channel_seconds",
		Help: "Seconds between now and each channel's latest schedule entry end.",
	}, []string{"channel_id"})
	ScheduleEntriesWrittenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_schedule_entries_written_total",
		Help: "Schedule entries written by the scheduler.",
	})
	ScheduleTickDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "linearcast_schedule_tick_duration_seconds",
		Help:    "Scheduler tick duration.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	})
	ScheduleTickErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_schedule_tick_errors_total",
		Help: "Scheduler ticks that returned an error.",
	})
	ScheduleHoldoversTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_schedule_holdovers_total",
		Help: "Schedule files that began by continuing the previous horizon's final entry.",
	})
	SchedulePolicyPicksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "linearcast_schedule_policy_picks_total",
		Help: "Scheduler media picks by daypart and whether a daypart-tagged pool was active.",
	}, []string{"daypart", "tagged"})
	PackageReadyDurationMs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "linearcast_package_ready_duration_ms",
		Help: "Total packaged_duration_ms of all ready packages per channel.",
	}, []string{"channel_id", "rendition_profile"})
	ScheduleGapCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "linearcast_schedule_gap_count",
		Help: "Number of schedule gaps exceeding threshold per channel.",
	}, []string{"channel_id"})
	ScheduleGapActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "linearcast_schedule_gap_active",
		Help: "1 if now falls inside a schedule gap for the channel, 0 otherwise.",
	}, []string{"channel_id"})

	OnDemandSessionSpawnsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_on_demand_session_spawns_total",
		Help: "Total on-demand live session spawns (first segment delivered).",
	})
	OnDemandSessionRestartsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_on_demand_session_restarts_total",
		Help: "Total on-demand session entry restarts after failure.",
	})
	OnDemandSessionEvictionsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_on_demand_session_evictions_total",
		Help: "Total on-demand sessions evicted to make room for other channels.",
	})
	OnDemandWarming503Total = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_on_demand_warming_503_total",
		Help: "On-demand session warming (no segments yet) 503 responses.",
	})
	OnDemandAtCapacity503Total = promauto.NewCounter(prometheus.CounterOpts{
		Name: "linearcast_on_demand_at_capacity_503_total",
		Help: "On-demand session at-capacity 503 responses.",
	})
	OnDemandSessionSpawnLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "linearcast_on_demand_session_spawn_latency_seconds",
		Help:    "Seconds from ffmpeg start to first segment available.",
		Buckets: []float64{0.5, 1, 2, 3, 5, 8, 12, 20, 30, 60},
	})
	OnDemandSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "linearcast_on_demand_sessions",
		Help: "Current on-demand live sessions by state (starting/serving/failed).",
	}, []string{"state"})
)

func ObserveDuration(h prometheus.Observer, start time.Time) {
	h.Observe(time.Since(start).Seconds())
}

func ReasonLabel(reason string) string {
	reason = strings.ToLower(reason)
	switch {
	case reason == "":
		return "unknown"
	case strings.Contains(reason, "schedule gap"):
		return "schedule_gap"
	case strings.Contains(reason, "schedule not loaded"):
		return "schedule_not_loaded"
	case strings.Contains(reason, "missing media"):
		return "missing_media"
	case strings.Contains(reason, "missing source"):
		return "missing_source"
	case strings.Contains(reason, "ffmpeg"):
		return "ffmpeg_failed"
	case strings.Contains(reason, "canceled"):
		return "canceled"
	default:
		return "other"
	}
}

func PackageStatusLabel(status string) string {
	switch status {
	case "", "missing":
		return "missing"
	case "pending", "processing", "ready", "failed":
		return status
	default:
		return "other"
	}
}

func PackageResultLabel(err error) string {
	if err == nil {
		return "ready"
	}
	return ReasonLabel(err.Error())
}
