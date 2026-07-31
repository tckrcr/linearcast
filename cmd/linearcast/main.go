// linearcast is the composed HTTP runtime for playback, public viewer metadata,
// protected admin controls, remote encoder transport, and service health.
//
// Its schedule lives in SQLite (see docs/database.md). linearcast opens
// the database and serves per-channel HLS at:
//
//	/channels/<channelID>/stream.m3u8
//	/channels/<channelID>/streams/<profile>/init/<packageID>/init.mp4
//	/channels/<channelID>/streams/<profile>/segments/<packageID>/<idx>.m4s
//	/channels/<channelID>/encoding/<encodingID>/init.mp4
//	/channels/<channelID>/encoding/<encodingID>/<idx>.m4s
//	/channels/<channelID>/now
//	/channels/<channelID>/direct-play
//
// Plus service-level /healthz, /readyz, /status, /metrics.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/linearcastlog"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packager"
	"github.com/tckrcr/linearcast/internal/playback"
)

const defaultAddr = ":8888"

func main() {
	linearcastlog.SetupJSON()

	cfg, err := loadStartupConfig(os.Getenv)
	if err != nil {
		slog.Error("startup config failed", "err", err)
		os.Exit(1)
	}
	conn, err := db.OpenReadWrite(cfg.dbPath)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := db.VerifySchema(context.Background(), conn); err != nil {
		slog.Error("verify schema", "err", err)
		os.Exit(1)
	}
	packagedProfile, err := db.GetDefaultPackagedProfile(context.Background(), conn)
	if err != nil {
		slog.Error("read default packaged profile", "err", err)
		os.Exit(1)
	}
	if packagedProfile == "" {
		packagedProfile = db.DefaultPackageProfile
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	burstSec := 0
	if packager.SupportsReadrateBurst(ctx) {
		burstSec = 45
	} else {
		slog.Warn("ffmpeg lacks -readrate_initial_burst; on-demand channel encodings will encode uncapped")
	}
	encodings, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:                   cfg.encodingDir,
		BurstSec:               burstSec,
		MaxConcurrent:          cfg.onDemandMaxConcurrent,
		MinArtifactRetentionMs: cfg.onDemandPlaybackLagMs + cfg.onDemandWarmupMs + playback.OnDemandReadyCoverageMs + 10_000,
		DB:                     conn,
	})
	if err != nil {
		slog.Error("init on-demand channel encodings", "err", err)
		os.Exit(1)
	}
	defer encodings.Shutdown()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	playbackRuntime, err := playback.NewRuntime(ctx, playback.Config{
		DB:                    conn,
		Encodings:             encodings,
		PackagedProfile:       packagedProfile,
		OnDemandPlaybackLagMs: cfg.onDemandPlaybackLagMs,
		OnDemandWarmupMs:      cfg.onDemandWarmupMs,
		Cache:                 layout.NewCache(cfg.cacheDir),
		StartedAt:             time.Now().UTC(),
	})
	if err != nil {
		slog.Error("init playback runtime", "err", err)
		os.Exit(1)
	}
	adminApp, sweeper, err := newAdminRuntime(ctx, cfg, conn, playbackRuntime, playbackRuntime, playbackRuntime)
	if err != nil {
		slog.Error("init admin runtime", "err", err)
		os.Exit(1)
	}

	prometheus.MustRegister(newScrapeOwner(conn, encodings, cfg.cacheDir, playbackRuntime.ChannelSnapshots))

	go playbackRuntime.Run(ctx)
	go sampleCacheMetricsLoop(ctx, layout.NewCache(cfg.cacheDir))
	go encodings.Run(ctx)
	go func() {
		if err := sweeper.Run(ctx); err != nil && err != context.Canceled {
			slog.Warn("encoder sweeper exited", "err", err)
		}
	}()

	slog.Info("linearcast listening",
		"addr", cfg.addr,
		"db", cfg.dbPath,
		"packaged_profile", packagedProfile,
		"channels", len(playbackRuntime.ChannelSnapshots()),
		"on_demand_playback_lag_ms", cfg.onDemandPlaybackLagMs,
		"on_demand_warmup_ms", cfg.onDemandWarmupMs,
	)
	srv := &http.Server{
		Addr: cfg.addr,
		Handler: composeRoutes(
			playbackRuntime.Handler(),
			adminApp.Handler(),
			playbackRuntime,
			playbackRuntime,
		),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server exited", "err", err)
	}
}
