// cmd/linearcast-admin exposes a small read-only JSON API for the future
// operations UI. It keeps playback separate from admin concerns: linearcast
// remains the HLS server, while this process reads SQLite and optionally
// enriches responses from linearcast's /status endpoint.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tckrcr/linearcast/internal/admin"
	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/linearcastlog"
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
	allowNoAuth       bool
	adminCookieSecure bool
}

func loadStartupConfig(getenv func(string) string) (startupConfig, error) {
	cfg := startupConfig{
		dbPath:            getenv("LINEARCAST_DB"),
		addr:              getenv("LINEARCAST_ADMIN_ADDR"),
		upstreamURL:       strings.TrimRight(getenv("LINEARCAST_UPSTREAM_URL"), "/"),
		allowNoAuth:       parseEnvBool(getenv("LINEARCAST_ADMIN_ALLOW_NO_AUTH")),
		adminCookieSecure: parseEnvBool(getenv("LINEARCAST_ADMIN_COOKIE_SECURE")),
	}
	if cfg.dbPath == "" {
		return startupConfig{}, fmt.Errorf("LINEARCAST_DB is required")
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if err := validateListenAddr("LINEARCAST_ADMIN_ADDR", cfg.addr); err != nil {
		return startupConfig{}, err
	}
	if cfg.upstreamURL == "" {
		cfg.upstreamURL = defaultUpstreamURL
	}
	return cfg, nil
}

// validateListenAddr rejects an address that net.Listen would later fail on, so
// a malformed listen address surfaces as a clear config error at startup
// instead of an opaque ListenAndServe failure. The port must be numeric to
// match the nginx upstream the container entrypoint derives from it.
func validateListenAddr(name, addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s must be a host:port listen address (got %q): %w", name, addr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%s must end in a numeric port 1-65535 (got %q)", name, addr)
	}
	return nil
}

// ensureAdminPassword seeds the DB password on first startup and returns the
// hash + must-change flag for the current run.
//
// Fresh install: seed the default packaged password with must-change=true so
// the operator is forced to set their own on first login.
//
// Existing install: return the persisted hash and must-change flag.
func ensureAdminPassword(conn *sql.DB) (hash string, mustChange bool, err error) {
	existing, exists, err := db.GetAdminPasswordHash(context.Background(), conn)
	if err != nil {
		return "", false, fmt.Errorf("read admin password hash: %w", err)
	}

	if !exists {
		h, err := bcrypt.GenerateFromPassword([]byte(defaultAdminPassword), bcrypt.DefaultCost)
		if err != nil {
			return "", false, fmt.Errorf("hash admin password: %w", err)
		}
		if err := db.SetAdminPasswordHash(context.Background(), conn, string(h)); err != nil {
			return "", false, err
		}
		if err := db.SetAdminPasswordMustChange(context.Background(), conn, true); err != nil {
			return "", false, err
		}
		slog.Info("first-run: default admin password set — sign in and change it immediately")
		return string(h), true, nil
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
	linearcastlog.SetupJSON()

	if len(os.Args) > 1 && os.Args[1] == "maint" {
		runMaint(os.Args[2:])
		return
	}

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
	if issues, err := db.ValidateChannelMediaChains(context.Background(), conn); err != nil {
		slog.Warn("chain integrity check failed to run", "err", err)
	} else if len(issues) > 0 {
		slog.Warn("chain integrity issues found",
			"count", len(issues),
		)
		for _, iss := range issues {
			slog.Warn("chain integrity issue",
				"channel", iss.ChannelID,
				"kind", iss.Kind,
				"media", iss.MediaIDs,
				"detail", iss.Detail,
			)
		}
	} else {
		slog.Info("chain integrity: all channel_media chains intact")
	}
	sweeperSettings, err := db.GetEncoderSweeperSettings(context.Background(), conn)
	if err != nil {
		slog.Error("read encoder sweeper settings", "err", err)
		os.Exit(1)
	}

	var adminPasswordHash string
	var adminPasswordMustChange bool
	if !cfg.allowNoAuth {
		adminPasswordHash, adminPasswordMustChange, err = ensureAdminPassword(conn)
		if err != nil {
			slog.Error("admin password setup", "err", err)
			os.Exit(1)
		}
	}

	a := admin.New(admin.Config{
		DB:                      conn,
		DBPath:                  cfg.dbPath,
		UpstreamURL:             cfg.upstreamURL,
		HTTPClient:              &http.Client{Timeout: 2 * time.Second},
		CacheDir:                os.Getenv("CACHE_DIR"),
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
		slog.Info("linearcast-admin listening",
			"addr", cfg.addr,
			"db", cfg.dbPath,
			"upstream", cfg.upstreamURL,
			"auth", "disabled",
			"allow_no_auth", true,
		)
	} else {
		slog.Info("linearcast-admin listening",
			"addr", cfg.addr,
			"db", cfg.dbPath,
			"upstream", cfg.upstreamURL,
			"auth", "password",
			"must_change", adminPasswordMustChange,
		)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	sweeper := admin.NewSweeper(conn)
	sweeper.Interval = time.Duration(sweeperSettings.SweepIntervalSeconds) * time.Second
	sweeper.MaxAttempts = sweeperSettings.MaxAttempts
	go func() {
		if err := sweeper.Run(ctx); err != nil && err != context.Canceled {
			slog.Warn("encoder sweeper exited", "err", err)
		}
	}()
	slog.Info("encoder sweeper started",
		"interval", sweeper.Interval.String(),
		"max_attempts", sweeper.MaxAttempts,
		"stale_processing_timeout", sweeper.StaleProcessingTimeout.String(),
	)

	if err := http.ListenAndServe(cfg.addr, a.Handler()); err != nil {
		slog.Error("server exited", "err", err)
	}
}
