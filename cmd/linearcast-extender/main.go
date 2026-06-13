// cmd/linearcast-extender is a long-running writer that keeps every
// enabled channel's schedule covered out to a target horizon. It reuses
// internal/scheduler to extend each channel when the remaining future
// drops below a low-water mark.
//
// Linearcast itself stays read-only on the database; the extender is the
// writer.
//
// Env:
//
//	LINEARCAST_DB  (required)  path to linearcast.db
//
// Scheduler tunables (horizon, low-water, tick) live in the settings table
// and are managed from the admin UI. The default packaged profile lives in
// settings.default_packaged_profile, toggled from the Profiles panel.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

type config struct {
	dbPath         string
	horizonHours   int
	lowWaterHours  int
	tickInterval   time.Duration
	packageProfile string
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatalf("config: LINEARCAST_DB is required")
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	if err := db.ApplySchema(context.Background(), conn); err != nil {
		log.Fatalf("apply schema: %v", err)
	}
	if err := db.VerifySchema(context.Background(), conn); err != nil {
		log.Fatalf("verify schema: %v", err)
	}

	cfg, err := loadConfig(conn, dbPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("starting horizon_hours=%d low_water_hours=%d tick=%s profile=%s",
		cfg.horizonHours, cfg.lowWaterHours, cfg.tickInterval, cfg.packageProfile)

	tick(ctx, conn, cfg)
	t := time.NewTicker(cfg.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("shutting down")
			return
		case <-t.C:
			tick(ctx, conn, cfg)
		}
	}
}

// loadConfig assembles the runtime config from the DB. The settings table is
// seeded by ApplySchema, so on a fresh install we get the package defaults
// (24/23/300); operators change them from the admin UI. Changes only take
// effect on the next process restart — the ticker is built once here.
func loadConfig(conn *sql.DB, dbPath string) (config, error) {
	tunables, err := db.GetSchedulerTunables(context.Background(), conn)
	if err != nil {
		return config{}, fmt.Errorf("read scheduler tunables: %w", err)
	}
	if tunables.LowWaterHours >= tunables.HorizonHours {
		return config{}, fmt.Errorf("scheduler_low_water_hours (%d) must be < scheduler_horizon_hours (%d)",
			tunables.LowWaterHours, tunables.HorizonHours)
	}
	profile, err := db.GetDefaultPackagedProfile(context.Background(), conn)
	if err != nil {
		return config{}, fmt.Errorf("read default packaged profile: %w", err)
	}
	if profile == "" {
		profile = db.DefaultPackageProfile
	}
	return config{
		dbPath:         dbPath,
		horizonHours:   tunables.HorizonHours,
		lowWaterHours:  tunables.LowWaterHours,
		tickInterval:   time.Duration(tunables.TickSeconds) * time.Second,
		packageProfile: profile,
	}, nil
}

func tick(ctx context.Context, conn *sql.DB, cfg config) {
	result, err := scheduler.ExtendAllEnabled(ctx, conn, scheduler.ServiceOptions{
		HorizonHours:             cfg.horizonHours,
		LowWaterHours:            cfg.lowWaterHours,
		RenditionProfile:         cfg.packageProfile,
		BootstrapRequireAllReady: true,
	})
	if err != nil {
		log.Printf("ERROR extend enabled channels: %v", err)
		return
	}
	for _, ch := range result.Channels {
		if ch.Error != "" {
			log.Printf("ERROR channel=%s extend: %s", ch.ChannelID, ch.Error)
			continue
		}
		if ch.SkippedLowWater {
			continue
		}
		if ch.BootstrapDelayed {
			log.Printf("channel=%s bootstrap delayed ready=%d total=%d profile=%s",
				ch.ChannelID, ch.BootstrapReady, ch.BootstrapTotal, ch.RenditionProfile)
			continue
		}
		log.Printf("channel=%s extending remaining_ms=%d horizon_hours=%d require_packages=%t profile=%s",
			ch.ChannelID, ch.RemainingMs, cfg.horizonHours, ch.RequireReadyPackages, ch.RenditionProfile)
		log.Printf("channel=%s inserted=%d new_end_ms=%d (%s UTC)",
			ch.ChannelID, ch.Inserted, ch.LastEndMs, time.UnixMilli(ch.LastEndMs).UTC().Format(time.RFC3339))
	}
}
