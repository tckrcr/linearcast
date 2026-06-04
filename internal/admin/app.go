package admin

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

const segmentMs = int64(6000)

// App is the admin HTTP application.
type App struct {
	dbConn          *sql.DB
	dbPath          string
	upstreamURL     string
	httpClient      *http.Client
	upstreamCache   *upstreamStatusCache
	now             func() time.Time
	cacheDir        string
	packageRoot     string
	mediaRoot       string
	plexURL         string
	plexPathMap     string
	jellyfinURL     string
	jellyfinPathMap string
	logger          *log.Logger
	schedule        *scheduleService
	ingestJobs      *ingestJobStore
	auth            *authService
	encoderDistDir  string
	subtitleScan    *subtitleScanJob
	subtitleExtract *subtitleExtractJob
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
	CacheDir string
	// PackageRoot is where finalized package directories are written. Defaults
	// to CacheDir/packages when empty.
	PackageRoot string
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
	// Logger is used for structured import logging. Defaults to log.Default().
	Logger *log.Logger
	// EncoderDistDir is the directory that holds cross-compiled
	// linearcast-encoder binaries served from the admin UI when an operator
	// registers a new encoder. Empty disables the download endpoint.
	EncoderDistDir string
}

// effectivePlexURL returns the DB-stored Plex URL, falling back to the
// env-configured value set at startup.
func (a *App) effectivePlexURL() (string, error) {
	stored, err := db.GetPlexURL(context.Background(), a.dbConn)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	return strings.TrimRight(strings.TrimSpace(a.plexURL), "/"), nil
}

// effectivePlexPathMap returns the DB-stored path map, falling back to the
// env-configured value set at startup.
func (a *App) effectivePlexPathMap() (string, error) {
	stored, err := db.GetPlexPathMap(context.Background(), a.dbConn)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	return strings.TrimSpace(a.plexPathMap), nil
}

// effectiveJellyfinPathMap returns the DB-stored Jellyfin path map, falling
// back to the env-configured value set at startup.
func (a *App) effectiveJellyfinPathMap() (string, error) {
	stored, err := db.GetJellyfinPathMap(context.Background(), a.dbConn)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	return strings.TrimSpace(a.jellyfinPathMap), nil
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
		logger = log.Default()
	}
	return &App{
		dbConn:          cfg.DB,
		dbPath:          cfg.DBPath,
		upstreamURL:     cfg.UpstreamURL,
		httpClient:      client,
		upstreamCache:   newUpstreamStatusCache(),
		now:             now,
		cacheDir:        cfg.CacheDir,
		packageRoot:     cfg.PackageRoot,
		mediaRoot:       cfg.MediaRoot,
		plexURL:         cfg.PlexURL,
		plexPathMap:     cfg.PlexPathMap,
		jellyfinURL:     cfg.JellyfinURL,
		jellyfinPathMap: cfg.JellyfinPathMap,
		logger:          logger,
		schedule:        newScheduleService(cfg.DB, now),
		ingestJobs:      newIngestJobStore(cfg.CacheDir),
		auth:            newAuthServiceFromHash(cfg.AdminPasswordHash, cfg.AdminPasswordMustChange, now, cfg.AdminCookieSecure),
		encoderDistDir:  strings.TrimSpace(cfg.EncoderDistDir),
	}
}
