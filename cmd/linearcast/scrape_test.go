package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/playback"
)

// TestScrapeOwner_DerivesSignalsAtCollectTime is the scrape-behavior gate for
// the realtime signals the roadmap routes through the scrape owner. It seeds
// known DB and on-demand state, scrapes, and asserts the derived gauges match
// the live state — not a last-write push. This is the test the operator
// recovery view's "each documented signal has a scrape or behavior test"
// precondition points at for lease, capacity, disk-pressure, queue depth,
// runway, and schedule gaps.
func TestScrapeOwner_DerivesSignalsAtCollectTime(t *testing.T) {
	conn := newScrapeTestDB(t)
	now := time.Now().UTC().UnixMilli()

	encID, _, err := db.RegisterEncoder(context.Background(), conn, "scrape-test", "{}", now)
	if err != nil {
		t.Fatalf("register encoder: %v", err)
	}
	for _, mid := range []string{"m1", "m2", "m3"} {
		if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
			video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
			VALUES (?, ?, '/tmp', 120000, 'mkv', 'h264', 1080, 'aac', 1, ?)`, mid, "/tmp/"+mid+".mkv", now); err != nil {
			t.Fatalf("insert media %s: %v", mid, err)
		}
	}
	// m1 is ready with a packaged duration; m2 and m3 are processing. Queue
	// depth must show 1 ready + 2 processing for the h264 profile.
	if _, err := conn.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status,
		created_at_ms, updated_at_ms, packaged_duration_ms)
		VALUES ('pkg-m1', 'm1', 'h264-1080p-8mbps', 'ready', ?, ?, 120000)`, now, now); err != nil {
		t.Fatalf("insert package m1: %v", err)
	}
	for _, mid := range []string{"m2", "m3"} {
		if _, err := conn.Exec(`INSERT INTO media_packages (id, media_id, rendition_profile, status, created_at_ms, updated_at_ms)
			VALUES (?, ?, 'h264-1080p-8mbps', 'processing', ?, ?)`, "pkg-"+mid, mid, now, now); err != nil {
			t.Fatalf("insert package %s: %v", mid, err)
		}
	}
	// Two live leases, one expired lease the sweeper has not yet reclaimed. The
	// active-lease gauge must count only the two with a future expiry.
	leases := []struct {
		pkg   string
		delta int64
	}{
		{"pkg-m2", 60_000},
		{"pkg-m3", 60_000},
		{"pkg-m1", -60_000},
	}
	for _, l := range leases {
		if _, err := conn.Exec(`INSERT INTO encoder_jobs (package_id, encoder_id, claimed_at_ms, lease_expires_ms, last_heartbeat_ms)
			VALUES (?, ?, ?, ?, ?)`, l.pkg, encID, now, now+l.delta, now); err != nil {
			t.Fatalf("insert encoder_jobs %s: %v", l.pkg, err)
		}
	}

	// Channel with a schedule entry so runway and gap signals are non-trivial.
	// m1 is linked via channel_media so package-ready-duration picks up its
	// 120000ms packaged duration.
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms,
		required_package_profile, prefill_mode)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'h264-1080p-8mbps', 'on_demand')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, added_at_ms) VALUES ('ch', 'm1', 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}
	gridStart := (now / db.ScheduleGridMs) * db.ScheduleGridMs
	if _, err := conn.Exec(`INSERT INTO schedule_entries (id, channel_id, start_ms, media_id, offset_ms, duration_ms, created_at_ms)
		VALUES ('e1', 'ch', ?, 'm1', 0, 120000, 0)`, gridStart); err != nil {
		t.Fatalf("insert schedule entry: %v", err)
	}

	mgr, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:          filepath.Join(t.TempDir(), "encodings"),
		MaxConcurrent: 4,
		DB:            conn,
	})
	if err != nil {
		t.Fatalf("new ondemand manager: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	channels := func() []playback.ChannelSnapshot {
		return []playback.ChannelSnapshot{{ID: "ch", RequiredPackageProfile: "h264-1080p-8mbps"}}
	}
	reg := prometheus.NewRegistry()
	if err := reg.Register(newScrapeOwner(conn, mgr, t.TempDir(), channels)); err != nil {
		t.Fatalf("register scrape owner: %v", err)
	}

	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	if got := gaugeValue(t, fams, "linearcast_encoder_active_leases"); got != 2 {
		t.Fatalf("linearcast_encoder_active_leases = %v, want 2 (expired lease must not count)", got)
	}
	if got := gaugeValue(t, fams, "linearcast_on_demand_active_encodings"); got != 0 {
		t.Fatalf("linearcast_on_demand_active_encodings = %v, want 0 (no encodings spawned)", got)
	}
	if got := gaugeValue(t, fams, "linearcast_on_demand_max_concurrent"); got != 4 {
		t.Fatalf("linearcast_on_demand_max_concurrent = %v, want 4", got)
	}
	if got := gaugeValue(t, fams, "linearcast_disk_free_gb"); got <= 0 {
		t.Fatalf("linearcast_disk_free_gb = %v, want > 0", got)
	}

	if got := gaugeByLabels(t, fams, "linearcast_package_queue_depth", "h264-1080p-8mbps", "processing"); got != 2 {
		t.Fatalf("queue_depth{processing} = %v, want 2", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_package_queue_depth", "h264-1080p-8mbps", "ready"); got != 1 {
		t.Fatalf("queue_depth{ready} = %v, want 1", got)
	}
	if got := gaugeValue(t, fams, "linearcast_schedule_runway_seconds"); got <= 0 {
		t.Fatalf("schedule_runway_seconds = %v, want > 0", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_schedule_runway_by_channel_seconds", "ch"); got <= 0 {
		t.Fatalf("runway_by_channel{ch} = %v, want > 0", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_schedule_gap_count", "ch"); got != 0 {
		t.Fatalf("gap_count{ch} = %v, want 0 (single entry, no gaps)", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_schedule_gap_active", "ch"); got != 0 {
		t.Fatalf("gap_active{ch} = %v, want 0 (inside the single entry)", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_package_ready_duration_ms", "ch", "h264-1080p-8mbps"); got != 120000 {
		t.Fatalf("package_ready_duration_ms{ch} = %v, want 120000", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_degraded", "disk_pressure"); got != 0 {
		t.Fatalf("degraded{disk_pressure} = %v, want 0 (temp dir has free space)", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_degraded", "at_capacity"); got != 0 {
		t.Fatalf("degraded{at_capacity} = %v, want 0 (0 active / 4 max)", got)
	}
}

// TestScrapeOwner_NoLeasesReportsZero covers the empty case so the gauge can't
// be confused with a missing metric when the encoder fleet is idle and no
// channels are configured.
func TestScrapeOwner_NoLeasesReportsZero(t *testing.T) {
	conn := newScrapeTestDB(t)
	mgr, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:          filepath.Join(t.TempDir(), "encodings"),
		MaxConcurrent: 2,
		DB:            conn,
	})
	if err != nil {
		t.Fatalf("new ondemand manager: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	reg := prometheus.NewRegistry()
	if err := reg.Register(newScrapeOwner(conn, mgr, t.TempDir(), func() []playback.ChannelSnapshot { return nil })); err != nil {
		t.Fatalf("register scrape owner: %v", err)
	}
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if got := gaugeValue(t, fams, "linearcast_encoder_active_leases"); got != 0 {
		t.Fatalf("linearcast_encoder_active_leases = %v, want 0", got)
	}
	if got := gaugeValue(t, fams, "linearcast_schedule_runway_seconds"); got != 0 {
		t.Fatalf("linearcast_schedule_runway_seconds = %v, want 0 (no channels)", got)
	}
}

// TestScrapeOwner_DegradedFlagsFire asserts the degraded gauge flips to 1 when
// a signal crosses its threshold. Thresholds are overridden on the owner so the
// test doesn't depend on the actual filesystem free space or spawning real
// encodings.
func TestScrapeOwner_DegradedFlagsFire(t *testing.T) {
	conn := newScrapeTestDB(t)
	mgr, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:          filepath.Join(t.TempDir(), "encodings"),
		MaxConcurrent: 4,
		DB:            conn,
	})
	if err != nil {
		t.Fatalf("new ondemand manager: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	owner := newScrapeOwner(conn, mgr, t.TempDir(), func() []playback.ChannelSnapshot { return nil })
	owner.diskFreeThresholdGB = 1e9
	owner.capacityUtilization = 0.0

	reg := prometheus.NewRegistry()
	if err := reg.Register(owner); err != nil {
		t.Fatalf("register: %v", err)
	}
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if got := gaugeByLabels(t, fams, "linearcast_degraded", "disk_pressure"); got != 1 {
		t.Fatalf("degraded{disk_pressure} = %v, want 1 (threshold set absurdly high)", got)
	}
	if got := gaugeByLabels(t, fams, "linearcast_degraded", "at_capacity"); got != 1 {
		t.Fatalf("degraded{at_capacity} = %v, want 1 (utilization threshold set to 0)", got)
	}
}

func newScrapeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "linearcast.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	return conn
}

func gaugeValue(t *testing.T, fams []*dto.MetricFamily, name string) float64 {
	t.Helper()
	for _, f := range fams {
		if f.GetName() == name {
			if len(f.GetMetric()) == 0 {
				t.Fatalf("metric %s has no samples", name)
			}
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s not found in gathered families", name)
	return 0
}

// gaugeByLabels finds the gauge sample whose label values match vals in order.
func gaugeByLabels(t *testing.T, fams []*dto.MetricFamily, name string, vals ...string) float64 {
	t.Helper()
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := m.GetLabel()
			if len(labels) != len(vals) {
				continue
			}
			match := true
			for i, l := range labels {
				if l.GetValue() != vals[i] {
					match = false
					break
				}
			}
			if match {
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, vals)
	return 0
}
