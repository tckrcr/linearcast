// linearcast is the multi-channel HLS endpoint.
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

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
	"github.com/tckrcr/linearcast/internal/linearcastlog"
	"github.com/tckrcr/linearcast/internal/liveproxy"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packager"
)

const (
	lookaheadMs            int64 = 3 * 60 * 1000
	manifestAheadMs        int64 = 72 * 1000
	packagedManifestLimit        = 24
	defaultAddr                  = ":8888"
	streamPath                   = "streams"
	encodingPath                 = "encoding"
	onDemandSubtitlePath         = "subs-channel-encoding"
	defaultPackagedProfile       = db.DefaultPackageProfile
	channelRefreshPeriod         = 60 * time.Second
)

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
		packagedProfile = defaultPackagedProfile
	}
	ctxStartup, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	err = runStartupClockCheck(ctxStartup, cfg.clockCheckMode)
	cancelStartup()
	if err != nil {
		slog.Error("ntp clock check", "err", err)
		os.Exit(1)
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
		MinArtifactRetentionMs: cfg.onDemandPlaybackLagMs + cfg.onDemandWarmupMs + onDemandReadyCoverageMs + 10_000,
		DB:                     conn,
	})
	if err != nil {
		slog.Error("init on-demand channel encodings", "err", err)
		os.Exit(1)
	}
	defer encodings.Shutdown()

	a := &app{
		dbConn:                conn,
		addr:                  cfg.addr,
		httpClient:            &http.Client{Timeout: 15 * time.Second},
		externalHLSClient:     liveproxy.NewGuardedClient(externalHLSTimeout, liveproxy.AllowAllAddresses),
		encodings:             encodings,
		packagedProfile:       packagedProfile,
		onDemandPlaybackLagMs: cfg.onDemandPlaybackLagMs,
		onDemandWarmupMs:      cfg.onDemandWarmupMs,
		cache:                 layout.NewCache(cfg.cacheDir),
		startedAt:             time.Now().UTC(),
		channels:              map[string]*channelRuntime{},
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := a.refreshChannels(ctx); err != nil {
		slog.Error("load channels", "err", err)
		os.Exit(1)
	}

	go a.channelRefreshLoop(ctx)
	go a.metricsRefreshLoop(ctx)
	go sampleCacheMetricsLoop(ctx, layout.NewCache(cfg.cacheDir))
	go encodings.Run(ctx)

	slog.Info("linearcast listening",
		"addr", cfg.addr,
		"db", cfg.dbPath,
		"packaged_profile", packagedProfile,
		"channels", len(a.snapshotChannels()),
		"on_demand_playback_lag_ms", cfg.onDemandPlaybackLagMs,
		"on_demand_warmup_ms", cfg.onDemandWarmupMs,
	)
	srv := &http.Server{Addr: cfg.addr, Handler: a.routes()}
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
