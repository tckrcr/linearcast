// cmd/linearcast-admin exposes a small read-only JSON API for the future
// operations UI. It keeps playback separate from admin concerns: linearcast
// remains the HLS server, while this process reads SQLite and optionally
// enriches responses from linearcast's /status endpoint.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tckrcr/linearcast/internal/admin"
	"github.com/tckrcr/linearcast/internal/db"
)

const (
	defaultAddr        = ":8890"
	defaultUpstreamURL = "http://127.0.0.1:8888"
	// defaultAdminPassword is the packaged default for fresh installs. The
	// operator is forced to change it on first login.
	defaultAdminPassword = "linearcast"
)

type startupConfig struct {
	dbPath            string
	addr              string
	upstreamURL       string
	adminPassword     string
	allowNoAuth       bool
	adminCookieSecure bool
}

func loadStartupConfig(getenv func(string) string) (startupConfig, error) {
	cfg := startupConfig{
		dbPath:            getenv("LINEARCAST_DB"),
		addr:              getenv("LINEARCAST_ADMIN_ADDR"),
		upstreamURL:       strings.TrimRight(getenv("LINEARCAST_UPSTREAM_URL"), "/"),
		adminPassword:     strings.TrimSpace(getenv("LINEARCAST_ADMIN_PASSWORD")),
		allowNoAuth:       parseEnvBool(getenv("LINEARCAST_ADMIN_ALLOW_NO_AUTH")),
		adminCookieSecure: parseEnvBool(getenv("LINEARCAST_ADMIN_COOKIE_SECURE")),
	}
	if cfg.dbPath == "" {
		return startupConfig{}, fmt.Errorf("LINEARCAST_DB is required")
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.upstreamURL == "" {
		cfg.upstreamURL = defaultUpstreamURL
	}
	// LINEARCAST_ADMIN_PASSWORD is no longer required: the DB-backed default
	// password is used on fresh installs and existing env passwords are seeded
	// into the DB on first startup. LINEARCAST_ADMIN_ALLOW_NO_AUTH=true
	// disables auth for development or recovery.
	return cfg, nil
}

// ensureAdminPassword seeds the DB password on first startup and returns the
// hash + must-change flag for the current run.
//
// Migration path: if envPassword is set and the DB has no password yet, hash
// the env value and seed it without a must-change flag so existing installs
// keep working without interruption. Log a deprecation notice either way.
//
// Fresh install: seed the default packaged password with must-change=true so
// the operator is forced to set their own on first login.
func ensureAdminPassword(conn *sql.DB, envPassword string) (hash string, mustChange bool, err error) {
	existing, exists, err := db.GetAdminPasswordHash(context.Background(), conn)
	if err != nil {
		return "", false, fmt.Errorf("read admin password hash: %w", err)
	}

	if !exists {
		plaintext := envPassword
		mustChange = plaintext == ""
		if plaintext == "" {
			plaintext = defaultAdminPassword
		}
		h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
		if err != nil {
			return "", false, fmt.Errorf("hash admin password: %w", err)
		}
		if err := db.SetAdminPasswordHash(context.Background(), conn, string(h)); err != nil {
			return "", false, err
		}
		if err := db.SetAdminPasswordMustChange(context.Background(), conn, mustChange); err != nil {
			return "", false, err
		}
		if envPassword != "" {
			log.Printf("LINEARCAST_ADMIN_PASSWORD seeded to DB — remove it from .env to finish migration")
		} else {
			log.Printf("first-run: default admin password set — sign in with %q and change it immediately", defaultAdminPassword)
		}
		return string(h), mustChange, nil
	}

	if envPassword != "" {
		log.Printf("LINEARCAST_ADMIN_PASSWORD is set but DB password already exists — env var ignored; remove it from .env")
	}
	mustChange, err = db.AdminPasswordMustChange(context.Background(), conn)
	if err != nil {
		return "", false, fmt.Errorf("read must-change flag: %w", err)
	}
	return existing, mustChange, nil
}

func parseEnvBool(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// defaultEncoderDistDir resolves the directory served by the encoder download
// endpoint. Honors LINEARCAST_ENCODER_DIST_DIR when set; otherwise falls back
// to /opt/linearcast/encoder-dist which is where the Dockerfile stages the
// cross-compiled binaries.
func defaultEncoderDistDir(value string) string {
	v := strings.TrimSpace(value)
	if v != "" {
		return v
	}
	return "/opt/linearcast/encoder-dist"
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if len(os.Args) > 1 && os.Args[1] == "maint" {
		runMaint(os.Args[2:])
		return
	}

	cfg, err := loadStartupConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	conn, err := db.OpenReadWrite(cfg.dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := db.VerifySchema(context.Background(), conn); err != nil {
		log.Fatalf("verify schema: %v", err)
	}
	if issues, err := db.ValidateChannelMediaChains(context.Background(), conn); err != nil {
		log.Printf("chain integrity check failed to run: %v", err)
	} else if len(issues) > 0 {
		log.Printf("chain integrity: %d issue(s) found across channel_media — boot continues; query /api/admin/chain-integrity for details", len(issues))
		for _, iss := range issues {
			log.Printf("  channel=%s kind=%s media=%v detail=%s", iss.ChannelID, iss.Kind, iss.MediaIDs, iss.Detail)
		}
	} else {
		log.Printf("chain integrity: all channel_media chains intact")
	}
	sweeperSettings, err := db.GetEncoderSweeperSettings(context.Background(), conn)
	if err != nil {
		log.Fatalf("read encoder sweeper settings: %v", err)
	}

	var adminPasswordHash string
	var adminPasswordMustChange bool
	if !cfg.allowNoAuth {
		adminPasswordHash, adminPasswordMustChange, err = ensureAdminPassword(conn, cfg.adminPassword)
		if err != nil {
			log.Fatalf("admin password setup: %v", err)
		}
	}

	a := admin.New(admin.Config{
		DB:                      conn,
		DBPath:                  cfg.dbPath,
		UpstreamURL:             cfg.upstreamURL,
		HTTPClient:              &http.Client{Timeout: 2 * time.Second},
		CacheDir:                os.Getenv("CACHE_DIR"),
		PackageRoot:             os.Getenv("LINEARCAST_PACKAGE_ROOT"),
		MediaRoot:               os.Getenv("LINEARCAST_MEDIA_ROOT"),
		PlexURL:                 os.Getenv("PLEX_URL"),
		PlexPathMap:             os.Getenv("PLEX_PATH_MAP"),
		JellyfinURL:             os.Getenv("JELLYFIN_URL"),
		JellyfinPathMap:         os.Getenv("JELLYFIN_PATH_MAP"),
		AdminPasswordHash:       adminPasswordHash,
		AdminPasswordMustChange: adminPasswordMustChange,
		AdminCookieSecure:       cfg.adminCookieSecure,
		EncoderDistDir:          defaultEncoderDistDir(os.Getenv("LINEARCAST_ENCODER_DIST_DIR")),
	})

	if cfg.allowNoAuth {
		log.Printf("linearcast-admin listening addr=%s db=%s upstream=%s auth=disabled allow_no_auth=true", cfg.addr, cfg.dbPath, cfg.upstreamURL)
	} else {
		log.Printf("linearcast-admin listening addr=%s db=%s upstream=%s auth=password must_change=%v", cfg.addr, cfg.dbPath, cfg.upstreamURL, adminPasswordMustChange)
	}

	// Sweeper runs alongside the HTTP server. It transitions expired encoder
	// leases back to pending (or to failed once the attempts cap is reached).
	// We tie it to SIGINT/SIGTERM so an orderly shutdown lets in-flight sweep
	// transactions commit before the DB closes.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	sweeper := admin.NewSweeper(conn)
	sweeper.Interval = time.Duration(sweeperSettings.SweepIntervalSeconds) * time.Second
	sweeper.MaxAttempts = sweeperSettings.MaxAttempts
	go func() {
		if err := sweeper.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("encoder sweeper exited: %v", err)
		}
	}()
	log.Printf("encoder sweeper interval=%s max_attempts=%d", sweeper.Interval, sweeper.MaxAttempts)

	if err := http.ListenAndServe(cfg.addr, a.Handler()); err != nil {
		log.Fatal(err)
	}
}
