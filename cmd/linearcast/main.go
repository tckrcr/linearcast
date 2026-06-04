// linearcast is the multi-channel HLS endpoint.
//
// Its schedule lives in SQLite (see docs/database.md). linearcast opens
// the database and serves per-channel HLS at:
//
//	/channel/<channelID>/stream.m3u8
//	/channel/<channelID>/packaged/init/<packageID>/init.mp4
//	/channel/<channelID>/packaged/segments/<packageID>/<idx>.m4s
//	/channel/<channelID>/now
//
// Plus service-level /healthz, /readyz, /status, /metrics.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/tckrcr/linearcast/internal/clock"
	"github.com/tckrcr/linearcast/internal/db"
)

const (
	lookaheadMs            int64 = 3 * 60 * 1000
	manifestAheadMs        int64 = 72 * 1000
	packagedManifestLimit        = 24
	defaultAddr                  = ":8888"
	packagedPath                 = "packaged"
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

	ctxStartup, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	if err := clock.Check(ctxStartup); err != nil {
		cancelStartup()
		log.Fatalf("ntp clock check: %v", err)
	}
	cancelStartup()
	log.Printf("ntp drift within tolerance")

	a := &app{
		dbConn:          conn,
		addr:            addr,
		httpClient:      &http.Client{Timeout: 15 * time.Second},
		packagedProfile: packagedProfile,
		startedAt:       time.Now().UTC(),
		channels:        map[string]*channelRuntime{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.refreshChannels(ctx); err != nil {
		log.Fatalf("load channels: %v", err)
	}

	go a.channelRefreshLoop(ctx)
	go a.metricsRefreshLoop(ctx)

	log.Printf("linearcast listening addr=%s db=%s packaged_profile=%s channels=%d",
		addr, dbPath, packagedProfile, len(a.snapshotChannels()))
	if err := http.ListenAndServe(addr, a.routes()); err != nil {
		log.Fatal(err)
	}
}
