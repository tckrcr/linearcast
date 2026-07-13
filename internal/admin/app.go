package admin

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/layout"
)

// App is the admin HTTP application.
type App struct {
	dbConn             *sql.DB
	dbPath             string
	upstreamURL        string
	httpClient         *http.Client
	upstreamCache      *upstreamStatusCache
	externalHeartbeat  *externalHeartbeatCache
	now                func() time.Time
	cache              layout.Cache
	mediaRoot          string
	plexURL            string
	plexPathMap        string
	jellyfinURL        string
	jellyfinPathMap    string
	logger             *slog.Logger
	schedule           *scheduleService
	ingestJobs         *ingestJobStore
	auth               *authService
	encoderDistDir     string
	subtitleScan       *subtitleScanJob
	encoderBroadcaster *encoderBroadcaster
}

// Config holds the dependencies for New.
type Config struct {
	DB *sql.DB
	// DBPath is the filesystem path to the SQLite database file. Required for
	// maintenance operations (VACUUM, size reporting); other endpoints work
	// without it.
	DBPath      string
	UpstreamURL string
	HTTPClient  *http.Client
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// CacheDir is the value of CACHE_DIR used by cache summary handlers.
	// Finalized package directories are written under CacheDir/packages.
	CacheDir string
	// MediaRoot is the value of LINEARCAST_MEDIA_ROOT — the directory browser
	// and ingest endpoints are constrained to this root.
	MediaRoot       string
	PlexURL         string
	PlexPathMap     string
	JellyfinURL     string
	JellyfinPathMap string
	// AdminPasswordHash is the bcrypt hash of the admin password loaded from the
	// DB. Empty disables auth (trusted local development / LINEARCAST_ADMIN_ALLOW_NO_AUTH).
	AdminPasswordHash string
	// AdminPasswordMustChange requires the operator to set a new password on
	// first login. Set to true for the packaged default password.
	AdminPasswordMustChange bool
	// AdminCookieSecure sets the Secure flag on the admin session cookie. Enable
	// this only when browsers access the admin UI over HTTPS.
	AdminCookieSecure bool
	// Logger is used for structured import logging. Defaults to slog.Default().
	Logger *slog.Logger
	// EncoderDistDir is the directory that holds cross-compiled
	// linearcast-encoder binaries served from the admin UI when an operator
	// registers a new encoder. Empty disables the download endpoint.
	EncoderDistDir string
}

// effectiveJellyfinURL returns the DB-stored Jellyfin URL, falling back to the
// env-configured value set at startup.
func (a *App) effectiveJellyfinURL() (string, error) {
	stored, err := db.GetJellyfinURL(context.Background(), a.dbConn)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	return strings.TrimRight(strings.TrimSpace(a.jellyfinURL), "/"), nil
}

// New creates an App from cfg. Nil optional fields get safe defaults.
func New(cfg Config) *App {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &App{
		dbConn:             cfg.DB,
		dbPath:             cfg.DBPath,
		upstreamURL:        cfg.UpstreamURL,
		httpClient:         client,
		upstreamCache:      newUpstreamStatusCache(),
		externalHeartbeat:  newExternalHeartbeatCache(),
		now:                now,
		cache:              layout.NewCache(cfg.CacheDir),
		mediaRoot:          cfg.MediaRoot,
		plexURL:            cfg.PlexURL,
		plexPathMap:        cfg.PlexPathMap,
		jellyfinURL:        cfg.JellyfinURL,
		jellyfinPathMap:    cfg.JellyfinPathMap,
		logger:             logger,
		schedule:           newScheduleService(cfg.DB, now),
		ingestJobs:         newIngestJobStore(cfg.CacheDir),
		auth:               newAuthServiceFromHash(cfg.AdminPasswordHash, cfg.AdminPasswordMustChange, now, cfg.AdminCookieSecure),
		encoderDistDir:     strings.TrimSpace(cfg.EncoderDistDir),
		encoderBroadcaster: newEncoderBroadcaster(),
	}
}
