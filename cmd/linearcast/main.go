// linearcast is the multi-channel HLS endpoint.
//
// Its schedule lives in SQLite (see docs/database.md). linearcast opens
// the database and serves per-channel HLS at:
//
//	/channel/<channelID>/stream.m3u8
//	/channel/<channelID>/packaged/init/<packageID>/init.mp4
//	/channel/<channelID>/packaged/segments/<packageID>/<idx>.m4s
//	/channel/<channelID>/session/<sessionID>/init.mp4
//	/channel/<channelID>/session/<sessionID>/<idx>.m4s
//	/channel/<channelID>/plexrelay.m3u8
//	/channel/<channelID>/plexrelay/<viewerToken>/<path>
//	/channel/<channelID>/now
//
// Plus service-level /healthz, /readyz, /status, /metrics.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tckrcr/linearcast/internal/clock"
	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ondemand"
	"github.com/tckrcr/linearcast/internal/packager"
	"github.com/tckrcr/linearcast/internal/plexrelay"
)

const (
	lookaheadMs            int64 = 3 * 60 * 1000
	manifestAheadMs        int64 = 72 * 1000
	packagedManifestLimit        = 24
	defaultAddr                  = ":8888"
	packagedPath                 = "packaged"
	sessionPath                  = "session"
	defaultPackagedProfile       = db.DefaultPackageProfile
	channelRefreshPeriod         = 60 * time.Second
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}
	addr := os.Getenv("LINEARCAST_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.VerifySchema(context.Background(), conn); err != nil {
		log.Fatalf("verify schema: %v", err)
	}
	packagedProfile, err := db.GetDefaultPackagedProfile(context.Background(), conn)
	if err != nil {
		log.Fatalf("read default packaged profile: %v", err)
	}
	if packagedProfile == "" {
		packagedProfile = defaultPackagedProfile
	}
	sessionDir := os.Getenv("LINEARCAST_SESSION_DIR")
	if sessionDir == "" {
		sessionDir = filepath.Join(os.TempDir(), "linearcast-sessions")
	}

	ctxStartup, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	if err := clock.Check(ctxStartup); err != nil {
		cancelStartup()
		log.Fatalf("ntp clock check: %v", err)
	}
	cancelStartup()
	log.Printf("ntp drift within tolerance")

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	burstSec := 0
	if packager.SupportsReadrateBurst(ctx) {
		burstSec = 90
	} else {
		log.Printf("ffmpeg lacks -readrate_initial_burst; on-demand live sessions will encode uncapped")
	}
	sessionSettings, err := db.GetOnDemandSessionSettings(context.Background(), conn)
	if err != nil {
		log.Fatalf("read on-demand session settings: %v", err)
	}
	sessions, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root:           sessionDir,
		BurstSec:       burstSec,
		GraceMs:        int64(sessionSettings.GraceSeconds) * 1000,
		MaxConcurrent:  sessionSettings.MaxConcurrent,
		EvictIdleMs:    int64(sessionSettings.EvictIdleSeconds) * 1000,
		StallTimeoutMs: int64(sessionSettings.StallTimeoutSeconds) * 1000,
		RestartBudget:  sessionSettings.RestartBudget,
		MaxKeepaliveMs: int64(sessionSettings.KeepaliveCeilingSec) * 1000,
	})
	if err != nil {
		log.Fatalf("init on-demand sessions: %v", err)
	}
	defer sessions.Shutdown()

	plexURL, _ := db.GetPlexURL(context.Background(), conn)
	plexToken, _ := db.GetPlexToken(context.Background(), conn)
	var plexRelayManager *plexrelay.Manager
	if plexURL != "" && plexToken != "" {
		plexRelayManager = plexrelay.NewManager(plexURL, plexToken, &http.Client{Timeout: 30 * time.Second})
		log.Printf("plexrelay manager initialized url=%s", plexURL)
	}

	a := &app{
		dbConn:          conn,
		addr:            addr,
		httpClient:      &http.Client{Timeout: 15 * time.Second},
		sessions:        sessions,
		plexRelay:       plexRelayManager,
		packagedProfile: packagedProfile,
		startedAt:       time.Now().UTC(),
		channels:        map[string]*channelRuntime{},
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := a.refreshChannels(ctx); err != nil {
		log.Fatalf("load channels: %v", err)
	}

	go a.channelRefreshLoop(ctx)
	go a.metricsRefreshLoop(ctx)
	go sessions.Run(ctx)
	if a.plexRelay != nil {
		a.plexRelay.Run(ctx)
	}

	log.Printf("linearcast listening addr=%s db=%s packaged_profile=%s channels=%d",
		addr, dbPath, packagedProfile, len(a.snapshotChannels()))
	srv := &http.Server{Addr: addr, Handler: a.routes()}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
